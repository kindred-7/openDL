package main

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openDL/download"

	"github.com/egoist/mygo/ui"
)

func serveFile(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=\"report.pdf\"")
		w.Header().Set("Content-Length", "2048")
		_, _ = w.Write(make([]byte, 2048))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitDone(t *testing.T, m *download.Manager, id string) download.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.Snapshots() {
			if s.ID == id && s.State == download.Done {
				return s
			}
			if s.ID == id && s.State == download.ErrorS {
				t.Fatalf("task failed: %s", s.Err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timeout")
	return download.Snapshot{}
}

func seededApp(t *testing.T) (*app, *download.Manager) {
	t.Helper()
	srv := serveFile(t)
	mgr := download.NewManager(t.TempDir(), 1, 1)
	waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/dl"}))
	return &app{mgr: mgr, saveDir: t.TempDir(), status: "就绪", showDetail: true}, mgr
}

// The dashboard renders its regions: top bar, sidebar, KPI cards, tabs,
// table, queue chart and detail panel.
func TestDashboardRegions(t *testing.T) {
	a, _ := seededApp(t)
	tt := ui.NewTester(a.view, 1440, 960)
	tt.Frame()
	for _, want := range []string{
		appBrand, "新建下载", "粘贴链接", "导入", "Ctrl+K",
		"仪表盘", "下载中", "已完成", "计划任务", "设置", "存储空间",
		"活跃下载", "总速度", "今日完成", "已暂停",
		"全部", "视频", "文档", "压缩包", "软件",
		"全选", "暂停", "继续", "删除", "限速",
		"文件名", "大小", "进度", "速度", "剩余时间", "状态", "来源", "操作",
		"report.pdf", "PDF", "2 KB",
		"任务队列", "下载速度", "上传速度",
		"下载详情", "基本信息", "存储路径", "下载线程", "校验信息", "调度选项", "打开文件夹",
	} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in texts %q", want, tt.Texts())
		}
	}
}

func TestEmptyState(t *testing.T) {
	a := &app{mgr: download.NewManager(t.TempDir(), 1, 1), saveDir: t.TempDir()}
	tt := ui.NewTester(a.view, 1440, 960)
	if !tt.HasText("还没有下载任务") {
		t.Errorf("texts %q", tt.Texts())
	}
}

// A click on a sortable column header sorts the table by it.
func TestSortByColumnHeader(t *testing.T) {
	a, _ := seededApp(t)
	tt := ui.NewTester(a.view, 1440, 960)
	if err := tt.Click("文件名"); err != nil {
		t.Fatal(err)
	}
	if a.list.Sort == nil || a.list.Sort.Column != "文件名" {
		t.Fatalf("sort %+v", a.list.Sort)
	}
	if err := tt.Click("文件名"); err != nil {
		t.Fatal(err)
	}
	if a.list.Sort == nil || a.list.Sort.Column != "文件名" {
		t.Fatalf("sort after second click %+v", a.list.Sort)
	}
}

// The 新建下载 button opens the inline form and adds a task.
func TestNewDownloadPanel(t *testing.T) {
	mgr := download.NewManager(t.TempDir(), 1, 1)
	a := &app{mgr: mgr, saveDir: t.TempDir()}
	tt := ui.NewTester(a.view, 1440, 960)
	if err := tt.Click("新建下载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("开始下载") {
		t.Fatalf("panel not open: %q", tt.Texts())
	}
	a.input = "ftp://example.com/x"
	if err := tt.Click("开始下载"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Snapshots()) != 0 {
		t.Fatalf("tasks %d", len(mgr.Snapshots()))
	}
	a.input = "http://127.0.0.1:1/manual.zip"
	if err := tt.Click("开始下载"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Snapshots()) != 1 {
		t.Fatalf("tasks %d", len(mgr.Snapshots()))
	}
}

// Checked rows drive the toolbar 删除 command.
func TestCheckboxBulkDelete(t *testing.T) {
	mgr := download.NewManager(t.TempDir(), 1, 1)
	a := &app{mgr: mgr, saveDir: t.TempDir()}
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/alpha.zip", Name: "alpha.zip"})
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/beta.zip", Name: "beta.zip"})
	snaps := mgr.Snapshots()
	*a.checkbox(snaps[0].ID) = true
	tt := ui.NewTester(a.view, 1440, 960)
	tt.Frame()
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Snapshots()) != 1 {
		t.Fatalf("tasks left %d", len(mgr.Snapshots()))
	}
}

// The theme toggles between light (default) and dark.
func TestThemeToggle(t *testing.T) {
	themeOverride = filepath.Join(t.TempDir(), "theme")
	a := &app{mgr: download.NewManager(t.TempDir(), 1, 1), saveDir: t.TempDir()}
	if a.dark {
		t.Fatal("default theme must be light")
	}
	a.toggleTheme()
	if !a.dark || loadTheme() != true {
		t.Fatal("dark not set or not persisted")
	}
	a.toggleTheme()
	if a.dark || loadTheme() != false {
		t.Fatal("light not restored")
	}
}

