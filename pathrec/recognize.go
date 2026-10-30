package pathrec

import "strings"

// Candidate is one recognized download path.
type Candidate struct {
	URL  string // the URL as it appears in the text
	Name string // derived Windows file name
	Tag  string // why it was recognized: mask, site rule, extension, plain
}

// Options configures recognition.
type Options struct {
	Masks     *MaskList  // special-path mask table; nil disables masking
	Rules     []SiteRule // per-site path rules
	SiteLabel string     // domain label for page-title cleanup
	Title     string     // page title fallback for naming
}

// Recognize extracts download paths from text and derives their filenames.
// It is the reconstruction of the pipeline IDM runs: extract every URL the
// permissive tokenizer sees, mark the ones hitting the special-path mask
// table, tag rule-matched paths, then derive names.
func Recognize(text string, opt Options) []Candidate {
	var out []Candidate
	for _, u := range ExtractURLs(text) {
		c := Candidate{URL: u, Tag: "普通路径"}
		switch {
		case opt.Masks != nil && opt.Masks.Match(u):
			c.Tag = "特殊路径: " + opt.Masks.MatchBy(u)
		default:
			if r, ok := MatchRule(opt.Rules, u); ok {
				c.Tag = "站点规则: " + r.Tag
			} else if ext := extOf(u); ext != "" {
				c.Tag = "扩展名: " + ext
			}
		}
		c.Name = DeriveFilename(Source{
			URL:       u,
			Title:     opt.Title,
			SiteLabel: opt.SiteLabel,
		})
		out = append(out, c)
	}
	return out
}

func extOf(rawURL string) string {
	name := FromURLPath(rawURL)
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		return strings.ToLower(name[dot:])
	}
	return ""
}
