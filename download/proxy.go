package download

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"

	"openDL/i18n"
)

// parseSystemProxyServer 解析 Windows 的 ProxyServer：单一地址或按协议
// （http=...;https=...）形式；仅取 HTTP 与 HTTPS。
func parseSystemProxyServer(server string) (httpAddress, httpsAddress string) {
	if strings.Contains(server, "=") {
		socksAddress := ""
		for _, entry := range strings.FieldsFunc(server, func(r rune) bool { return r == ';' || r == ' ' || r == ',' }) {
			if name, value, ok := strings.Cut(entry, "="); ok {
				switch strings.ToLower(strings.TrimSpace(name)) {
				case "http":
					httpAddress = strings.TrimSpace(value)
				case "https":
					httpsAddress = strings.TrimSpace(value)
				case "socks":
					socksAddress = strings.TrimSpace(value)
				}
			}
		}
		if httpAddress == "" && httpsAddress == "" && socksAddress != "" {
			return "socks5://" + socksAddress, "socks5://" + socksAddress
		}
		return httpAddress, httpsAddress
	}
	server = strings.TrimSpace(server)
	return server, server
}

// resolveSystemProxy 把系统代理声明折算为按协议地址与绕过列表。PAC 自动
// 配置无法静态求值，按环境代理继续并说明。
func resolveSystemProxy() (httpAddress, httpsAddress, bypass, pac, note string) {
	settings, readNote := platformSystemProxy()
	if !settings.available {
		return "", "", "", "", readNote
	}
	if settings.enabled {
		httpAddress, httpsAddress = parseSystemProxyServer(settings.server)
	}
	if httpAddress == "" && httpsAddress == "" {
		if settings.autoConfigURL != "" {
			if platformPACAvailable() {
				return "", "", "", settings.autoConfigURL, "系统代理为 PAC 自动配置，按系统求值"
			}
			return "", "", "", "", "系统代理为 PAC 自动配置，当前平台无法求值；按环境代理继续"
		}
		return "", "", "", "", "系统未配置代理；按环境代理继续"
	}
	entries := strings.FieldsFunc(settings.override, func(r rune) bool { return r == ';' || r == ',' || r == ' ' })
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.EqualFold(entry, "<local>") {
			continue
		}
		kept = append(kept, entry)
	}
	return httpAddress, httpsAddress, strings.Join(kept, ","), "", "系统代理已应用"
}

// ProxyNote 报告代理路由的补充说明（例如系统 PAC 未解析）。
func (m *Manager) ProxyNote() string {
	if m.proxy == nil {
		return ""
	}
	m.proxy.mu.RLock()
	defer m.proxy.mu.RUnlock()
	return m.proxy.note
}

// errPACUnavailable 报告当前平台无法求值 PAC 自动配置。
var errPACUnavailable = errors.New("当前平台不支持 PAC 自动配置求值")

// pacCacheEntry 是一次 PAC 求值的缓存结果。
type pacCacheEntry struct {
	proxy *url.URL
	at    time.Time
}

// pacProxyFunc 用系统 PAC 为目标地址求值代理；按来源主机与协议缓存，求值
// 失败按直连处理并在缓存期内不重试。
func pacProxyFunc(pacURL string) func(*url.URL) (*url.URL, error) {
	var mu sync.Mutex
	cache := map[string]pacCacheEntry{}
	return func(target *url.URL) (*url.URL, error) {
		if target == nil {
			return nil, nil
		}
		key := target.Scheme + "://" + strings.ToLower(target.Hostname())
		now := time.Now()
		mu.Lock()
		entry, ok := cache[key]
		mu.Unlock()
		if ok && now.Sub(entry.at) < 10*time.Minute {
			return entry.proxy, nil
		}
		declaration, err := platformPACProxy(pacURL, target.String())
		if err != nil {
			declaration = ""
		}
		var proxy *url.URL
		if declaration != "" {
			httpAddress, httpsAddress := parseSystemProxyServer(declaration)
			address := httpAddress
			if target.Scheme == "https" {
				address = httpsAddress
			}
			if address != "" {
				target := address
				if !strings.Contains(target, "://") {
					target = "http://" + target
				}
				if parsed, parseErr := url.Parse(target); parseErr == nil {
					proxy = parsed
				}
			}
		}
		mu.Lock()
		if len(cache) >= 256 {
			cache = map[string]pacCacheEntry{}
		}
		cache[key] = pacCacheEntry{proxy: proxy, at: now}
		mu.Unlock()
		return proxy, nil
	}
}

