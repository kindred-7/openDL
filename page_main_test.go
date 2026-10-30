package main

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

func TestMain(m *testing.M) {
	if err := style.RegisterFonts(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// slowServer serves bytes slowly until release closes, keeping a task Running.
func slowServer(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			_, _ = w.Write(make([]byte, 1024))
			f.Flush()
		}
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, release
}

func seededPage(t *testing.T) (*mainPage, *download.Manager) {
	t.Helper()
	srv := serveFile(t)
	dir := t.TempDir()
	mgr := download.NewManager(dir, 2, 8)
	waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/report.pdf", Name: "report.pdf"}))
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/album-flac.rar", Name: "album-flac.rar"})
	pg := &mainPage{mgr: mgr, saveDir: dir}
	pg.night = []download.Request{{URL: "https://files.musicbox.net/night.iso", Name: "night.iso"}}
	pg.nightOn = true
	return pg, mgr
}

// 当前页面区域：标题栏、合并工具栏（操作与筛选）、任务表、
// 详情面板、状态栏。
func TestSegmentCoverageUsesRealFileOffsets(t *testing.T) {
	segments := []download.Segment{{Start: 128, End: 191, Downloaded: 1, Active: true}}
	if got := segmentCoverage(256, segments, 0); got != 0 {
		t.Fatalf("unrelated range filled: %v", got)
	}
	if got := segmentCoverage(256, segments, 64); got != 0.5 {
		t.Fatalf("partial range = %v, want 0.5", got)
	}
	if got := segmentCoverage(256, segments, 65); got != 0 {
		t.Fatalf("undownloaded range filled: %v", got)
	}
}

func TestMainPageRegions(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1680, 880)
	tt.Frame()
	for _, want := range []string{
		appBrand, "Ctrl F",
		"添加 URL", "站点抓取", "计划任务",
		"全部任务", "下载中", "排队中", "已完成", "失败", "主队列", "夜间队列",
		"分类", "视频", "音乐", "压缩包", "程序", "文档",
		"类型", "日期", "清理已完成", "已选 1 项",
		"report.pdf", "album-flac.rar", "night.iso",
		"概览", "分段", "请求头", "日志",
		"任务信息", "峰值", "地址", "保存到", "断点续传", "添加时间",
		// 状态栏：左限速入口与夜间队列开始时间，右下载/排队数量与速度。
		"无限制", "夜间队列 01:00 开始", "个下载中",
	} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in texts %q", want, tt.Texts())
		}
	}
	pg.clearTableSelection()
	tt.Frame()
	for _, want := range []string{"名称", "大小", "进度", "速度", "剩余时间"} {
		if !tt.HasText(want) {
			t.Errorf("missing column header %q in texts %q", want, tt.Texts())
		}
	}
}

// 添加 URL 对话框真的把链接加进引擎。
func TestMainPageAddDialog(t *testing.T) {
	pg, mgr := seededPage(t)
	before := len(mgr.Snapshots())
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	if err := tt.Click("添加 URL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !pg.showAdd {
		t.Fatal("dialog should open")
	}
	pg.dlgLinks = "http://127.0.0.1:1/a.zip\nhttp://127.0.0.1:1/b.zip"
	tt.Frame()
	if err := tt.Click("开始下载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if pg.showAdd {
		t.Fatal("dialog should close")
	}
	if got := len(mgr.Snapshots()) - before; got != 2 {
		t.Fatalf("added %d tasks, want 2", got)
	}
}

