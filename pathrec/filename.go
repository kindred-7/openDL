package pathrec

import (
	"io"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

// Source carries everything 参考下载器 considers when naming a download.
type Source struct {
	URL                string
	ContentDisposition string
	ContentType        string
	Title              string
	SiteLabel          string // second-level domain label, used to clean titles
}

// DeriveFilename derives a Windows-safe filename like 原始程序 does:
// Content-Disposition first (RFC 5987 filename*, then RFC 2047 encoded
// words), then the last URL path segment (percent-decoded, charset-aware),
// then the cleaned page title, and finally a generated name from the MIME
// type.
func DeriveFilename(s Source) string {
	ext := extFromMIME(s.ContentType)
	finish := func(name string) string {
		name = sanitizeWindows(name)
		// 参考下载器 appends the extension from the response type when the name
		// derived from the path or title has none.
		if ext != "" && !strings.Contains(name, ".") {
			name = sanitizeWindows(name + ext)
		}
		return name
	}
	if name := FromContentDisposition(s.ContentDisposition); name != "" {
		return finish(name)
	}
	if name := FromURLPath(s.URL); name != "" && !scriptLike(name) {
		return finish(name)
	}
	if name := CleanTitle(s.Title, s.SiteLabel); name != "" && !scriptLike(name) {
		return finish(name)
	}
	if name := FromURLPath(s.URL); name != "" {
		return finish(name)
	}
	if name := CleanTitle(s.Title, s.SiteLabel); name != "" {
		return finish(name)
	}
	return sanitizeWindows("download" + ext)
}

// SanitizeFilename confines an externally supplied name to one safe filename.
func SanitizeFilename(name string) string { return sanitizeWindows(name) }

// FromContentDisposition extracts the filename parameter of a
// Content-Disposition header, decoding RFC 5987 filename* values and RFC 2047
// encoded words. These are the "=?UTF-8?B?..?=" and "utf-8”..." forms the
// 原始程序 header parser carries in its string table.
func FromContentDisposition(cd string) string {
	cd = strings.TrimSpace(cd)
	if cd == "" {
		return ""
	}
	if v := firstParam(cd, "filename*"); v != "" {
		if name := decodeRFC5987(v); name != "" {
			return name
		}
	}
	if v := firstParam(cd, "filename"); v != "" {
		return decodeEncodedWords(unquote(v))
	}
	return ""
}

// firstParam returns the value of a ;-separated header parameter such as
// filename or filename*, skipping quoted semicolons.
func firstParam(cd, key string) string {
	key = strings.ToLower(key)
	for _, part := range splitParams(cd) {
		name, value, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	escaped := false
	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && inQuote:
			cur.WriteRune(r)
			escaped = true
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ';' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			b.WriteByte(v[i])
		}
		v = b.String()
	}
	return v
}

// decodeRFC5987 decodes charset'language'percent-encoded values, the
// filename* form (RFC 5987) 参考下载器 supports charsets for: utf-8, windows-1252
// and ISO-8859-1 appear in its parser's string table.
func decodeRFC5987(v string) string {
	v = unquote(v)
	i := strings.IndexByte(v, '\'')
	if i < 0 {
		return ""
	}
	rest := v[i+1:]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	charset := strings.ToLower(v[:i])
	value := rest[j+1:]
	return charsetToString(charset, percentDecodeBytes(value))
}

// decodeEncodedWords decodes =?charset?B|Q?text?= runs anywhere in the value.
func decodeEncodedWords(v string) string {
	decoder := mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		raw, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(charsetToString(strings.ToLower(charset), raw)), nil
	}}
	decoded, err := decoder.DecodeHeader(v)
	if err != nil {
		return v
	}
	return decoded
}

// FromURLPath takes the last path segment of a URL and percent-decodes it the
// way 原始程序 does: bytes are read as UTF-8 when valid, otherwise as
// windows-1252 / ISO-8859-1.
func FromURLPath(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	// EscapedPath preserves encoded slashes and percent signs until the final
	// component has been selected. Decoding u.Path again corrupts both.
	seg := path.Base(u.EscapedPath())
	if seg == "." || seg == "/" || seg == "" {
		return ""
	}
	seg = charsetToString("utf-8", percentDecodeBytes(seg))
	return stripSessionParams(seg)
}