// ProxyConfig configures the download transport, never the local desktop API.
// Password is deliberately excluded from serialization. An empty mode preserves
// the pre-existing environment/default transport behavior.
type ProxyConfig struct {
	Mode         string `json:"mode,omitempty"`
	Address      string `json:"address,omitempty"`
	HTTPAddress  string `json:"http_address,omitempty"`  // 覆盖 HTTP 代理地址
	HTTPSAddress string `json:"https_address,omitempty"` // 覆盖 HTTPS 代理地址
	Bypass       string `json:"bypass,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"-"`
	// IntegratedAuth 用 Windows 当前用户身份完成代理的 Negotiate/NTLM 挑战。
	IntegratedAuth bool `json:"integrated_auth,omitempty"`
}

func NormalizeProxyConfig(value ProxyConfig) (ProxyConfig, error) {
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	if value.Mode == "" {
		value.Mode = "environment"
	}
	if value.Mode != "environment" && value.Mode != "direct" && value.Mode != "manual" && value.Mode != "system" {
		return ProxyConfig{}, fmt.Errorf(i18n.T("代理模式无效"))
	}
	var err error
	if value.Address, err = normalizeProxyAddress(value.Address); err != nil {
		return ProxyConfig{}, err
	}
	if value.HTTPAddress, err = normalizeProxyAddress(value.HTTPAddress); err != nil {
		return ProxyConfig{}, err
	}
	if value.HTTPSAddress, err = normalizeProxyAddress(value.HTTPSAddress); err != nil {
		return ProxyConfig{}, err
	}
	if value.Mode == "manual" && value.Address == "" && value.HTTPAddress == "" && value.HTTPSAddress == "" {
		return ProxyConfig{}, fmt.Errorf(i18n.T("请填写代理地址"))
	}
	if strings.ContainsAny(value.Username, ":\r\n\x00") || strings.ContainsAny(value.Password, "\r\n\x00") || len(value.Username) > 1024 || len(value.Password) > 4096 {
		return ProxyConfig{}, fmt.Errorf(i18n.T("代理用户名或密码格式无效"))
	}
	if value.Username == "" && value.Password != "" {
		return ProxyConfig{}, fmt.Errorf(i18n.T("请填写代理用户名"))
	}
	for _, proxyAddress := range []string{value.Address, value.HTTPAddress, value.HTTPSAddress} {
		if strings.HasPrefix(proxyAddress, "socks5") && (len(value.Username) > 255 || len(value.Password) > 255) {
			return ProxyConfig{}, fmt.Errorf(i18n.T("SOCKS5 用户名和密码各不能超过 255 字节"))
		}
		if value.IntegratedAuth && strings.HasPrefix(proxyAddress, "socks5") {
			return ProxyConfig{}, fmt.Errorf(i18n.T("代理集成认证不支持 SOCKS5"))
		}
	}
	value.Bypass, err = normalizeProxyBypass(value.Bypass)
	if err != nil {
		return ProxyConfig{}, err
	}
	return value, nil
}

