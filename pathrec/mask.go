package pathrec

import (
	_ "embed"
	"regexp"
	"strings"
	"sync"
)

//go:embed masks.txt
var masksRaw string

// MaskList recognizes "special paths": URL masks 参考下载器 handles outside the
// normal interception flow (background resources, update files, ads and
// other paths that must not become downloads). Patterns use "*" wildcards,
// matching the table shipped with the 参考下载器 installer.
type MaskList struct {
	patterns []string
	re       []*regexp.Regexp
	once     sync.Once
}

// DefaultMasks returns the mask table extracted from the 参考下载器 6.43 installer.
func DefaultMasks() *MaskList {
	return &MaskList{patterns: parseLines(masksRaw)}
}

// NewMasks builds a mask table from explicit patterns.
func NewMasks(patterns ...string) *MaskList {
	return &MaskList{patterns: parseLines(strings.Join(patterns, "\n"))}
}

func parseLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Patterns returns the table's patterns.
func (ml *MaskList) Patterns() []string { return ml.patterns }

// Match reports whether url hits any mask: a special path.
func (ml *MaskList) Match(url string) bool {
	ml.once.Do(func() {
		for _, p := range ml.patterns {
			ml.re = append(ml.re, maskToRegexp(p))
		}
	})
	for _, re := range ml.re {
		if re.MatchString(url) {
			return true
		}
	}
	return false
}

// MatchBy returns the first mask that matches, or "".
func (ml *MaskList) MatchBy(url string) string {
	ml.once.Do(func() {
		for _, p := range ml.patterns {
			ml.re = append(ml.re, maskToRegexp(p))
		}
	})
	for i, re := range ml.re {
		if re.MatchString(url) {
			return ml.patterns[i]
		}
	}
	return ""
}

// maskToRegexp turns a mask into an anchored case-insensitive regexp where
// "*" matches any run of characters and every other byte is literal. "?" is
// literal on purpose: masks like "http://ad.example.com/st?*" use it as the
// query separator.
func maskToRegexp(mask string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, part := range strings.Split(mask, "*") {
		b.WriteString(regexp.QuoteMeta(part))
		b.WriteString(".*")
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