// stripSessionParams drops ";jsessionid=.." style suffixes, keeping other
// semicolons: Windows filenames may contain them and 参考下载器 keeps them.
func stripSessionParams(seg string) string {
	for _, key := range []string{";jsessionid=", ";sid=", ";phpsessid="} {
		if i := strings.Index(strings.ToLower(seg), key); i > 0 {
			seg = seg[:i]
		}
	}
	return seg
}

var counterPrefixRe = regexp.MustCompile(`^\(\d+\)\s+`)

// CleanTitle applies the content script's title cleanup: it strips a leading
// unread counter and a trailing " - Site Name" / " | Site" / " @ Site" label.
func CleanTitle(title, siteLabel string) string {
	t := counterPrefixRe.ReplaceAllString(strings.TrimSpace(title), "")
	t = strings.ReplaceAll(t, "\u25B6", " ")
	t = collapseSpaces(t)
	if siteLabel != "" {
		re := regexp.MustCompile(`(?i)^(.+?)\s+[-|@]+\s*` + regexp.QuoteMeta(siteLabel) + `$`)
		if m := re.FindStringSubmatch(t); m != nil {
			t = m[1]
		}
	}
	return strings.TrimSpace(t)
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

var scriptExt = map[string]bool{
	".php": true, ".asp": true, ".aspx": true, ".jsp": true,
	".cgi": true, ".do": true, ".action": true,
}

func scriptLike(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return false
	}
	return scriptExt[strings.ToLower(name[dot:])]
}

func percentDecodeBytes(s string) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

func hexVal(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// charsetToString decodes explicitly declared legacy charsets. Undeclared or
// unknown encodings keep the existing UTF-8/Windows-1252 fallback; guessing a
// regional encoding for an arbitrary URL could silently change its name.
func charsetToString(charset string, raw []byte) string {
	switch charset {
	case "", "utf-8", "utf8":
		if utf8.Valid(raw) {
			return string(raw)
		}
		return windows1252ToString(raw)
	case "iso-8859-1", "iso8859-1", "latin1", "windows-1252", "windows1252", "cp1252":
		return windows1252ToString(raw)
	}
	if encoding, err := htmlindex.Get(charset); err == nil && encoding != nil {
		if decoded, err := encoding.NewDecoder().Bytes(raw); err == nil && !strings.ContainsRune(string(decoded), '\uFFFD') {
			return string(decoded)
		}
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	return windows1252ToString(raw)
}

// windows1252ToString decodes CP1252 bytes, including the 0x80-0x9F slots
// that differ from ISO-8859-1.
func windows1252ToString(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c >= 0xA0:
			sb.WriteRune(rune(c))
		case c == 0x80:
			sb.WriteRune('\u20AC')
		case c == 0x82:
			sb.WriteRune('\u201A')
		case c == 0x83:
			sb.WriteRune('\u0192')
		case c == 0x84:
			sb.WriteRune('\u201E')
		case c == 0x85:
			sb.WriteRune('\u2026')
		case c == 0x86:
			sb.WriteRune('\u2020')
		case c == 0x87:
			sb.WriteRune('\u2021')
		case c == 0x88:
			sb.WriteRune('\u02C6')
		case c == 0x89:
			sb.WriteRune('\u2030')
		case c == 0x8A:
			sb.WriteRune('\u0160')
		case c == 0x8B:
			sb.WriteRune('\u2039')
		case c == 0x8C:
			sb.WriteRune('\u0152')
		case c == 0x8E:
			sb.WriteRune('\u017D')
		case c == 0x91:
			sb.WriteRune('\u2018')
		case c == 0x92:
			sb.WriteRune('\u2019')
		case c == 0x93:
			sb.WriteRune('\u201C')
		case c == 0x94:
			sb.WriteRune('\u201D')
		case c == 0x95:
			sb.WriteRune('\u2022')
		case c == 0x96:
			sb.WriteRune('\u2013')
		case c == 0x97:
			sb.WriteRune('\u2014')
		case c == 0x98:
			sb.WriteRune('\u02DC')
		case c == 0x99:
			sb.WriteRune('\u2122')
		case c == 0x9A:
			sb.WriteRune('\u0161')
		case c == 0x9B:
			sb.WriteRune('\u203A')
		case c == 0x9C:
			sb.WriteRune('\u0153')
		case c == 0x9E:
			sb.WriteRune('\u017E')
		case c == 0x9F:
			sb.WriteRune('\u0178')
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

var invalidWinChars = map[rune]bool{
	'<': true, '>': true, ':': true, '"': true,
	'/': true, '\\': true, '|': true, '?': true, '*': true,
}

var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true,
	"com9": true, "lpt1": true, "lpt2": true, "lpt3": true,
	"lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true,
	"lpt8": true, "lpt9": true,
	"com¹": true, "com²": true, "com³": true,
	"lpt¹": true, "lpt²": true, "lpt³": true,
}

func reservedWindowsName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	return reservedNames[strings.ToLower(strings.TrimRight(base, " "))]
}

// sanitizeWindows replaces characters Windows rejects in file names, trims
// trailing dots and spaces, guards reserved device names and bounds the
// length. Unlike an ASCII-only sanitizer it keeps CJK and other unicode text.
func sanitizeWindows(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || invalidWinChars[r]:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimRight(b.String(), " .")
	if name == "" {
		return "download"
	}
	if reservedWindowsName(name) {
		name = "_" + name
	}
	ext := ""
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		ext = name[dot:]
	}
	if len(name) > 200 {
		// Preserve a reasonably sized extension so the resulting file remains
		// recognizable, while keeping the entire name within the byte budget.
		suffix := ""
		if len(ext) > 0 && len(ext) <= 32 {
			suffix = ext
		}
		budget := 200 - len(suffix)
		end := 0
		prefix := strings.TrimSuffix(name, suffix)
		for i, r := range prefix {
			if i+utf8.RuneLen(r) > budget {
				break
			}
			end = i + utf8.RuneLen(r)
		}
		name = strings.TrimRight(prefix[:end], " .") + suffix
	}
	// Truncation and trailing-space removal can expose a reserved name.
	if reservedWindowsName(name) {
		name = "_" + name
	}
	return name
}