// normalizeProxyAddress 校验并规范化一个代理地址；空地址返回空。
func normalizeProxyAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	address, err := url.Parse(raw)
	if err != nil || address.Opaque != "" || address.Hostname() == "" || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
		return "", fmt.Errorf(i18n.T("请输入有效代理地址，不包含路径、查询参数或片段"))
	}
	address.Scheme = strings.ToLower(address.Scheme)
	switch address.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf(i18n.T("代理仅支持 HTTP、HTTPS 或 SOCKS5"))
	}
	if address.User != nil {
		return "", fmt.Errorf(i18n.T("请在用户名和密码输入框填写代理凭据"))
	}
	host, err := proxyHost(address.Hostname())
	if err != nil {
		return "", fmt.Errorf(i18n.T("代理主机名或 IP 地址无效"))
	}
	port := address.Port()
	if port == "" {
		if strings.HasSuffix(address.Host, ":") {
			return "", fmt.Errorf(i18n.T("代理端口无效"))
		}
		port = "80"
		if address.Scheme == "https" {
			port = "443"
		} else if strings.HasPrefix(address.Scheme, "socks5") {
			port = "1080"
		}
	}
	if !validProxyPort(port) {
		return "", fmt.Errorf(i18n.T("代理端口应在 1 到 65535 之间"))
	}
	address.Host = net.JoinHostPort(host, port)
	address.Path, address.RawPath = "", ""
	return address.String(), nil
}

func validProxyPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strings.Trim(port, "0123456789") == ""
}

func proxyHost(host string) (string, error) {
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return address.String(), nil
	}
	host, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || len(host) == 0 || len(host) > 253 {
		return "", fmt.Errorf("invalid host")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
			return "", fmt.Errorf("invalid host")
		}
	}
	return strings.ToLower(host), nil
}

// Validate before using httpproxy, which otherwise silently ignores malformed
// NO_PROXY tokens. Domains include subdomains; a leading dot only matches subdomains.
func normalizeProxyBypass(text string) (string, error) {
	if len(text) > 8192 {
		return "", fmt.Errorf(i18n.T("代理排除地址过长"))
	}
	items := strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == ';' || r == '，' || r == '；' })
	if len(items) > 256 {
		return "", fmt.Errorf(i18n.T("代理排除地址最多 256 项"))
	}
	var out []string
	seen := make(map[string]bool)
	for _, item := range items {
		canonical := ""
		if item == "*" {
			canonical = item
		} else if prefix, err := netip.ParsePrefix(item); err == nil && prefix.Addr().Zone() == "" {
			canonical = prefix.Masked().String()
		} else {
			host, port := item, ""
			if value, number, err := net.SplitHostPort(item); err == nil {
				host, port = value, number
				if !validProxyPort(port) {
					return "", fmt.Errorf(i18n.T("代理排除地址的端口无效"))
				}
			} else if strings.HasPrefix(item, "[") && strings.HasSuffix(item, "]") {
				host = item[1 : len(item)-1]
			}
			prefix := ""
			if strings.HasPrefix(host, "*.") {
				host, prefix = host[2:], "."
			} else if strings.HasPrefix(host, ".") {
				host, prefix = host[1:], "."
			}
			name, err := proxyHost(host)
			_, ipError := netip.ParseAddr(name)
			if err != nil || (prefix != "" && ipError == nil) {
				return "", fmt.Errorf(i18n.T("代理排除地址无效，请使用域名、IP、CIDR 或带端口的主机"))
			}
			canonical = prefix + name
			if port != "" {
				canonical = net.JoinHostPort(canonical, port)
			}
		}
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	}
	return strings.Join(out, ","), nil
}

type proxyAuthenticationError struct{}

func (*proxyAuthenticationError) Error() string {
	return i18n.T("代理服务器要求认证（HTTP 407），请检查网络设置中的代理用户名和密码")
}

// Proxy configuration errors must not be mistaken for transient network errors.
type proxyConfigurationError struct{ reason string }

func (e *proxyConfigurationError) Error() string { return e.reason }

type proxyRequestError struct {
	err      error
	password string
}

func (e *proxyRequestError) Error() string {
	message := e.err.Error()
	if e.password != "" {
		for _, value := range []string{e.password, url.QueryEscape(e.password), url.PathEscape(e.password)} {
			message = strings.ReplaceAll(message, value, i18n.T("[已隐藏]"))
		}
	}
	return message
}
func (e *proxyRequestError) Unwrap() error { return e.err }

