package download

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"openDL/i18n"
	"openDL/winauth"

	"golang.org/x/net/idna"
)

const (
	AuthKerberosOnly = "kerberos"
	AuthAllowNTLM    = "negotiate"
)

type WindowsAuthSite struct {
	Origin string `json:"origin"`
	Policy string `json:"policy"`
	SPN    string `json:"spn,omitempty"`
}

// Exact origin matching includes scheme and effective port. It never expands
// trust to subdomains, redirect targets, or other services on the same host.
func windowsAuthOrigin(text string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(text))
	if u != nil {
		u.Scheme = strings.ToLower(u.Scheme)
	}
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", i18n.NewError("请输入仅包含协议、主机和可选端口的 HTTP(S) 站点地址")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" || strings.ContainsAny(host, "* \\/%\t\r\n") {
			return "", i18n.NewError("Windows 集成认证站点地址无效")
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", i18n.NewError("Windows 集成认证站点端口无效")
		}
		port = strconv.Itoa(n)
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

func NormalizeWindowsAuthSites(sites []WindowsAuthSite) ([]WindowsAuthSite, error) {
	if len(sites) > 256 {
		return nil, i18n.NewError("Windows 集成认证最多配置 256 个站点")
	}
	result := make([]WindowsAuthSite, 0, len(sites))
	seen := map[string]bool{}
	for _, site := range sites {
		origin, err := windowsAuthOrigin(site.Origin)
		if err != nil {
			return nil, err
		}
		if site.Policy != AuthKerberosOnly && site.Policy != AuthAllowNTLM {
			return nil, i18n.NewError("Windows 集成认证策略无效")
		}
		spn := strings.TrimSpace(site.SPN)
		if spn != "" && (!strings.HasPrefix(spn, "HTTP/") || len(spn) <= 5 || len(spn) > 512 || strings.ContainsAny(spn, " \t\r\n\x00?#@\\")) {
			return nil, i18n.NewError("服务 SPN 必须采用 HTTP/主机 格式")
		}
		if seen[origin] {
			return nil, i18n.NewError("Windows 集成认证站点重复")
		}
		seen[origin] = true
		result = append(result, WindowsAuthSite{Origin: origin, Policy: site.Policy, SPN: spn})
	}
	return result, nil
}

func (m *Manager) SetWindowsAuthSites(sites []WindowsAuthSite) error {
	normalized, err := NormalizeWindowsAuthSites(sites)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.windowsAuth = normalized
	m.windowsAuthError = nil
	m.mu.Unlock()
	return nil
}

// BlockWindowsAuth prevents a corrupt persisted policy from downgrading to
// another authentication mechanism. Saving valid settings clears this block.
func (m *Manager) BlockWindowsAuth(err error) {
	m.mu.Lock()
	m.windowsAuthError = err
	m.mu.Unlock()
}

func requestOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	copy := *u
	copy.User = nil
	copy.Path = ""
	copy.RawPath = ""
	copy.RawQuery = ""
	copy.Fragment = ""
	copy.ForceQuery = false
	origin, _ := windowsAuthOrigin(copy.String())
	return origin
}

func (m *Manager) windowsAuthForLocked(u *url.URL) (WindowsAuthSite, bool) {
	origin := requestOrigin(u)
	for _, site := range m.windowsAuth {
		if site.Origin == origin {
			return site, true
		}
	}
	return WindowsAuthSite{}, false
}

func (m *Manager) windowsAuthFor(u *url.URL) (WindowsAuthSite, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.windowsAuthForLocked(u)
}

func (m *Manager) canSystemAuthenticateLocked(t *Task) bool {
	if !winauth.Available {
		return false
	}
	u, err := url.Parse(t.sourceURL())
	if err != nil {
		return false
	}
	_, ok := m.windowsAuthForLocked(u)
	return ok
}

func (m *Manager) canSystemAuthenticate(t *Task) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.canSystemAuthenticateLocked(t)
}

func stripIntegratedAuthorization(req *http.Request) *http.Request {
	clean := req.Clone(req.Context())
	u := *req.URL
	u.User = nil
	clean.URL = &u
	clean.Header.Del("Authorization")
	clean.Header.Del("Proxy-Authorization")
	return clean
}

type windowsAuthTransport struct {
	base    http.RoundTripper
	manager *Manager
	task    *Task
}

func (t windowsAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.manager.mu.Lock()
	blocked := t.manager.windowsAuthError
	t.manager.mu.Unlock()
	if blocked != nil {
		return nil, blocked
	}
	site, ok := t.manager.windowsAuthFor(req.URL)
	if !ok {
		return t.base.RoundTrip(req)
	}
	return integratedRoundTrip(t.base, stripIntegratedAuthorization(req), site, t.task)
}

func (t *Task) recordAuthProtocol(protocol string) {
	if t == nil {
		return
	}
	t.metaMu.Lock()
	t.authProtocol = protocol
	t.metaMu.Unlock()
}