var mimeExt = map[string]string{
	"application/zip":                         ".zip",
	"application/x-7z-compressed":             ".7z",
	"application/vnd.rar":                     ".rar",
	"application/x-rar-compressed":            ".rar",
	"application/gzip":                        ".gz",
	"application/x-tar":                       ".tar",
	"application/pdf":                         ".pdf",
	"application/x-msdownload":                ".exe",
	"application/vnd.android.package-archive": ".apk",
	"application/msword":                      ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.ms-powerpoint":                                             ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.apple.mpegurl":                                             ".m3u8",
	"application/mpegurl":                                                       ".m3u8",
	"application/x-mpegurl":                                                     ".m3u8",
	"audio/mpegurl":                                                             ".m3u8",
	"audio/x-mpegurl":                                                           ".m3u8",
	"application/dash+xml":                                                      ".mpd",
	"video/mp4":                                                                 ".mp4",
	"video/webm":                                                                ".webm",
	"video/quicktime":                                                           ".mov",
	"video/x-matroska":                                                          ".mkv",
	"video/x-msvideo":                                                           ".avi",
	"video/x-ms-wmv":                                                            ".wmv",
	"audio/mpeg":                                                                ".mp3",
	"audio/mp4":                                                                 ".m4a",
	"audio/aac":                                                                 ".aac",
	"audio/wav":                                                                 ".wav",
	"audio/x-wav":                                                               ".wav",
	"audio/ogg":                                                                 ".ogg",
	"audio/opus":                                                                ".opus",
	"audio/flac":                                                                ".flac",
	"audio/webm":                                                                ".webm",
	"image/jpeg":                                                                ".jpg",
	"image/png":                                                                 ".png",
	"image/gif":                                                                 ".gif",
	"image/webp":                                                                ".webp",
	"image/avif":                                                                ".avif",
	"text/html":                                                                 ".html",
	"text/plain":                                                                ".txt",
}

func extFromMIME(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return mimeExt[strings.ToLower(ct)]
}
