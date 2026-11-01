// Package pathrec extracts download URLs, matches path and site rules, and
// derives filenames from response headers, URL paths and page titles.
package pathrec

import (
	"regexp"
	"strings"
)

// These permissive character classes accept spaces, Unicode and filename
// punctuation that can occur in download links embedded in page text.
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
	// Trim a prose label following spaces after matching; RE2 does not support
	// the negative lookahead needed to exclude it in the path expression.
	spaceColonRe = regexp.MustCompile(` +\w+:`)
	// Drop trailing space-separated prose while the remaining path ends in a
	// filename with an extension.
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
