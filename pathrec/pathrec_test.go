package pathrec

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The cases below are the "special paths" IDM recognizes and a strict
// RFC 3986 tokenizer (what a plain downloader such as fluxdown uses) drops.
func TestExtractURLsSpecialPaths(t *testing.T) {
	text := strings.Join([]string{
		`下载地址: https://mirror.example.cn/下载/年度报告.zip 提取码 8888`,
		`Alt: https://files.example.com/Q3 Report;v=2.pdf?x=1`,
		`Pad: https://x.example.org/dl/My%20Report%20(2024).tar.gz#mirror1`,
		`Home: https://example.net/~user/soft;v=1/setup_x64.exe`,
		`Plain: https://example.com/simple.zip`,
	}, "\n")
	want := []string{
		`https://mirror.example.cn/下载/年度报告.zip`,
		`https://files.example.com/Q3 Report;v=2.pdf?x=1`,
		`https://x.example.org/dl/My%20Report%20(2024).tar.gz#mirror1`,
		`https://example.net/~user/soft;v=1/setup_x64.exe`,
		`https://example.com/simple.zip`,
	}
	got := ExtractURLs(text)
	if len(got) != len(want) {
		t.Fatalf("got %d urls %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractURLsStopsBeforeProse(t *testing.T) {
	// IDM's lookahead stops a path at "word:" so prose does not become URL.
	got := ExtractURLs(`See https://example.com/a/b file: it works`)
	if len(got) != 1 || got[0] != "https://example.com/a/b" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractURLsDedupes(t *testing.T) {
	got := ExtractURLs("https://a.example/x.zip https://a.example/x.zip")
	if len(got) != 1 {
		t.Fatalf("got %q", got)
	}
}

func TestMasksSpecialPaths(t *testing.T) {
	ml := NewMasks(
		`https://web.whatsapp.com/res/*.mp3`,
		`http://ad.example.com/st?*`,
	)
	cases := map[string]bool{
		`https://web.whatsapp.com/res/audio/msg.mp3`: true,
		`http://ad.example.com/st?ad_type=3`:         true,
		`https://web.whatsapp.com/css/site.css`:      false,
	}
	for url, want := range cases {
		if got := ml.Match(url); got != want {
			t.Errorf("Match(%q) = %v, want %v", url, got, want)
		}
	}
	if m := ml.MatchBy(`http://ad.example.com/st?x=1`); m == "" {
		t.Error("MatchBy should return the matching mask")
	}
}

func TestDefaultMasksLoaded(t *testing.T) {
	ml := DefaultMasks()
	if len(ml.Patterns()) < 80 {
		t.Fatalf("masks.txt not loaded: %d patterns", len(ml.Patterns()))
	}
	if !ml.Match(`https://web.whatsapp.com/res/a.mp3`) {
		t.Error("whatsapp res mask should match")
	}
	if !ml.Match(`http://*.download.windowsupdate.com/a/b`) {
		t.Error("windowsupdate mask should match")
	}
}

func TestSiteRuleHostRelativePaths(t *testing.T) {
	rules := []SiteRule{
		{Tag: "watch", Host: `^www\.youtube\.com$`, Path: `^/watch\?v=`},
	}
	if _, ok := MatchRule(rules, `https://www.youtube.com/watch?v=abc123`); !ok {
		t.Error("host-relative path rule should match full URL")
	}
	if _, ok := MatchRule(rules, `https://other.example.com/watch?v=abc123`); ok {
		t.Error("rule must not match another host")
	}
}

func TestExpandPathPattern(t *testing.T) {
	got := ExpandPathPattern(`^/watch\?v=`, `^www\.example\.com$`)
	want := `^(?:https?://www\.example\.com)?/watch\?v=`
	if got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}
}

func TestSanitizeLongUnicodeFilename(t *testing.T) {
	got := SanitizeFilename(strings.Repeat("下载", 40) + ".zip")
	if len(got) > 200 || !utf8.ValidString(got) {
		t.Fatalf("invalid long filename: %q", got)
	}
}

func TestSanitizeWindowsDeviceNamesAcrossExtensions(t *testing.T) {
	for input, want := range map[string]string{
		"NUL.tar.gz": "_NUL.tar.gz", "con .txt": "_con .txt",
		"COM¹.log": "_COM¹.log", "lpt².txt": "_lpt².txt",
		"CONSOLE.txt": "CONSOLE.txt", ".con": ".con",
	} {
		if got := SanitizeFilename(input); got != want {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
	name := SanitizeFilename("CON" + strings.Repeat(" ", 300) + "other")
	if reservedWindowsName(name) || len(name) > 200 {
		t.Fatalf("truncated reserved name: %q", name)
	}
}

func TestDeriveFilenameFromContentDisposition(t *testing.T) {
	cases := []struct{ cd, want string }{
		{`attachment; filename="report 2024.pdf"`, `report 2024.pdf`},
		{`attachment; filename*=utf-8''%E5%B9%B4%E6%8A%A5.pdf`, `年报.pdf`},
		{`attachment; filename="=?UTF-8?B?5Lit5paH?="`, `中文`},
		{`attachment; filename="=?UTF-8?Q?caf=C3=A9?=.txt"`, `café.txt`},
		{`attachment; filename = "report; 2024.pdf"`, `report; 2024.pdf`},
		{`inline; filename*=windows-1252''caf%E9.pdf`, `café.pdf`},
		{`attachment; filename*=GBK''%D6%D0%CE%C4.txt`, `中文.txt`},
		{`attachment; filename="=?GBK?B?1tDOxA==?=.txt"`, `中文.txt`},
		{`attachment; filename*=Shift_JIS''%93%FA%96%7B.txt`, `日本.txt`},
		{`attachment; filename*=windows-1251''%D4%E0%E9%EB.txt`, `Файл.txt`},
		{`attachment; filename="CON.txt"`, `_CON.txt`},
	}
	for _, c := range cases {
		if got := DeriveFilename(Source{URL: "https://x.example/dl", ContentDisposition: c.cd}); got != c.want {
			t.Errorf("DeriveFilename(%q) = %q, want %q", c.cd, got, c.want)
		}
	}
}

func TestDeriveFilenameFromSpecialPaths(t *testing.T) {
	cases := []struct{ url, want string }{
		{`https://mirror.example.cn/下载/年度报告.zip`, `年度报告.zip`},
		{`https://files.example.com/Q3 Report;v=2.pdf`, `Q3 Report;v=2.pdf`},
		{`https://x.example.org/dl/My%20Report%20(2024).tar.gz`, `My Report (2024).tar.gz`},
		{`https://example.net/~user/soft/setup_x64.exe`, `setup_x64.exe`},
		{`https://example.com/dl/setup.exe#mirror1`, `setup.exe`},
		{`https://shop.example.com/order/download.php?id=88&jsessionid=ABC`, `download.php`},
		{`https://example.com/a/b/CON`, `_CON`},
		{`https://example.com/files/report%2520final.pdf`, `report%20final.pdf`},
		{`https://example.com/files/part%2Fname.pdf`, `part_name.pdf`},
		{`https://example.com/files/caf%E9.txt`, `café.txt`},
	}
	for _, c := range cases {
		got := DeriveFilename(Source{URL: c.url})
		if got != c.want {
			t.Errorf("DeriveFilename(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestContentDispositionEscapedQuotesDoNotEndParameter(t *testing.T) {
	cd := `attachment; filename="report \"draft; revised\".pdf"; size=123`
	if got := FromContentDisposition(cd); got != `report "draft; revised".pdf` {
		t.Fatalf("unexpected filename: %q", got)
	}
}

func TestDeriveFilenameScriptFallsBackToTitle(t *testing.T) {
	got := DeriveFilename(Source{
		URL:       `https://shop.example.com/order/download.php?id=88`,
		Title:     `电子发票 2024 - shop.example.com`,
		SiteLabel: `shop.example.com`,
	})
	if got != `电子发票 2024` {
		t.Errorf("got %q", got)
	}
}

func TestDeriveFilenameGeneratedFromMIME(t *testing.T) {
	got := DeriveFilename(Source{URL: "https://x.example/dl?id=1", ContentType: "video/mp4"})
	if got != "dl.mp4" {
		t.Errorf("got %q", got)
	}
	got = DeriveFilename(Source{URL: "https://x.example/dl?id=1"})
	if got != "dl" {
		t.Errorf("got %q", got)
	}
}

func TestCommonMIMETypesAndLongFilenames(t *testing.T) {
	for contentType, suffix := range map[string]string{
		"video/webm": ".webm", "audio/flac": ".flac", "application/vnd.rar": ".rar",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
		"application/vnd.apple.mpegurl":                                           ".m3u8", "application/dash+xml": ".mpd",
	} {
		name := DeriveFilename(Source{URL: "https://example.test/file", ContentType: contentType + "; charset=utf-8"})
		if name != "file"+suffix {
			t.Errorf("%s produced %q", contentType, name)
		}
	}
	for _, source := range []Source{
		{URL: "https://example.test/" + strings.Repeat("中", 70) + ".zip"},
		{URL: "https://example.test/" + strings.Repeat("中", 70), ContentType: "application/zip"},
	} {
		name := DeriveFilename(source)
		if len(name) > 200 || !utf8.ValidString(name) || !strings.HasSuffix(name, ".zip") {
			t.Fatalf("long filename lost its extension or exceeded the limit: %q", name)
		}
	}
	if name := DeriveFilename(Source{URL: "https://example.test/file", ContentType: "application/octet-stream"}); name != "file" {
		t.Fatalf("ambiguous MIME type acquired an extension: %q", name)
	}
}

func TestRecognizeTags(t *testing.T) {
	opt := Options{
		Masks: NewMasks(`https://web.whatsapp.com/res/*.mp3`),
		Rules: []SiteRule{{Tag: "watch", Host: `^www\.example\.com$`, Path: `^/watch\?v=`}},
	}
	text := strings.Join([]string{
		`https://web.whatsapp.com/res/a.mp3`,
		`https://www.example.com/watch?v=1`,
		`https://cdn.example.net/f.zip`,
	}, "\n")
	got := Recognize(text, opt)
	if len(got) != 3 {
		t.Fatalf("got %d candidates", len(got))
	}
	if !strings.HasPrefix(got[0].Tag, "特殊路径") {
		t.Errorf("tag[0] = %q", got[0].Tag)
	}
	if !strings.HasPrefix(got[1].Tag, "站点规则") {
		t.Errorf("tag[1] = %q", got[1].Tag)
	}
	if got[2].Tag != "扩展名: .zip" {
		t.Errorf("tag[2] = %q", got[2].Tag)
	}
}
