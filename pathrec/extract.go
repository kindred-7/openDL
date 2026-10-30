// Package pathrec reconstructs Internet Download Manager's download-path
// recognition: extracting "special" URLs from text that ordinary parsers miss,
// matching IDM's URL-mask and per-site path rules, and deriving filenames the
// way IDMan.exe does (Content-Disposition RFC 2047/5987, percent-decoding,
// Windows name sanitizing).
//
// Evidence: extracted from idman643build16.exe (IDM 6.43.16.1 installer),
// content scripts inside its browser extension (URL regex and site-rule
// expansion), the URL-mask table shipped as installer entry 044, and the
// header-parsing string table of IDMan.exe (0x30b700..0x30b980).
package pathrec

import (
	"regexp"
	"strings"
)

// Character classes ported from IDM's content script (its %H / %T / %Q
// substitutions). They deliberately accept spaces, unicode, semicolons and
// other "illegal" URL characters: that is why IDM recognizes paths a strict
// RFC 3986 tokenizer drops.
const (
	charsHostDot = `\w\.\x{20}-\x{FFFF}\-`
	charsHost    = `\w\x{20}-\x{FFFF}\-`
	charsPath    = `\w\-\.\+\*\(\)\$!,%\x{20}-\x{FFFF}`
	charsQuery   = `\s\[\]{}()`
)

var (
	urlRe = regexp.MustCompile(
		`\b\w+://(?:[` + charsPath + `]*(?::[` + charsPath + `]*)?@)?[` +
			charsHostDot + `]+\.[` + charsHost + `]+(?::\d+)?` +
			`(?:/(?:(?: +)?[` + charsPath + `/~;])*)?(?:\?[^` + charsQuery + `]*)?(?:#[` + charsPath + `]*)?`)
	// The original uses the lookahead ( +(?![\w-]+:)) while expanding path
	// characters; RE2 has none, so a trailing "word:" after spaces is trimmed
	// after matching.
	spaceColonRe = regexp.MustCompile(` +\w+:`)
	// The permissive path class also swallows prose after a space (IDM relies
	// on its native parser to reject it later). A trailing space-separated
	// token is dropped while what stays ends in a file name with an extension.
	proseTailRe = regexp.MustCompile(` [^\s/]+$`)
	extNameRe   = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)
)

func trimProseTail(m string) string {
	for {
		loc := proseTailRe.FindStringIndex(m)
		if loc == nil {
			return m
		}
		before := m[:loc[0]]
		if !lastSegmentHasExt(before) {
			return m
		}
		m = before
	}
}

func lastSegmentHasExt(u string) bool {
	cut := u
	for i, r := range u {
		if r == '?' || r == '#' {
			cut = u[:i]
			break
		}
	}
	seg := cut
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	if i := strings.IndexByte(seg, ' '); i >= 0 {
		seg = seg[:i]
	}
	return extNameRe.MatchString(seg)
}

// ExtractURLs returns every URL found in text in order of appearance,
// deduplicated. Unlike net/url based scanners it keeps URLs whose path
// contains spaces, semicolons (path parameters), tildes or non-ASCII text.
func ExtractURLs(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range urlRe.FindAllString(text, -1) {
		if loc := spaceColonRe.FindStringIndex(m); loc != nil {
			m = m[:loc[0]]
		}
		m = trimProseTail(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
