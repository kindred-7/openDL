//go:build windows

// OpenDL's visible installer. The private engine is appended at build time.
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"openDL/installerconfig"
	"openDL/resources"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/sys/windows/registry"
)

const trailerMagic = "OPENDL-INSTALL-2"

var errAutoStartChoice = errors.New("安装完成，但无法保存启动选项")

type installer struct {
	win                *mygo.Window
	icon               *ui.Bitmap
	directory          string
	desktop, autostart bool
	page               string
	progress           float64
	message, logText   string
	errorTitle         string
	stage, activity    string
}

type installStatus struct {
	progress float64
	stage    string
	activity string
}

func main() {
	args := parseArguments(os.Args[1:], previousDir(), previousAutoStart())
	if args.silent {
		err, details := install(args.directory, args.desktop, args.autostart, func(installStatus) {})
		if args.logPath != "" {
			if writeErr := os.WriteFile(args.logPath, []byte(details), 0600); writeErr != nil {
				log.Printf("write installer log: %v", writeErr)
				if err == nil {
					err = writeErr
				}
			}
		}
		if err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if err := style.RegisterFonts(); err != nil {
		log.Printf("fonts: %v", err)
	}
	icon, err := ui.DecodeBitmap(resources.IconPNG)
	if err != nil {
		log.Printf("installer icon: %v", err)
	}
	s := &installer{icon: icon, directory: args.directory, desktop: args.desktop, autostart: args.autostart, page: "options", progress: -1}
	mygo.App.WhenReady(func() {
		s.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "安装 OpenDL", Width: 560, Height: 420, MinWidth: 500, MinHeight: 380,
			DisableMaximize: true, TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 38,
			Content: ui.View(s.view),
		})
		_ = s.win.SetIcon(resources.IconPNG)
		offTheme := mygo.Theme.OnUpdated(func() { s.win.Invalidate() })
		s.win.OnClosed(offTheme)
		s.win.OnClose(func(e *mygo.CloseEvent) {
			if s.page == "installing" {
				e.PreventDefault()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
	}
}

type arguments struct {
	directory, logPath         string
	desktop, autostart, silent bool
}

func parseArguments(values []string, directory string, autostart bool) arguments {
	a := arguments{directory: directory, autostart: autostart}
	for _, value := range values {
		name, param, hasValue := strings.Cut(value, "=")
		switch strings.ToUpper(name) {
		case "/S", "/SILENT", "/VERYSILENT":
			a.silent = true
		case "/DIR":
			if hasValue && param != "" {
				a.directory = param
			}
		case "/LOG":
			if hasValue {
				a.logPath = param
			}
		case "/TASKS", "/MERGETASKS":
			if !hasValue {
				continue
			}
			if strings.EqualFold(name, "/TASKS") {
				a.desktop, a.autostart = false, false
			}
			for _, task := range strings.Split(param, ",") {
				task = strings.TrimSpace(task)
				enabled := !strings.HasPrefix(task, "!")
				switch strings.ToLower(strings.TrimPrefix(task, "!")) {
				case "desktopicon":
					a.desktop = enabled
				case "autostart":
					a.autostart = enabled
				}
			}
		}
	}
	return a
}

func (s *installer) view(c *ui.Context) {
	p := style.Light()
	if mygo.Theme.IsDark() {
		p = style.Dark()
	}
	c.SetTheme(p.Theme())
	s.drawView(c, p)
}

// drawView takes the resolved palette so headless checks exercise the same
// layout in both appearances as the native window.
func (s *installer) drawView(c *ui.Context, p *style.Palette) {
	ui.Column(c).Fill().Background(p.Card).Padding(0, 28, 24, 28).Children(func() {
		ui.Row(c).FillWidth().Height(38).Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
			if s.icon != nil {
				ui.Image(c, s.icon).Size(22, 22).Shrink(0)
			}
			ui.Text(c, "OpenDL").FontSize(13).FontWeight(style.WeightMedium).TextColor(p.Text)
		})
		ui.Column(c).FillWidth().Grow(1).Shrink(1).MinHeight(0).Gap(18).Children(func() {
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
	})
}

func (s *installer) optionsView(c *ui.Context, p *style.Palette) {
	ui.Text(c, "安装 OpenDL").FontSize(23).FontWeight(style.WeightMedium).TextColor(p.Text)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "安装位置").FontSize(style.TextBase).TextColor(p.Text)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			c.Key("install-directory")
			ui.TextInputBase(c, &s.directory).Grow(1).Shrink(1).Basis(0).MinWidth(0).Height(36).Padding(0, 10).Radius(style.RadiusSM).Border(1, p.Border).Background(p.Card).FontSize(style.TextBase).TextColor(p.Text).Label("安装位置")
			ui.Button(c, "浏览").Height(36).OnClick(s.browse)
		})
	})
	ui.Column(c).FillWidth().Gap(14).Children(func() {
		c.Key("desktop-shortcut")
		style.Checkbox(c, p, &s.desktop, "创建桌面快捷方式")
		c.Key("start-at-login")
		style.Checkbox(c, p, &s.autostart, "开机自动启动")
	})
	if s.message != "" {
		ui.Text(c, s.message).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.Danger)
	}
	ui.Box(c).Grow(1).Shrink(1).Basis(0)
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		ui.PrimaryButton(c, "安装").Height(36).MinWidth(112).OnClick(s.start)
	})
}

