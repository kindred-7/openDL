package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"openDL/i18n"
	"openDL/winauth"

	"golang.org/x/net/idna"
)

func sameProxyTLSAddress(addr string, proxy *url.URL) bool {
	if proxy == nil {
		return false
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	proxyPort := proxy.Port()
	if proxyPort == "" {
		proxyPort = "443"
	}
	proxyHost := proxy.Hostname()
	if ip := net.ParseIP(proxyHost); ip != nil {
		return net.ParseIP(host) != nil && net.ParseIP(host).Equal(ip) && port == proxyPort
	}
	asciiHost, err := idna.Lookup.ToASCII(proxyHost)
	return err == nil && strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(asciiHost, ".")) && port == proxyPort
}

// tls-server-end-point application data. The platform authentication backend
// is responsible for wrapping it in its native channel-binding structure.
// MD5/SHA-1 certificate signatures are upgraded to SHA-256 per RFC 5929.
func integratedChannelBinding(state *tls.ConnectionState) ([]byte, error) {
	if state == nil {
		return nil, nil
	}
	if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return nil, i18n.NewError("Windows 集成认证要求经过验证的 TLS 服务端证书")
	}
	cert := state.PeerCertificates[0]
	var digest []byte
	switch cert.SignatureAlgorithm {
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		h := sha512.Sum384(cert.Raw)
		digest = h[:]
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		h := sha512.Sum512(cert.Raw)
		digest = h[:]
	case x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.ECDSAWithSHA1, x509.SHA256WithRSA, x509.DSAWithSHA256, x509.ECDSAWithSHA256, x509.SHA256WithRSAPSS:
		h := sha256.Sum256(cert.Raw)
		digest = h[:]
	default:
		return nil, i18n.NewError("TLS 证书签名算法暂不支持 Windows 通道绑定")
	}
	return append([]byte("tls-server-end-point:"), digest...), nil
}

func integratedHeader(headers http.Header, scheme string) (string, bool) {
	return integratedChallengeHeader(headers, "WWW-Authenticate", scheme)
}

func integratedChallengeHeader(headers http.Header, name, scheme string) (string, bool) {
	found := false
	for _, header := range headers.Values(name) {
		for _, part := range authParts(header) {
			fields := strings.Fields(part)
			if len(fields) > 0 && strings.EqualFold(fields[0], scheme) {
				if len(fields) == 1 {
					found = true
					continue
				}
				if len(fields) == 2 {
					return fields[1], true
				}
			}
		}
	}
	return "", found
}

func decodeIntegratedToken(token string) ([]byte, error) {
	if len(token) > 2<<20 {
		return nil, i18n.NewError("Windows 认证令牌超过大小限制")
	}
	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil || len(data) > 1<<20 {
		return nil, i18n.NewError("Windows 服务端认证令牌无效")
	}
	return data, nil
}

