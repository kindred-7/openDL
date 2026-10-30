package pathrec

import (
	"net/url"
	"regexp"
	"strings"
)

// SiteRule is one entry of IDM's per-site rule table: the rules the native
// side sends to the browser content script. A rule names a host pattern, a
// path pattern (regular expression) and an optional extension filter. This is
// how IDM recognizes download paths that carry no file extension at all.
type SiteRule struct {
	Tag      string   `json:"tag"`                // human label, e.g. "youtube-watch"
	Host     string   `json:"host"`               // host regexp, optional anchors ^...$ allowed
	Path     string   `json:"path"`               // path regexp; "^/" marks a host-relative pattern
	Exts     []string `json:"exts,omitempty"`     // extension filter such as ".mp4"; empty matches any
	SiteKind int      `json:"siteKind,omitempty"` // rule family from the content script (1..5)
}

// ExpandPathPattern ports the content script's rule expansion: a pattern
// containing "^/" matches either the bare path or the full URL, so the
// pattern becomes "^(?:<host-or-origin>)?/...".
func ExpandPathPattern(pattern, hostPattern string) string {
	if !strings.Contains(pattern, "^/") {
		return pattern
	}
	prefix := strings.TrimSpace(hostPattern)
	if prefix != "" {
		prefix = strings.TrimPrefix(prefix, "^")
		prefix = strings.TrimSuffix(prefix, "$")
		prefix = "https?://" + prefix
	} else {
		prefix = `https?://[^/]+`
	}
	return strings.ReplaceAll(pattern, "^/", "^(?:"+prefix+")?/")
}

// Regexp compiles the rule's path pattern with the host-relative expansion.
func (r SiteRule) Regexp() *regexp.Regexp {
	return regexp.MustCompile(ExpandPathPattern(r.Path, r.Host))
}

// Match reports whether a URL satisfies the rule's path pattern, host
// pattern and extension filter.
func (r SiteRule) Match(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if r.Host != "" {
		hostRe := regexp.MustCompile(`(?i)` + r.Host)
		if !hostRe.MatchString(strings.ToLower(u.Hostname())) {
			return false
		}
	}
	if len(r.Exts) > 0 {
		ext := ""
		if dot := strings.LastIndexByte(u.Path, '.'); dot >= 0 {
			ext = strings.ToLower(u.Path[dot:])
		}
		ok := false
		for _, e := range r.Exts {
			if strings.ToLower(e) == ext {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return r.Regexp().MatchString(rawURL)
}

// MatchRule returns the first rule that matches.
func MatchRule(rules []SiteRule, rawURL string) (SiteRule, bool) {
	for _, r := range rules {
		if r.Match(rawURL) {
			return r, true
		}
	}
	return SiteRule{}, false
}