func (s *installer) progressView(c *ui.Context, p *style.Palette) {
	ui.Text(c, "正在安装").FontSize(23).FontWeight(style.WeightMedium).TextColor(p.Text)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Text(c, s.stage).Role(ui.RoleStatus).FontSize(style.TextBase).TextColor(p.TextSoft)
			ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(s.progress*100)))).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.Text)
		})
		ui.Progress(c, s.progress).FillWidth().Height(8).Label("安装进度")
	})
	ui.Column(c).FillWidth().Grow(1).Shrink(1).MinHeight(0).Gap(8).Children(func() {
		ui.Text(c, "安装日志").FontSize(style.TextSM).TextColor(p.TextSoft)
		c.Key("installation-log")
		ui.TextArea(c, &s.activity).ReadOnly(true).Grow(1).Shrink(1).MinHeight(50).FillWidth().Padding(10).FontSize(style.TextSM).Label("安装日志")
	})
}

func (s *installer) errorView(c *ui.Context, p *style.Palette) {
	title := s.errorTitle
	if title == "" {
		title = "安装失败"
	}
	ui.Text(c, title).FontSize(23).FontWeight(style.WeightMedium).TextColor(p.Text)
	ui.Text(c, s.message).Role(ui.RoleStatus).FontSize(style.TextBase).TextColor(p.Danger)
	c.Key("error-log")
	ui.TextArea(c, &s.logText).ReadOnly(true).Grow(1).Shrink(1).MinHeight(50).FillWidth().Padding(10).FontSize(style.TextSM).Label("错误日志")
	ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
		ui.Button(c, "关闭").Height(36).OnClick(func() { s.win.Close() })
		ui.PrimaryButton(c, "返回").Height(36).OnClick(func() { s.page = "options"; s.message = "" })
	})
}

func (s *installer) doneView(c *ui.Context, p *style.Palette) {
	ui.Column(c).FillWidth().Grow(1).Shrink(1).Basis(0).Center().Gap(16).Children(func() {
		if s.icon != nil {
			ui.Image(c, s.icon).Size(64, 64)
		}
		ui.Text(c, "安装完成").FontSize(23).FontWeight(style.WeightMedium).TextColor(p.Text)
	})
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		ui.Button(c, "完成").Grow(1).Basis(0).Height(44).OnClick(func() { s.win.Close() })
		ui.PrimaryButton(c, "启动 OpenDL").Grow(1).Basis(0).Height(44).OnClick(s.launch)
	})
}

func (s *installer) browse() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: s.win, Title: "选择安装位置", DefaultPath: s.directory, Directory: true, CreateDirectories: true})
	if err != nil {
		s.message = err.Error()
		return
	}
	if len(paths) > 0 {
		s.directory = paths[0]
	}
}

func (s *installer) start() {
	dir := strings.TrimSpace(s.directory)
	if !filepath.IsAbs(dir) || filepath.VolumeName(dir) == "" {
		s.message = "请输入完整的安装路径。"
		return
	}
	s.directory, s.message, s.page, s.progress = filepath.Clean(dir), "", "installing", 0
	s.stage, s.activity = "准备安装", "准备安装"
	win, dest, desktop, autostart := s.win, s.directory, s.desktop, s.autostart
	go func() {
		err, details := install(dest, desktop, autostart, func(status installStatus) {
			win.Update(func() { s.progress, s.stage, s.activity = status.progress, status.stage, status.activity })
		})
		win.Update(func() {
			if err != nil {
				s.errorTitle = "安装失败"
				if errors.Is(err, errAutoStartChoice) {
					s.errorTitle = "安装完成，设置未保存"
				}
				s.message, s.logText, s.page = err.Error(), details, "error"
				return
			}
			s.progress, s.page = 1, "done"
		})
	}()
}