// Transports are immutable after publication. Replacing a configuration closes
// only idle connections; responses already streaming keep their existing route.
type proxyTransport struct {
	mu         sync.RWMutex
	base       http.RoundTripper
	current    http.RoundTripper
	config     ProxyConfig
	blocked    error
	note       string   // 路由补充说明，例如系统 PAC 未解析
	proxyHTTP  *url.URL // 供 HTTP 目标挑战应答定位代理主机
	proxyHTTPS *url.URL // 供 HTTPS 隧道建立代理连接
}

func (p *proxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.RLock()
	current, config, blocked, proxyHTTP := p.current, p.config, p.blocked, p.proxyHTTP
	p.mu.RUnlock()
	if blocked != nil {
		return nil, blocked
	}
	if req.Header.Get("Proxy-Authorization") != "" {
		req = req.Clone(req.Context())
		req.Header.Del("Proxy-Authorization")
	}
	// A 407 returned by a direct destination must not elicit a proxy token.
	// Evaluate the configured transport's actual route for this request.
	var selectedProxy *url.URL
	if config.IntegratedAuth && proxyHTTP != nil && req.URL != nil && req.URL.Scheme == "http" {
		if transport, ok := current.(*http.Transport); ok && transport.Proxy != nil {
			var routeErr error
			selectedProxy, routeErr = transport.Proxy(req)
			if routeErr != nil {
				return nil, &proxyRequestError{err: routeErr, password: config.Password}
			}
		}
	}
	resp, err := current.RoundTrip(req)
	if err != nil {
		return nil, &proxyRequestError{err: err, password: config.Password}
	}
	if resp != nil && resp.StatusCode == http.StatusProxyAuthRequired {
		if selectedProxy != nil && strings.EqualFold(selectedProxy.Scheme, proxyHTTP.Scheme) && strings.EqualFold(selectedProxy.Host, proxyHTTP.Host) {
			negotiated, authErr := integratedProxyRetry(current, req, resp, selectedProxy)
			if authErr == nil {
				return negotiated, nil
			}
			return nil, &proxyRequestError{err: authErr, password: config.Password}
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		return nil, &proxyAuthenticationError{}
	}
	return resp, nil
}

// integratedProxyRetry 用 SSPI 当前用户身份完成代理的 Negotiate/NTLM 挑战
// 应答；失败时保留原 407 语义。
func integratedProxyRetry(route http.RoundTripper, req *http.Request, resp *http.Response, proxyURL *url.URL) (*http.Response, error) {
	scheme, challengeToken, ok := proxyIntegratedScheme(resp.Header)
	var binding, proxyCertificate []byte
	if strings.EqualFold(proxyURL.Scheme, "https") {
		if resp.TLS == nil {
			resp.Body.Close()
			return nil, i18n.NewError("代理认证要求经过验证的 TLS 连接")
		}
		var err error
		binding, err = integratedChannelBinding(resp.TLS)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		proxyCertificate = append([]byte(nil), resp.TLS.PeerCertificates[0].Raw...)
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
	if !ok {
		return nil, i18n.NewError("代理未提供 Windows 集成认证")
	}
	base, ok := route.(*http.Transport)
	if !ok {
		return nil, i18n.NewError("当前网络传输不支持隔离的代理认证")
	}
	transport := base.Clone()
	// Pin this isolated exchange to the route that produced the challenge.
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.DisableKeepAlives = false
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if proxyCertificate != nil {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		previous := transport.TLSClientConfig.VerifyConnection
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if previous != nil {
				if err := previous(state); err != nil {
					return err
				}
			}
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, proxyCertificate) {
				return i18n.NewError("代理认证期间 TLS 证书发生变化或无效")
			}
			return nil
		}
	}
	defer func() { transport.CloseIdleConnections() }()
	session, err := proxyIntegratedSession(proxyURL.Hostname(), scheme, binding)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var serverToken []byte
	if challengeToken != "" {
		if serverToken, err = decodeProxyToken(challengeToken); err != nil {
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
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		next := req.Clone(req.Context())
		next.Header.Set("Proxy-Authorization", scheme+" "+base64.StdEncoding.EncodeToString(token))
		response, err := transport.RoundTrip(next)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusProxyAuthRequired {
			if response.StatusCode < 400 {
				if err := finishProxyAuthentication(session, scheme, complete, response.Header); err != nil {
					response.Body.Close()
					return nil, err
				}
			}
			response.Request = req
			response.Body = &ntlmBody{ReadCloser: response.Body, cleanup: transport.CloseIdleConnections}
			return response, nil
		}
		challengeToken, hasChallenge := integratedChallengeHeader(response.Header, "Proxy-Authenticate", scheme)
		closed := response.Close
		if err := drainIntegratedChallenge(response); err != nil {
			return nil, err
		}
		if closed {
			return nil, i18n.NewError("代理在认证期间关闭连接")
		}
		if complete || !hasChallenge || challengeToken == "" {
			return nil, i18n.NewError("代理拒绝系统集成认证")
		}
		var challengeData []byte
		if challengeToken != "" {
			if challengeData, err = decodeProxyToken(challengeToken); err != nil {
				return nil, err
			}
		}
		token, complete, err = session.Next(challengeData)
		if err != nil {
			return nil, err
		}
	}
	return nil, i18n.NewError("代理的 Windows 集成认证轮次过多")
}

func (p *proxyTransport) set(value ProxyConfig) error {
	value, err := NormalizeProxyConfig(value)
	if err != nil {
		return err
	}
	note := ""
	var proxyHTTP, proxyHTTPS *url.URL
	var next http.RoundTripper
	if base, ok := p.base.(*http.Transport); ok {
		transport := base.Clone()
		switch value.Mode {
		case "direct":
			transport.Proxy = nil
		case "manual", "system":
			// 按协议分别代理：HTTP/HTTPS 可覆盖共享地址，凭据作用于两者；
			// 系统模式的地址与绕过列表来自系统设置。
			withCredentials := func(raw string) string {
				if raw == "" || value.Username == "" {
					return raw
				}
				address, err := url.Parse(raw)
				if err != nil {
					return raw
				}
				address.User = url.UserPassword(value.Username, value.Password)
				return address.String()
			}
			httpProxy, httpsProxy := value.Address, value.Address
			bypass := value.Bypass
			pacURL := ""
			if value.Mode == "system" {
				httpProxy, httpsProxy, bypass, pacURL, note = resolveSystemProxy()
			} else {
				if value.HTTPAddress != "" {
					httpProxy = value.HTTPAddress
				}
				if value.HTTPSAddress != "" {
					httpsProxy = value.HTTPSAddress
				}
			}
			effectiveHTTP, effectiveHTTPS := httpProxy, httpsProxy
			if effectiveHTTP == "" {
				effectiveHTTP = httpsProxy
			}
			if effectiveHTTPS == "" {
				effectiveHTTPS = httpProxy
			}
			if effectiveHTTP != "" {
				proxyHTTP, _ = url.Parse(withCredentials(effectiveHTTP))
			}
			if effectiveHTTPS != "" {
				proxyHTTPS, _ = url.Parse(withCredentials(effectiveHTTPS))
			}
			if httpProxy == "" && httpsProxy == "" && pacURL != "" {
				// PAC 自动配置按目标地址逐个求值；求值不可用时按直连。
				evaluate := pacProxyFunc(pacURL)
				transport.Proxy = func(req *http.Request) (*url.URL, error) { return evaluate(req.URL) }
			} else if httpProxy == "" && httpsProxy == "" {
				// 系统没有可用代理声明：保持环境语义。
				transport.Proxy = nil
				proxyHTTP, proxyHTTPS = nil, nil
			} else {
				choose := (&httpproxy.Config{HTTPProxy: withCredentials(httpProxy), HTTPSProxy: withCredentials(httpsProxy), NoProxy: bypass}).ProxyFunc()
				if value.IntegratedAuth {
					// HTTPS 的 CONNECT 挑战应答需要 SSPI 交换，隧道自理；
					// HTTP 目标仍走请求层的 407 挑战应答。
					transport.Proxy = func(req *http.Request) (*url.URL, error) {
						if req.URL != nil && req.URL.Scheme == "https" {
							return nil, nil
						}
						return choose(req.URL)
					}
					transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						tunnelProxy, err := choose(&url.URL{Scheme: "https", Host: addr})
						if err != nil {
							return nil, err
						}
						if tunnelProxy == nil {
							dialer := &net.Dialer{}
							conn, err := dialer.DialContext(ctx, network, addr)
							if err != nil {
								return nil, err
							}
							return handshakeProxyTLS(ctx, conn, addr, transport.TLSClientConfig)
						}
						conn, err := dialProxyTunnel(ctx, tunnelProxy, addr, true, value.Username, value.Password)
						if err != nil {
							return nil, err
						}
						return handshakeProxyTLS(ctx, conn, addr, transport.TLSClientConfig)
					}
				} else {
					transport.Proxy = func(req *http.Request) (*url.URL, error) { return choose(req.URL) }
				}
			}
		}
		previousConnect := transport.OnProxyConnectResponse
		transport.OnProxyConnectResponse = func(ctx context.Context, proxyURL *url.URL, request *http.Request, response *http.Response) error {
			if response.StatusCode == http.StatusProxyAuthRequired {
				return &proxyAuthenticationError{}
			}
			if previousConnect != nil {
				return previousConnect(ctx, proxyURL, request, response)
			}
			return nil
		}
		next = transport
	} else if value.Mode == "environment" {
		next = p.base
	} else {
		return fmt.Errorf(i18n.T("当前自定义传输不支持修改代理"))
	}
	p.mu.Lock()
	if p.config == value && p.blocked == nil {
		p.mu.Unlock()
		return nil
	}
	previous := p.current
	p.current, p.config, p.blocked, p.note = next, value, nil, note
	p.proxyHTTP, p.proxyHTTPS = proxyHTTP, proxyHTTPS
	p.mu.Unlock()
	closeProxyIdle(previous)
	return nil
}