func drainIntegratedChallenge(resp *http.Response) error {
	if resp.Body == nil {
		return nil
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, (64<<10)+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 64<<10 {
		return i18n.NewError("Windows 认证响应超过大小限制")
	}
	return nil
}

type integratedExchange struct {
	route http.RoundTripper
	site  WindowsAuthSite
	task  *Task
}

func (t integratedExchange) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	resp, err := t.route.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		if err == nil && resp.StatusCode == http.StatusProxyAuthRequired {
			resp.Body.Close()
			return nil, i18n.NewError("站点与代理的集成认证无法同时用于此 HTTP 连接")
		}
		return resp, err
	}
	scheme := "Negotiate"
	initial, ok := integratedHeader(resp.Header, scheme)
	if !ok {
		if initial, ok = integratedHeader(resp.Header, "NTLM"); ok && t.site.Policy == AuthAllowNTLM {
			scheme = "NTLM"
		} else {
			resp.Body.Close()
			if t.site.Policy == AuthKerberosOnly {
				return nil, i18n.NewError("站点未提供 Kerberos/Negotiate 认证，已禁止 NTLM 回退")
			}
			return nil, i18n.NewError("站点未提供 Windows 集成认证")
		}
	}
	var bindings []byte
	if req.URL.Scheme == "https" {
		if resp.TLS == nil {
			resp.Body.Close()
			return nil, i18n.NewError("系统认证要求经过验证的站点 TLS 连接")
		}
		bindings, err = integratedChannelBinding(resp.TLS)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	if err = drainIntegratedChallenge(resp); err != nil {
		return nil, err
	}
	spn := t.site.SPN
	if spn == "" {
		spn = "HTTP/" + req.URL.Hostname()
	}
	session, err := winauth.New(spn, t.site.Policy == AuthKerberosOnly, scheme == "NTLM", bindings)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var initialToken []byte
	if initial != "" {
		initialToken, err = decodeIntegratedToken(initial)
		if err != nil {
			return nil, err
		}
	}
	token, complete, err := session.Next(initialToken)
	if err != nil {
		return nil, err
	}
	for round := 0; round < 8; round++ {
		if err = req.Context().Err(); err != nil {
			return nil, err
		}
		if len(token) == 0 {
			return nil, i18n.NewError("Windows 集成认证未生成请求令牌")
		}
		next := req.Clone(req.Context())
		next.Header.Set("Authorization", scheme+" "+base64.StdEncoding.EncodeToString(token))
		resp, err = t.route.RoundTrip(next)
		if err != nil {
			return nil, err
		}
		// Outer Client redirects see the clean request, never SSPI tokens.
		resp.Request = req
		serverToken, hasToken := integratedHeader(resp.Header, scheme)
		if resp.StatusCode == http.StatusUnauthorized {
			closed := resp.Close
			if err = drainIntegratedChallenge(resp); err != nil {
				return nil, err
			}
			if closed {
				return nil, i18n.NewError("服务器在 Windows 认证期间关闭连接")
			}
			if complete || !hasToken || serverToken == "" {
				return nil, i18n.NewError("Windows 集成认证被服务器拒绝")
			}
			data, err := decodeIntegratedToken(serverToken)
			if err != nil {
				return nil, err
			}
			token, complete, err = session.Next(data)
			if err != nil {
				return nil, err
			}
			continue
		}
		// A redirect can be the server's successful authenticated response.
		// Verify its final token and negotiated package before allowing the
		// outer client to follow Location. Error responses do not publish data
		// or authorize a subsequent redirect and may be returned as failures.
		if resp.StatusCode >= 400 {
			return resp, nil
		}
		if hasToken && serverToken != "" {
			data, err := decodeIntegratedToken(serverToken)
			if err != nil {
				resp.Body.Close()
				return nil, err
			}
			var final []byte
			final, complete, err = session.Next(data)
			if err != nil {
				resp.Body.Close()
				return nil, err
			}
			if len(final) > 0 {
				resp.Body.Close()
				return nil, i18n.NewError("Windows 认证仍需额外令牌交换，无法接受当前响应")
			}
		}
		if !complete {
			resp.Body.Close()
			return nil, i18n.NewError("Windows 集成认证尚未完成，未接受下载内容")
		}
		protocol, err := session.Package()
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		// Resource extraction can authenticate without a download Task.
		if t.task != nil {
			t.task.recordAuthProtocol(protocol)
		}
		return resp, nil
	}
	return nil, i18n.NewError("Windows 集成认证交换次数超过限制")
}

