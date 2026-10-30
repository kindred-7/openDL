package download

// 代理集成认证：407 的 Negotiate/NTLM 挑战用 Windows SSPI 当前用户身份应
// 答。HTTP 目标在请求层挑战应答；HTTPS 隧道在 CONNECT 层挑战应答，NTLM
// 的连接绑定要求挑战应答复用同一条连接。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"openDL/i18n"
	"openDL/winauth"
)

// proxyIntegratedScheme 从 407 的 Proxy-Authenticate 中挑选可协商方案。
func proxyIntegratedScheme(header http.Header) (string, string, bool) {
	for _, scheme := range []string{"Negotiate", "NTLM"} {
		if token, ok := integratedChallengeHeader(header, "Proxy-Authenticate", scheme); ok {
			return scheme, token, true
		}
	}
	return "", "", false
}

// decodeProxyToken 解码挑战令牌的 Base64 载荷。
func decodeProxyToken(token string) ([]byte, error) {
	if len(token) > 2<<20 {
		return nil, i18n.NewError("系统认证令牌超过大小限制")
	}
	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf(i18n.T("Windows 集成认证令牌无效"))
	}
	return data, nil
}

// A successful proxy response may carry the final mutual-authentication token.
func finishProxyAuthentication(session winauth.Session, scheme string, complete bool, header http.Header) error {
	token, hasToken := integratedChallengeHeader(header, "Proxy-Authenticate", scheme)
	if hasToken && token != "" {
		data, err := decodeProxyToken(token)
		if err != nil {
			return err
		}
		var output []byte
		output, complete, err = session.Next(data)
		if err != nil {
			return err
		}
		if len(output) != 0 {
			return i18n.NewError("代理认证仍需额外令牌交换")
		}
	}
	if !complete {
		return i18n.NewError("代理集成认证尚未完成")
	}
	_, err := session.Package()
	return err
}

// proxyIntegratedSession 为代理建立 SSPI 会话；spn 使用代理主机。
func proxyIntegratedSession(proxyHost, scheme string, binding []byte) (winauth.Session, error) {
	return winauth.New("HTTP/"+proxyHost, false, scheme == "NTLM", binding)
}

// A private transport can authenticate the proxy and the origin on the same
// connection. It belongs to a single origin exchange and is closed by its owner.
type isolatedProxyExchange struct {
	transport *http.Transport
	proxyURL  *url.URL
}

func (p isolatedProxyExchange) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := p.transport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusProxyAuthRequired {
		return resp, err
	}
	scheme, initial, ok := proxyIntegratedScheme(resp.Header)
	var binding []byte
	if strings.EqualFold(p.proxyURL.Scheme, "https") {
		if resp.TLS == nil {
			resp.Body.Close()
			return nil, i18n.NewError("代理认证要求经过验证的 TLS 连接")
		}
		binding, err = integratedChannelBinding(resp.TLS)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	closed := resp.Close
	if err = drainIntegratedChallenge(resp); err != nil {
		return nil, err
	}
	if closed || !ok {
		return nil, i18n.NewError("代理拒绝系统集成认证")
	}
	session, err := proxyIntegratedSession(p.proxyURL.Hostname(), scheme, binding)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var serverToken []byte
	if initial != "" {
		serverToken, err = decodeProxyToken(initial)
		if err != nil {
			return nil, err
		}
	}
	token, complete, err := session.Next(serverToken)
	if err != nil {
		return nil, err
	}
	for round := 0; round < 8; round++ {
		if len(token) == 0 {
			return nil, i18n.NewError("Windows 集成认证未生成请求令牌")
		}
		next := req.Clone(req.Context())
		next.Header.Set("Proxy-Authorization", scheme+" "+base64.StdEncoding.EncodeToString(token))
		resp, err = p.transport.RoundTrip(next)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusProxyAuthRequired {
			if err := finishProxyAuthentication(session, scheme, complete, resp.Header); err != nil {
				resp.Body.Close()
				return nil, err
			}
			resp.Request = req
			return resp, nil
		}
		challenge, found := integratedChallengeHeader(resp.Header, "Proxy-Authenticate", scheme)
		closed = resp.Close
		if err = drainIntegratedChallenge(resp); err != nil {
			return nil, err
		}
		if closed || complete || !found || challenge == "" {
			return nil, i18n.NewError("代理拒绝系统集成认证")
		}
		serverToken, err = decodeProxyToken(challenge)
		if err != nil {
			return nil, err
		}
		token, complete, err = session.Next(serverToken)
		if err != nil {
			return nil, err
		}
	}
	return nil, i18n.NewError("代理的 Windows 集成认证轮次过多")
}

// handshakeProxyTLS 在已建立的连接上完成到目标的 TLS 握手；保留 HTTP/2
// 协商能力。
func handshakeProxyTLS(ctx context.Context, conn net.Conn, addr string, base *tls.Config) (net.Conn, error) {
	config := &tls.Config{}
	if base != nil {
		config = base.Clone()
	}
	if config.ServerName == "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			config.ServerName = host
		}
	}
	if len(config.NextProtos) == 0 {
		config.NextProtos = []string{"h2", "http/1.1"}
	}
	client := tls.Client(conn, config)
	if err := client.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

