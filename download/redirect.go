package download

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"openDL/i18n"
)

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && originPort(a) == originPort(b)
}

// Strip custom and credential-bearing headers on cross-origin redirects,
// including HTTPS-to-HTTP downgrades. Range headers are representation
// controls and must survive common redirects to download servers.
func checkDownloadRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf(i18n.T("重定向次数过多"))
	}
	if len(via) == 0 {
		return nil
	}
	if !sameOrigin(via[0].URL, req.URL) {
		if req.URL.User != nil {
			return i18n.NewError("跨站重定向地址不能包含登录凭据")
		}
		for key := range req.Header {
			switch http.CanonicalHeaderKey(key) {
			case "Accept", "Accept-Encoding", "Accept-Language", "User-Agent", "Range", "If-Range":
			default:
				delete(req.Header, key)
			}
		}
	}
	if referer := sourceReferer(via[len(via)-1].URL.String(), req.URL); referer != "" {
		req.Header.Set("Referer", referer)
	} else {
		req.Header.Del("Referer")
	}
	return nil
}