// 点任务行切换右侧详情与页签标题。
func TestMainPageSelectDetail(t *testing.T) {
	pg, mgr := seededPage(t)
	var reportID string
	for _, s := range mgr.Snapshots() {
		if s.Name == "report.pdf" {
			reportID = s.ID
		}
	}
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	if err := tt.Click("report.pdf"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if pg.sel != reportID {
		t.Fatalf("sel %q, want %q", pg.sel, reportID)
	}
}

// 搜索框过滤任务。
func TestMainPageSearch(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1440, 880)
	pg.focus = "d1search"
	tt.Frame()
	tt.Type("report")
	tt.Frame()
	if !tt.HasText("report.pdf") {
		t.Errorf("report.pdf should stay: %q", tt.Texts())
	}
	if tt.HasText("album-flac.rar") {
		t.Errorf("album should filter out: %q", tt.Texts())
	}
}

// 侧栏视图过滤任务。
func TestMainPageNavFilter(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1440, 880)
	pg.sel = ""
	tt.Frame()
	pg.nav = 3 // 已完成
	tt.Frame()
	if !tt.HasText("report.pdf") || tt.HasText("album-flac.rar") {
		t.Errorf("done view: %q", tt.Texts())
	}
	pg.nav = 6 // 夜间队列
	tt.Frame()
	if !tt.HasText("night.iso") || tt.HasText("report.pdf") {
		t.Errorf("night view: %q", tt.Texts())
	}
}

// 复选框多选 + 表头「已选 N 项」。
func TestMainPageCheckbox(t *testing.T) {
	pg, mgr := seededPage(t)
	var reportID string
	for _, s := range mgr.Snapshots() {
		if s.Name == "report.pdf" {
			reportID = s.ID
		}
	}
	pg.init()
	pg.checked[reportID] = true
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	if !tt.HasText("已选 1 项") {
		t.Errorf("selection counter: %q", tt.Texts())
	}
}