// bufferedConn 保留 CONNECT 应答读取时的缓冲余量。
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// Bound the response headers before handing them to the standard HTTP parser.
// Preserve the parser's buffered bytes for the next exchange or the tunnel.
func readProxyResponse(reader *bufio.Reader, request *http.Request) (*http.Response, *bufio.Reader, error) {
	var raw bytes.Buffer
	for {
		fragment, err := reader.ReadSlice('\n')
		if raw.Len()+len(fragment) > 64<<10 {
			return nil, nil, i18n.NewError("代理应答头超过大小限制")
		}
		_, _ = raw.Write(fragment)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if bytes.Equal(fragment, []byte("\r\n")) || bytes.Equal(fragment, []byte("\n")) {
			break
		}
	}
	parsedReader := bufio.NewReader(io.MultiReader(bytes.NewReader(raw.Bytes()), reader))
	response, err := http.ReadResponse(parsedReader, request)
	return response, parsedReader, err
}

// Drain bounded content, including chunked encoding and trailers, before reuse.
func drainProxyBody(response *http.Response) error {
	if response.Close {
		return i18n.NewError("代理在认证期间关闭连接")
	}
	if response.ContentLength < 0 && len(response.TransferEncoding) == 0 {
		return i18n.NewError("代理认证响应正文长度无效")
	}
	if response.ContentLength > 64<<10 {
		return i18n.NewError("代理认证响应正文长度无效")
	}
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return err
	}
	if n > 64<<10 {
		return i18n.NewError("代理认证响应正文长度无效")
	}
	return response.Body.Close()
}

// dialProxyTunnel 经代理建立到目标地址的 CONNECT 隧道；集成认证开启时代
// 理 407 的挑战应答，成功后返回带缓冲余量的连接。
func dialProxyTunnel(ctx context.Context, proxyURL *url.URL, targetAddr string, integrated bool, username, password string) (net.Conn, error) {
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("连接代理服务器失败：%w"), err)
	}
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
		}
	}()
	var proxyBinding []byte
	if strings.EqualFold(proxyURL.Scheme, "https") {
		proxyTLS := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname(), NextProtos: []string{"http/1.1"}})
		if err := proxyTLS.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf(i18n.T("代理 TLS 握手失败：%w"), err)
		}
		state := proxyTLS.ConnectionState()
		proxyBinding, err = integratedChannelBinding(&state)
		if err != nil {
			return nil, err
		}
		conn = proxyTLS
	} else if !strings.EqualFold(proxyURL.Scheme, "http") {
		return nil, i18n.NewError("代理集成认证不支持此代理协议")
	}
	reader := bufio.NewReader(conn)
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	header := http.Header{}
	if proxyURL.User != nil {
		if account := proxyURL.User.Username(); account != "" {
			passwordValue, _ := proxyURL.User.Password()
			header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(account+":"+passwordValue)))
		}
	} else if username != "" {
		header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	}
	var session winauth.Session
	scheme := ""
	complete := false
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	for round := 0; round < 8; round++ {
		request := &http.Request{
			Method: http.MethodConnect,
			URL:    &url.URL{Opaque: targetAddr},
			Host:   targetAddr,
			Header: header.Clone(),
		}
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		if err := request.Write(conn); err != nil {
			return nil, fmt.Errorf(i18n.T("代理隧道请求失败：%w"), err)
		}
		response, nextReader, err := readProxyResponse(reader, request)
		if err != nil {
			return nil, fmt.Errorf(i18n.T("代理隧道应答无效：%w"), err)
		}
		reader = nextReader
		status, responseHeader := response.StatusCode, response.Header
		if status != http.StatusProxyAuthRequired {
			if status != 200 {
				return nil, fmt.Errorf(i18n.T("代理隧道被拒绝（HTTP %d）"), status)
			}
			if session != nil {
				if err := finishProxyAuthentication(session, scheme, complete, responseHeader); err != nil {
					return nil, err
				}
			}
			_ = conn.SetDeadline(time.Time{})
			failed = false
			return &bufferedConn{Conn: conn, reader: reader}, nil
		}
		challengeScheme, challengeToken, ok := proxyIntegratedScheme(responseHeader)
		if session != nil {
			challengeScheme = scheme
			challengeToken, ok = integratedChallengeHeader(responseHeader, "Proxy-Authenticate", scheme)
		}
		if err := drainProxyBody(response); err != nil {
			return nil, err
		}
		if !integrated || !ok {
			return nil, &proxyAuthenticationError{}
		}
		if session == nil {
			if session, err = proxyIntegratedSession(proxyURL.Hostname(), challengeScheme, proxyBinding); err != nil {
				return nil, err
			}
			scheme = challengeScheme
		} else if !strings.EqualFold(scheme, challengeScheme) || complete || challengeToken == "" {
			return nil, i18n.NewError("代理拒绝系统集成认证")
		}
		var serverToken []byte
		if challengeToken != "" {
			if serverToken, err = decodeProxyToken(challengeToken); err != nil {
				return nil, err
			}
		}
		token, done, err := session.Next(serverToken)
		if err != nil {
			return nil, err
		}
		complete = done
		if len(token) == 0 {
			return nil, i18n.NewError("Windows 集成认证未生成请求令牌")
		}
		header.Set("Proxy-Authorization", scheme+" "+base64.StdEncoding.EncodeToString(token))
	}
	return nil, i18n.NewError("代理隧道的 Windows 集成认证轮次过多")
}