func closeProxyIdle(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (p *proxyTransport) CloseIdleConnections() {
	p.mu.RLock()
	current := p.current
	p.mu.RUnlock()
	closeProxyIdle(current)
}

// SetProxyConfig applies to subsequent requests, including retries, redirects
// and media segments. It never mutates an active transport.
func (m *Manager) SetProxyConfig(value ProxyConfig) error {
	if m.proxy == nil {
		return fmt.Errorf(i18n.T("下载代理尚未初始化"))
	}
	return m.proxy.set(value)
}

// UpdateTransport shares only the configured proxy route with update requests;
// it does not add task credentials, cookies, or site authentication.
func (m *Manager) UpdateTransport() http.RoundTripper {
	if m == nil || m.proxy == nil {
		return nil
	}
	return m.proxy
}

// DoResourceRequest lets page-link extraction share the configured route and
// connection limits, while the caller supplies its own deadline and body limit.
// The loopback API and native messaging do not use this transport.
func (m *Manager) DoResourceRequest(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return nil, fmt.Errorf(i18n.T("资源请求只支持 HTTP 或 HTTPS"))
	}
	client := *m.client
	client.Transport = windowsAuthTransport{base: client.Transport, manager: m}
	if _, configured := m.windowsAuthFor(request.URL); configured {
		request = stripIntegratedAuthorization(request)
	}
	return client.Do(request)
}

// BlockProxy prevents a corrupt/restored configuration from silently falling
// back to a direct connection. A valid SetProxyConfig clears the block.
func (m *Manager) BlockProxy(reason string) {
	if m.proxy == nil {
		return
	}
	m.proxy.mu.Lock()
	m.proxy.blocked = &proxyConfigurationError{reason: reason}
	m.proxy.mu.Unlock()
}

func nonRetryableProxyError(err error) bool {
	var auth *proxyAuthenticationError
	var config *proxyConfigurationError
	return errors.As(err, &auth) || errors.As(err, &config)
}