// 工具栏暂停动作真的暂停运行中的任务。
func TestMainPagePauseRunning(t *testing.T) {
	srv, _ := slowServer(t)
	dir := t.TempDir()
	mgr := download.NewManager(dir, 2, 8)
	id := mgr.Add(download.Request{URL: srv.URL + "/big.bin", Name: "big.bin"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Snapshots() {
			if s.ID == id && s.State == download.Running {
				goto running
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never started")
running:
	pg := &mainPage{mgr: mgr, saveDir: dir, sel: id}
	pg.pauseSelection() // 工具栏「暂停」按钮走的同一个动作
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Snapshots() {
			if s.ID == id && s.State == download.Canceled {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task not paused")
}

// 状态栏：左侧夜间队列开始时间，右侧依次为下载/排队数量、下载速度；
// 不再显示主队列运行状态与独立的“.”分隔。
func TestMainPageStatusBar(t *testing.T) {
	srv, _ := slowServer(t)
	dir := t.TempDir()
	mgr := download.NewManager(dir, 2, 8)
	mgr.Add(download.Request{URL: srv.URL + "/big.bin", Name: "big.bin"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Snapshots() {
			if s.State == download.Running {
				goto running
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never started")
running:
	pg := &mainPage{mgr: mgr, saveDir: dir}
	pg.nightOn = true
	tt := ui.NewTester(pg.view, 1680, 880)
	tt.Frame()
	for _, gone := range []string{"主队列运行中", "主队列空闲"} {
		if tt.HasText(gone) {
			t.Errorf("status bar should not show %q: %q", gone, tt.Texts())
		}
	}
	// 夜间队列关闭时状态栏不显示开始时间。
	pg.nightOn = false
	tt.Frame()
	if tt.HasText("夜间队列 01:00 开始") {
		t.Errorf("status bar should hide night schedule when disabled: %q", tt.Texts())
	}
	pg.nightOn = true
	tt.Frame()
	night, ok := tt.Find("夜间队列 01:00 开始")
	if !ok {
		t.Fatalf("missing night schedule text in %q", tt.Texts())
	}
	tt.ClickAt(night.X+night.W/2, night.Y+night.H/2)
	tt.Frame()
	if !pg.showSched {
		t.Fatal("clicking the night schedule text should open the schedule dialog")
	}
	pg.showSched = false
	tt.Frame()
	count, ok := tt.Find("1 个下载中")
	if !ok {
		t.Fatalf("missing task counts in %q", tt.Texts())
	}
	var speed ui.Rect
	found := false
	for _, n := range statusBarTexts(tt, night.Y) {
		if strings.HasSuffix(n.text, "/s") {
			speed, found = n.rect, true
			break
		}
	}
	if !found {
		t.Fatalf("no status-bar speed text in %q", tt.Texts())
	}
	if !(night.X < count.X && count.X < speed.X) {
		t.Fatalf("status bar order wrong: night %+v, counts %+v, speed %+v", night, count, speed)
	}
	if speed.X+speed.W < 1680-20 {
		t.Fatalf("speed is not on the right: %+v", speed)
	}
	for _, s := range mgr.Snapshots() {
		mgr.Cancel(s.ID)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		live := false
		for _, s := range mgr.Snapshots() {
			if s.State == download.Running || s.State == download.Queued {
				live = true
			}
		}
		if !live {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// statusBarTexts 遍历测试器记录的文本节点，按出现顺序返回
// 位于 yAt 以下（状态栏行）的文本及其矩形。mygo 的 Tester.Find 只按
// 精确文本匹配首个节点，重名文本（如表格的速度列）拿不到目标矩形。
func statusBarTexts(tt *ui.Tester, yAt float32) []struct {
	id   uint64
	text string
	rect ui.Rect
} {
	v := reflect.ValueOf(tt).Elem().FieldByName("rt").Elem().FieldByName("labels")
	labels := reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	var out []struct {
		id   uint64
		text string
		rect ui.Rect
	}
	for i := 0; i < labels.Len(); i++ {
		f := labels.Index(i)
		r := f.FieldByName("r")
		rect := ui.Rect{
			X: float32(r.FieldByName("X").Float()), Y: float32(r.FieldByName("Y").Float()),
			W: float32(r.FieldByName("W").Float()), H: float32(r.FieldByName("H").Float()),
		}
		if rect.Y >= yAt {
			out = append(out, struct {
				id   uint64
				text string
				rect ui.Rect
			}{f.FieldByName("id").Uint(), f.FieldByName("text").String(), rect})
		}
	}
	return out
}

// 空列表显示空状态。
func TestMainPageEmptyState(t *testing.T) {
	pg := &mainPage{mgr: download.NewManager(t.TempDir(), 1, 8), saveDir: t.TempDir()}
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	if !tt.HasText("还没有下载任务") {
		t.Errorf("empty state: %q", tt.Texts())
	}
}

// 宽窗口表格填满内容区；导航支持 Tab/Space；复选框整行标签只切换一次。
func TestMainPageLayoutAndKeyboard(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	remaining, ok := tt.Find("剩余时间")
	if !ok || remaining.X < 1300 || remaining.X+remaining.W > 1440 {
		t.Fatalf("last column does not fill the content area: %+v", remaining)
	}
	if err := tt.Click("全部任务"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if pg.nav != 1 {
		t.Fatalf("keyboard navigation selected %d, want 下载中", pg.nav)
	}
	pg.openSchedule()
	tt.Frame()
	before := pg.dlgScheduleOn
	if err := tt.Click("启用夜间队列"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if pg.dlgScheduleOn == before {
		t.Fatal("checkbox label did not toggle the setting")
	}
	tt.Frame()
	if pg.dlgScheduleOn == before {
		t.Fatal("checkbox setting did not survive another build")
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if pg.showSched || pg.nightOn == before {
		t.Fatal("draft schedule did not commit on save")
	}
}

// 应用菜单的二级菜单在行右侧弹出，不在行下方展开；悬停即展开，Escape 收起。
func TestAppMenuSubmenuOpensRight(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	pg.showAppMenu = true
	tt.Frame()
	row, ok := tt.Find("任务")
	if !ok {
		t.Fatalf("app menu group not found: %q", tt.Texts())
	}
	if tt.HasText("全部暂停") {
		t.Fatal("submenu should start closed")
	}
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	item, ok := tt.Find("全部暂停")
	if !ok {
		t.Fatalf("submenu did not open on hover: %q", tt.Texts())
	}
	// 二级菜单在父项右侧对齐展开，不在其下方。
	if item.X < row.X+row.W {
		t.Fatalf("submenu opens below the row instead of at its right: row=%+v item=%+v", row, item)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("全部暂停") {
		t.Fatal("Escape did not close the submenu")
	}
}

// 悬停展开的二级菜单在指针移开行和弹层后自动收起（含斜向移入宽限期）。
func TestAppMenuSubmenuAutoCloseOnLeave(t *testing.T) {
	pg, _ := seededPage(t)
	tt := ui.NewTester(pg.view, 1440, 880)
	tt.Frame()
	pg.showAppMenu = true
	tt.Frame()
	row, ok := tt.Find("任务")
	if !ok {
		t.Fatalf("app menu group not found: %q", tt.Texts())
	}
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	if !tt.HasText("全部暂停") {
		t.Fatalf("submenu did not open on hover: %q", tt.Texts())
	}
	// 指针移到窗口空白处：离开行和弹层，宽限期后自动收起。
	tt.Move(720, 60)
	tt.Frame()
	if !tt.HasText("全部暂停") {
		t.Fatal("submenu closed immediately on leave without the grace period")
	}
	tt.Advance(200 * time.Millisecond)
	if tt.HasText("全部暂停") {
		t.Fatal("submenu did not auto-close after the pointer left")
	}
}

// 渲染预览图，便于对照设计稿评审。
func TestMainPageRenderPreview(t *testing.T) {
	if err := os.MkdirAll("build", 0o755); err != nil {
		t.Fatal(err)
	}
	slow, _ := slowServer(t)
	for _, tc := range []struct {
		name          string
		dark          bool
		width, height int
	}{
		{"design1-light.png", false, 1440, 880},
		{"design1-dark.png", true, 1440, 880},
		{"design1-small.png", false, 1180, 640},
	} {
		srv := serveFile(t)
		dir := t.TempDir()
		mgr := download.NewManager(dir, 3, 8)
		waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/setup-x64.exe", Name: "setup-x64.exe"}))
		waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/trailer-4k.webm", Name: "trailer-4k.webm"}))
		id := mgr.Add(download.Request{URL: slow.URL + "/ubuntu-24.04-desktop-amd64.iso", Name: "ubuntu-24.04-desktop-amd64.iso"})
		mgr.Add(download.Request{URL: "http://127.0.0.1:1/report-q3.pdf", Name: "report-q3.pdf"})
		pg := &mainPage{mgr: mgr, saveDir: dir, dark: tc.dark, sel: id}
		pg.init()
		pg.checked[id] = true
		pg.night = []download.Request{{URL: "https://files.musicbox.net/album-flac.rar", Name: "album-flac.rar"}}
		tt := ui.NewTester(pg.view, tc.width, tc.height)
		tt.Frame()
		img := tt.Image()
		if img == nil {
			t.Fatal("no image")
		}
		f, err := os.Create(filepath.Join("build", tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
		if tc.name == "design1-light.png" {
			for _, state := range []string{"menu", "add", "schedule"} {
				pg.showMenu, pg.showAdd, pg.showSched = state == "menu", state == "add", state == "schedule"
				if state == "schedule" {
					pg.openSchedule()
				}
				pg.menuRow = id
				pg.menuX, pg.menuY = 510, 369
				tt.Frame()
				file, err := os.Create(filepath.Join("build", "design1-"+state+".png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tt.Image())
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, s := range mgr.Snapshots() {
			mgr.Cancel(s.ID)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			live := false
			for _, s := range mgr.Snapshots() {
				if s.State == download.Running || s.State == download.Queued {
					live = true
				}
			}
			if !live {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