// Every exchange gets one private HTTP/1.1 connection and one slot from the
// existing shared limiters. It cannot reuse another task's system identity.
func integratedRoundTrip(base http.RoundTripper, req *http.Request, site WindowsAuthSite, task *Task) (*http.Response, error) {
	if !winauth.Available {
		return nil, i18n.NewError("当前版本未启用系统集成认证")
	}
	if req.Body != nil && req.Body != http.NoBody {
		return nil, i18n.NewError("Windows 集成认证仅支持无请求体的下载")
	}
	if req.Host != "" && !strings.EqualFold(req.Host, req.URL.Host) {
		return nil, i18n.NewError("请求主机与 Windows 集成认证站点不一致")
	}
	var gate *originTransport
	var proxyPassword string
	var configuredProxy *url.URL
	if limited, ok := base.(*originTransport); ok {
		gate = limited
		base = limited.base
	}
	if proxy, ok := base.(*proxyTransport); ok {
		proxy.mu.RLock()
		base = proxy.current
		blocked := proxy.blocked
		proxyPassword = proxy.config.Password
		if proxy.config.IntegratedAuth && req.URL.Scheme == "http" {
			configuredProxy = proxy.proxyHTTP
		}
		proxy.mu.RUnlock()
		if blocked != nil {
			return nil, blocked
		}
	}
	route, ok := base.(*http.Transport)
	if !ok {
		return nil, i18n.NewError("当前网络传输不支持隔离的 Windows 集成认证")
	}
	transport := route.Clone()
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.DisableKeepAlives = false
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	// Resolve once so the authentication exchange and its TLS dialer agree on
	// whether the first TLS peer is an HTTPS proxy or the target server.
	var tlsProxy *url.URL
	if req.URL.Scheme == "https" && transport.Proxy != nil {
		selected, err := transport.Proxy(req)
		if err != nil {
			return nil, &proxyRequestError{err: err, password: proxyPassword}
		}
		if selected == nil {
			transport.Proxy = nil
		} else {
			transport.Proxy = http.ProxyURL(selected)
			if strings.EqualFold(selected.Scheme, "https") {
				tlsProxy = selected
			}
		}
	}
	proxyTLSConfig := transport.TLSClientConfig.Clone()
	previous := transport.TLSClientConfig.VerifyConnection
	var peerMu sync.Mutex
	var peer []byte
	checkPeer := func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
			return i18n.NewError("Windows 集成认证要求经过验证的 TLS 服务端证书")
		}
		peerMu.Lock()
		defer peerMu.Unlock()
		if peer == nil {
			peer = append([]byte(nil), state.PeerCertificates[0].Raw...)
		} else if !bytes.Equal(peer, state.PeerCertificates[0].Raw) {
			return i18n.NewError("认证期间 TLS 服务端证书发生变化")
		}
		return nil
	}
	transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if previous != nil {
			if err := previous(state); err != nil {
				return err
			}
		}
		return checkPeer(state)
	}
	// A custom TLS dialer may not use the cloned TLSClientConfig. With an
	// HTTPS proxy it dials the proxy first; the origin handshake happens later
	// inside the CONNECT tunnel and uses the origin callback above.
	previousDial, previousLegacyDial := transport.DialTLSContext, transport.DialTLS
	if req.URL.Scheme == "https" && (previousDial != nil || previousLegacyDial != nil || tlsProxy != nil) {
		transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			if previousDial != nil {
				conn, err = previousDial(ctx, network, addr)
			} else if previousLegacyDial != nil {
				conn, err = previousLegacyDial(network, addr)
			} else {
				dial := transport.DialContext
				if dial == nil {
					dial = (&net.Dialer{}).DialContext
				}
				conn, err = dial(ctx, network, addr)
				if err == nil {
					conn, err = handshakeProxyTLS(ctx, conn, addr, proxyTLSConfig)
				}
			}
			if err != nil {
				return nil, err
			}
			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				conn.Close()
				return nil, i18n.NewError("系统认证要求经过验证的站点 TLS 连接")
			}
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			state := tlsConn.ConnectionState()
			if sameProxyTLSAddress(addr, tlsProxy) {
				if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
					conn.Close()
					return nil, i18n.NewError("系统认证要求经过验证的代理 TLS 连接")
				}
				return conn, nil
			}
			if err := checkPeer(state); err != nil {
				conn.Close()
				return nil, err
			}
			return conn, nil
		}
	}
	var authRoute http.RoundTripper = transport
	if configuredProxy != nil && transport.Proxy != nil {
		selected, err := transport.Proxy(req)
		if err != nil {
			return nil, &proxyRequestError{err: err, password: proxyPassword}
		}
		if selected != nil && strings.EqualFold(selected.Scheme, configuredProxy.Scheme) && strings.EqualFold(selected.Host, configuredProxy.Host) {
			transport.Proxy = http.ProxyURL(selected)
			authRoute = isolatedProxyExchange{transport: transport, proxyURL: selected}
		}
	}
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(30*time.Second, cancel)
	cleanup := func() { timer.Stop(); cancel(); transport.CloseIdleConnections() }
	clean := req.Clone(ctx)
	exchange := integratedExchange{route: authRoute, site: site, task: task}
	var resp *http.Response
	var err error
	if gate != nil {
		resp, err = gate.roundTrip(clean, exchange)
	} else {
		resp, err = exchange.RoundTrip(clean)
	}
	if err != nil {
		cleanup()
		return nil, &proxyRequestError{err: err, password: proxyPassword}
	}
	timer.Stop()
	resp.Request = req
	resp.Body = &ntlmBody{ReadCloser: resp.Body, cleanup: cleanup}
	return resp, nil
}
