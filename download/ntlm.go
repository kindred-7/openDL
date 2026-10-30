package download

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"openDL/i18n"

	"github.com/Azure/go-ntlmssp"
)

func ntlmChallenge(header http.Header) bool {
	for _, value := range header.Values("WWW-Authenticate") {
		for _, part := range authParts(value) {
			fields := strings.Fields(part)
			if len(fields) > 0 && (strings.EqualFold(fields[0], "NTLM") || strings.EqualFold(fields[0], "Negotiate")) {
				return true
			}
		}
	}
	return false
}

type requestTransport func(*http.Request) (*http.Response, error)

func (f requestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Drain bounded 401 responses before the next handshake step. This makes the
// single HTTP/1.1 connection reusable even if a challenge has a response body.
type ntlmHandshakeTransport struct{ base http.RoundTripper }

func (t ntlmHandshakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if len(data) > 64<<10 {
		return nil, i18n.NewError("NTLM 认证响应超过大小限制")
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

type ntlmBody struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (b *ntlmBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.cleanup)
	}
	return n, err
}
func (b *ntlmBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.cleanup); return err }

// A fresh route per negotiated request prevents NTLM's connection-scoped
// identity from bleeding into another task or credential set. Handshake and
// final response retain the existing origin/global connection-limit slot.
func doNTLMRequest(client *http.Client, challenge *http.Response, username, password string) (*http.Response, error) {
	challenge.Body.Close()
	if challenge.Request == nil || challenge.Request.URL == nil {
		return nil, i18n.NewError("NTLM 认证响应缺少请求地址")
	}
	original := challenge.Request
	if original.Body != nil && original.Body != http.NoBody {
		return nil, i18n.NewError("NTLM 认证仅支持无请求体的下载")
	}
	var gate *originTransport
	var proxyPassword string
	base := client.Transport
	if integrated, ok := base.(windowsAuthTransport); ok {
		base = integrated.base
	}
	if limited, ok := base.(*originTransport); ok {
		gate = limited
		base = limited.base
	}
	if proxy, ok := base.(*proxyTransport); ok {
		proxy.mu.RLock()
		base = proxy.current
		blocked := proxy.blocked
		proxyPassword = proxy.config.Password
		proxy.mu.RUnlock()
		if blocked != nil {
			return nil, blocked
		}
	}
	route, ok := base.(*http.Transport)
	if !ok {
		return nil, i18n.NewError("当前网络传输不支持隔离的 NTLM 认证")
	}
	transport := route.Clone()
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.DisableKeepAlives = false
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	negotiator := ntlmssp.Negotiator{RoundTripper: ntlmHandshakeTransport{base: transport}}
	ctx, cancel := context.WithCancel(original.Context())
	timer := time.AfterFunc(30*time.Second, cancel)
	cleanup := func() { timer.Stop(); cancel(); transport.CloseIdleConnections() }
	isolated := *client
	isolated.Transport = requestTransport(func(req *http.Request) (*http.Response, error) {
		if gate != nil {
			return gate.roundTrip(req, negotiator)
		}
		return negotiator.RoundTrip(req)
	})
	redirect := client.CheckRedirect
	isolated.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(original.URL, req.URL) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 8 {
			return i18n.NewError("重定向次数过多")
		}
		if redirect != nil {
			return redirect(req, via)
		}
		return nil
	}
	req := original.Clone(ctx)
	address := *original.URL
	address.User = nil
	req.URL = &address
	req.SetBasicAuth(username, password)
	resp, err := isolated.Do(req)
	if err != nil {
		cleanup()
		return nil, &proxyRequestError{err: err, password: proxyPassword}
	}
	timer.Stop()
	resp.Body = &ntlmBody{ReadCloser: resp.Body, cleanup: cleanup}
	// The isolated NTLM client stops at a cross-origin redirect. Continue it
	// with the normal transport only after releasing the authenticated route;
	// never copy its Basic credentials, NTLM token, cookies, or custom headers.
	if (original.Method == http.MethodGet || original.Method == http.MethodHead) && (resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308) {
		destination, locationErr := resp.Location()
		if locationErr == nil && destination != nil && !sameOrigin(original.URL, destination) {
			destination.User = nil
			next := original.Clone(original.Context())
			next.URL = destination
			next.Host = ""
			if err := checkDownloadRedirect(next, []*http.Request{original}); err != nil {
				resp.Body.Close()
				return nil, err
			}
			if redirect != nil {
				if err := redirect(next, []*http.Request{original}); err != nil {
					if err == http.ErrUseLastResponse {
						return resp, nil
					}
					resp.Body.Close()
					return nil, err
				}
			}
			resp.Body.Close()
			return client.Do(next)
		}
	}
	return resp, nil
}