func (s *installer) launch() {
	cmd := exec.Command(filepath.Join(s.directory, "OpenDL.exe"))
	cmd.Dir = s.directory
	if err := cmd.Start(); err != nil {
		s.errorTitle = "无法启动 OpenDL"
		s.message, s.logText, s.page = "无法启动 OpenDL。", err.Error(), "error"
		return
	}
	s.win.Close()
}

func previousDir() string {
	for _, key := range []string{`Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl_is1`, `Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl`} {
		k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		path, _, err := k.GetStringValue("InstallLocation")
		k.Close()
		if err == nil && filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
	}
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "OpenDL")
}

func previousAutoStart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	value, _, err := k.GetStringValue("com.example.opendl")
	k.Close()
	if err != nil || strings.TrimSpace(value) == "" {
		return false
	}
	k, err = registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	state, _, err := k.GetBinaryValue("com.example.opendl")
	k.Close()
	return err != nil || len(state) == 0 || state[0]&1 == 0
}

func extractEngine(dst string) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	return extractEngineFrom(path, dst)
}

func extractEngineFrom(path, dst string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	trailer := int64(len(trailerMagic) + 8 + sha256.Size)
	if stat.Size() < trailer {
		return fmt.Errorf("安装包不完整")
	}
	if _, err = f.Seek(-trailer, io.SeekEnd); err != nil {
		return err
	}
	buf := make([]byte, trailer)
	if _, err = io.ReadFull(f, buf); err != nil {
		return err
	}
	if string(buf[8+sha256.Size:]) != trailerMagic {
		return fmt.Errorf("未找到安装数据")
	}
	size := binary.LittleEndian.Uint64(buf[:8])
	if size == 0 || size > uint64(stat.Size()-trailer) {
		return fmt.Errorf("安装数据无效")
	}
	if _, err = f.Seek(stat.Size()-trailer-int64(size), io.SeekStart); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.CopyN(io.MultiWriter(out, hash), f, int64(size))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr == nil && subtle.ConstantTimeCompare(hash.Sum(nil), buf[8:8+sha256.Size]) != 1 {
		return fmt.Errorf("安装数据校验失败")
	}
	return closeErr
}

func install(dest string, desktop, autostart bool, update func(installStatus)) (error, string) {
	tmp, err := os.MkdirTemp("", "opendl-install-*")
	if err != nil {
		return err, err.Error()
	}
	defer os.RemoveAll(tmp)
	engine, logPath := filepath.Join(tmp, "install.exe"), filepath.Join(tmp, "install.log")
	statusPath := filepath.Join(tmp, "status.txt")
	if err := extractEngine(engine); err != nil {
		return err, err.Error()
	}
	tasks := []string{"!desktopicon", "!autostart"}
	if desktop {
		tasks[0] = "desktopicon"
	}
	if autostart {
		tasks[1] = "autostart"
	}
	cmd := exec.Command(engine, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/DIR="+dest, "/TASKS="+strings.Join(tasks, ","), "/LOG="+logPath, "/OPENDLSTATUS="+statusPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err, err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if data, err := os.ReadFile(statusPath); err == nil {
				update(parseStatus(string(data)))
			}
		case err := <-done:
			data, _ := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("安装未完成：%w", err), string(data)
			}
			if err := installerconfig.WriteAutoStart(autostart, filepath.Join(dest, "OpenDL.exe")); err != nil {
				return fmt.Errorf("%w：%v", errAutoStartChoice, err), string(data)
			}
			return nil, string(data)
		}
	}
}

func parseStatus(data string) installStatus {
	result := installStatus{stage: "准备安装", activity: "准备安装"}
	labels := map[string]string{
		"PREPARE": "准备安装", "CLOSE": "检查并退出运行中的应用", "FORCE": "结束未响应的应用",
		"CLOSED": "应用检查完成", "INSTALL": "正在安装文件", "FINALIZE": "正在完成安装",
	}
	lines := []string{"准备安装"}
	// Ignore an incomplete final write; the next poll will see the full line.
	if end := strings.LastIndexByte(data, '\n'); end >= 0 {
		data = data[:end+1]
	} else {
		data = ""
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "P=") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "P="))
			if err == nil && n >= 0 && n <= 99 && float64(n)/100 > result.progress {
				result.progress = float64(n) / 100
			}
		} else if strings.HasPrefix(line, "S=") {
			if label := labels[strings.TrimPrefix(line, "S=")]; label != "" {
				result.stage = label
				if lines[len(lines)-1] != label {
					lines = append(lines, label)
				}
			}
		}
	}
	result.activity = strings.Join(lines, "\n")
	return result
}