// The search box filters by file name and URL.
func TestSearchFiltersRows(t *testing.T) {
	mgr := download.NewManager(t.TempDir(), 1, 1)
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/alpha.zip", Name: "alpha.zip"})
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/beta.zip", Name: "beta.zip"})
	a := &app{mgr: mgr, saveDir: t.TempDir()}
	tt := ui.NewTester(a.view, 1440, 960)
	a.focus = "search"
	tt.Frame()
	tt.Type("alpha")
	tt.Frame()
	if !tt.HasText("alpha.zip") {
		t.Errorf("alpha should stay: %q", tt.Texts())
	}
	if tt.HasText("beta.zip") {
		t.Errorf("beta should be filtered out: %q", tt.Texts())
	}
	a.query = "zzz"
	tt.Frame()
	if !tt.HasText("还没有下载任务") {
		t.Errorf("empty state: %q", tt.Texts())
	}
}

// The sidebar views and filter tabs narrow the table.
func TestNavAndTabFilters(t *testing.T) {
	srv := serveFile(t)
	mgr := download.NewManager(t.TempDir(), 1, 1)
	waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/dl"}))
	mgr.Add(download.Request{URL: "http://127.0.0.1:1/album.flac", Name: "album.flac"})
	a := &app{mgr: mgr, saveDir: t.TempDir()}
	tt := ui.NewTester(a.view, 1440, 960)
	// Tab: 音乐-equivalent via 文档? use 视频 tab (empty) then 全部.
	if err := tt.Click("视频"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("report.pdf") {
		t.Errorf("video tab should hide the pdf: %q", tt.Texts())
	}
	if err := tt.Click("全部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("report.pdf") || !tt.HasText("album.flac") {
		t.Errorf("all tab: %q", tt.Texts())
	}
	// Sidebar: 已完成 keeps only the finished task.
	if err := tt.Click("已完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("report.pdf") || tt.HasText("album.flac") {
		t.Errorf("done view: %q", tt.Texts())
	}
	// 计划任务 is an empty view.
	if err := tt.Click("计划任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("report.pdf") {
		t.Errorf("scheduled view should be empty: %q", tt.Texts())
	}
}

// The detail panel follows the selection and can be closed.
func TestDetailPanel(t *testing.T) {
	a, _ := seededApp(t)
	tt := ui.NewTester(a.view, 1440, 960)
	tt.Frame()
	for _, want := range []string{"下载详情", "report.pdf", "2 KB", "127.0.0.1", "SHA-256", "MD5", "自动", "手动", "定时", "下载限速", "完成后校验"} {
		if !tt.HasText(want) {
			t.Errorf("detail missing %q: %q", want, tt.Texts())
		}
	}
	a.showDetail = false
	tt.Frame()
	if tt.HasText("下载详情") {
		t.Errorf("panel should hide: %q", tt.Texts())
	}
}

// The layout adapts to the window width.
func TestResponsiveColumns(t *testing.T) {
	a, _ := seededApp(t)
	// The detail panel shares labels with the columns ("来源网站"), so this
	// test renders with the panel closed to see the table header alone.
	wide := ui.NewTester((&app{mgr: a.mgr, saveDir: t.TempDir()}).view, 1440, 960)
	for _, want := range []string{"来源", "剩余时间", "速度"} {
		if !wide.HasText(want) {
			t.Errorf("wide window missing %q", want)
		}
	}
	narrow := ui.NewTester((&app{mgr: a.mgr, saveDir: t.TempDir()}).view, 1000, 800)
	if narrow.HasText("来源") {
		t.Errorf("narrow window should shed 来源: %q", narrow.Texts())
	}
	if !narrow.HasText("文件名") || !narrow.HasText("进度") {
		t.Errorf("narrow window core columns: %q", narrow.Texts())
	}
}

// The tests render the window to PNGs so the design can be reviewed.
func TestRenderPreview(t *testing.T) {
	for _, tc := range []struct {
		name          string
		dark          bool
		width, height int
	}{
		{"ui-preview.png", false, 1440, 960},
		{"ui-preview-dark.png", true, 1440, 960},
		{"ui-preview-narrow.png", false, 1180, 820},
	} {
		srv := serveFile(t)
		mgr := download.NewManager(t.TempDir(), 1, 1)
		waitDone(t, mgr, mgr.Add(download.Request{URL: srv.URL + "/dl"}))
		mgr.Add(download.Request{URL: "http://127.0.0.1:1/album.flac", Name: "album.flac"})
		mgr.Add(download.Request{URL: "http://127.0.0.1:1/setup-x64.exe", Name: "setup-x64.exe"})
		for i := 0; i < 24; i++ {
			a := &app{mgr: mgr}
			a.pushHistory(float64(8<<20 + i*262144))
		}
		a := &app{mgr: mgr, saveDir: "C:\\Users\\kindr\\Downloads\\opendl", status: "浏览器扩展已就绪", dark: tc.dark, showDetail: true}
		for _, v := range a.histValues(0) {
			_ = v
		}
		a.hist = append(a.hist, 6<<20, 9<<20, 12<<20, 11<<20, 14<<20, 13<<20, 16<<20, 15<<20)
		tt := ui.NewTester(a.view, tc.width, tc.height)
		tt.Frame()
		img := tt.Image()
		if img == nil {
			t.Fatal("no image")
		}
		if err := os.MkdirAll("build", 0o755); err != nil {
			t.Fatal(err)
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
	}
}
