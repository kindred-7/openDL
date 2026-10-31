//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"openDL/resources"
	"openDL/style"
)

func TestExtractEngineRejectsDamagedPayload(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "setup.exe"), filepath.Join(dir, "engine.exe")
	payload := []byte("test engine")
	var trailer [8]byte
	binary.LittleEndian.PutUint64(trailer[:], uint64(len(payload)))
	data := append([]byte("frontend"), payload...)
	data = append(data, trailer[:]...)
	digest := sha256.Sum256(payload)
	data = append(data, digest[:]...)
	data = append(data, trailerMagic...)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractEngineFrom(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("extracted %q, %v", got, err)
	}
	corruptedPayload := append([]byte(nil), data...)
	corruptedPayload[len("frontend")] ^= 1
	for _, damaged := range [][]byte{data[:4], data[:len(data)-1], append(append([]byte(nil), data[:len(data)-8]...), []byte("badmagic")...), corruptedPayload} {
		if err := os.WriteFile(source, damaged, 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractEngineFrom(source, filepath.Join(dir, "unused.exe")); err == nil {
			t.Fatal("accepted damaged payload")
		}
	}
}

func TestProgressFromStatusFile(t *testing.T) {
	for _, tc := range []struct {
		log   string
		want  float64
		stage string
	}{
		{"waiting", 0, "准备安装"},
		{"S=CLOSE\r\nS=CLOSED\r\nS=INSTALL\r\nP=12\r\nP=67\r\n", .67, "正在安装文件"},
		{"P=101\n", 0, "准备安装"},
		{"P=broken\n", 0, "准备安装"},
		{"P=6", 0, "准备安装"},
		{"P=60\nP=20\nS=FINALIZE\n", .60, "正在完成安装"},
	} {
		got := parseStatus(tc.log)
		if got.progress != tc.want || got.stage != tc.stage {
			t.Fatalf("parseStatus(%q) = %+v", tc.log, got)
		}
	}
	status := parseStatus("S=PREPARE\nS=CLOSE\nS=CLOSED\nS=INSTALL\n")
	if !strings.Contains(status.activity, "检查并退出运行中的应用") {
		t.Fatal(status.activity)
	}
}

func TestLegacyInstallerArguments(t *testing.T) {
	a := parseArguments([]string{`/VERYSILENT`, `/DIR=C:\Apps\New`, `/TASKS=desktopicon,!autostart`, `/LOG=C:\setup.log`}, `C:\Default`, true)
	if !a.silent || a.directory != `C:\Apps\New` || !a.desktop || a.autostart || a.logPath != `C:\setup.log` {
		t.Fatalf("unexpected arguments: %+v", a)
	}
	b := parseArguments([]string{`/MERGETASKS=desktopicon`}, `C:\Default`, true)
	if !b.desktop || !b.autostart {
		t.Fatalf("merge lost an existing task: %+v", b)
	}
}

func TestInstallerViewsAndShortcutChoice(t *testing.T) {
	s := &installer{page: "options", directory: `C:\Apps\OpenDL`}
	p := style.Light()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(p.Theme())
		ui.Column(c).Fill().Children(func() {
			switch s.page {
			case "options":
				s.optionsView(c, p)
			case "installing":
				s.progressView(c, p)
			case "error":
				s.errorView(c, p)
			case "done":
				s.doneView(c, p)
			}
		})
	}, 560, 420)
	if s.desktop {
		t.Fatal("desktop shortcut must be unchecked by default")
	}
	if err := tt.Click("创建桌面快捷方式"); err != nil {
		t.Fatal(err)
	}
	if !s.desktop {
		t.Fatal("desktop shortcut did not toggle")
	}
	s.page, s.progress = "installing", .42
	tt.Frame()
	if !tt.HasText("42%") {
		t.Fatalf("progress missing: %v", tt.Texts())
	}
	s.page, s.message, s.logText = "error", "安装失败示例", "error detail"
	tt.Frame()
	if !tt.HasText("安装失败") || !tt.HasText("返回") {
		t.Fatalf("error view missing: %v", tt.Texts())
	}
	s.page = "done"
	tt.Frame()
	if !tt.HasText("完成") || !tt.HasText("启动 OpenDL") {
		t.Fatalf("completion buttons missing: %v", tt.Texts())
	}
	s.page = "options"
	tt.Frame()
	if !s.desktop {
		t.Fatal("shortcut state lost when returning to options")
	}
}

// The optional image output is useful for reviewing both themes without a
// live installation. The bounds assertions run in ordinary test runs too.
func TestInstallerPageLayouts(t *testing.T) {
	if err := style.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	icon, err := ui.DecodeBitmap(resources.IconPNG)
	if err != nil {
		t.Fatal(err)
	}
	for _, palette := range []struct {
		name  string
		value *style.Palette
	}{{"light", style.Light()}, {"dark", style.Dark()}} {
		for _, page := range []struct {
			name     string
			required []string
		}{
			{"options", []string{"安装 OpenDL", "安装位置", "创建桌面快捷方式", "开机自动启动", "安装"}},
			{"installing", []string{"正在安装", "42%", "安装日志"}},
			{"error", []string{"安装失败", "返回", "关闭"}},
			{"done", []string{"安装完成", "完成", "启动 OpenDL"}},
		} {
			t.Run(palette.name+"/"+page.name, func(t *testing.T) {
				s := &installer{icon: icon, page: page.name, directory: `C:\Users\Example\AppData\Local\Programs\OpenDL`, progress: .42, stage: "正在安装文件", activity: "准备安装\n检查并退出运行中的应用\n应用检查完成\n正在安装文件", message: "安装未完成：退出代码 4", logText: "安装日志\n无法访问目标目录。"}
				tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(palette.value.Theme()); s.drawView(c, palette.value) }, 500, 380)
				for _, label := range page.required {
					r, ok := tt.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 500 || r.Y+r.H > 380 {
						t.Errorf("%s outside window: %+v found=%v", label, r, ok)
					}
				}
				if dir := os.Getenv("OPENDL_INSTALLER_PREVIEW_DIR"); dir != "" {
					f, err := os.Create(filepath.Join(dir, palette.name+"-"+page.name+".png"))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(f, tt.Image()); err != nil {
						f.Close()
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
