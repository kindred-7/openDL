package main

// docs/design.html（“Downloader 主界面（shadcn nova）”）的 MyGo 原生 1:1 复刻：
//
//   - 标题栏（38px，保留原生窗口按钮）：品牌、中置搜索、主题/设置、
//     系统原生窗口按钮，整行可拖动；
//   - 工具栏：添加 URL / 站点抓取 / 继续、暂停、停止 / 移动、删除 /
//     计划任务 / 筛选 / 右侧详情栏开关；
//   - 220px 侧栏（视图 + 队列 + 分类 + 磁盘卡）、合并操作与筛选工具栏、
//     任务表（图标槽复选框多选 + 自适应列）、右键菜单、底部详情面板（分段/信息/速度/
//     请求头/日志）、状态栏（左侧限速入口）；
//   - 正式任务由 download.Manager 驱动。

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"openDL/api"
	"openDL/download"
	"openDL/pathrec"
	"openDL/signature"
	"openDL/style"
)

// ── 配色（design.html 的 :root / 深色变量） ──────────────────────────────

type d1Palette struct {
	dark               bool
	accent             string
	bg, fg, mu, mu2    ui.Color
	bd, bd2, card, sel ui.Color
	hover, pressed     ui.Color
	pri, onpri, brand  ui.Color
	ok, bad, page      ui.Color
}

// d1Light/d1Dark 是共享 Radix 令牌到模板组件的兼容适配。
func d1Light() *d1Palette { return d1AccentPalette(false, "") }

func d1AccentPalette(dark bool, accent string) *d1Palette {
	c := style.NewAccentRules(dark, accent).Colors
	return &d1Palette{
		dark: dark, accent: accent,
		bg: c.Bg, fg: c.Fg, mu: c.Muted, mu2: c.Faint,
		bd: c.Border, bd2: c.BorderStrong, card: c.Panel, sel: c.Selection,
		pri: c.Primary, onpri: c.OnPrimary, brand: c.Brand, hover: c.Hover, pressed: c.Pressed,
		ok: c.Success, bad: c.Danger, page: c.Page,
	}
}

func d1Dark() *d1Palette { return d1AccentPalette(true, "") }

func (p *d1Palette) theme() *ui.Theme { return style.NewAccentRules(p.dark, p.accent).Theme() }

// 状态徽章的着色：一律中性描边，仅“失败”以文字着色提示处理
// （对模板 design.html 彩色描边徽章的降噪，遵循用户最新要求）。
func (p *d1Palette) badgeColors(status string) (fg, border ui.Color) {
	if status == "失败" {
		return p.bad, p.bd
	}
	return p.mu, p.bd
}

// 进度条填充色（JS: pc 的三元链；默认 var(--pri)）。
func (p *d1Palette) progressFill(status string) ui.Color {
	switch status {
	case "失败":
		return p.bad
	case "已暂停":
		return p.mu2
	}
	return p.pri
}

// 界面操作图标共用 style.Icon 的 Lucide 资源与描边令牌。
func d1Icon(c *ui.Context, name string, size float32, col ui.Color) ui.Element {
	return style.Icon(c, name, size, col)
}

// ── 页面状态 ─────────────────────────────────────────────────────────────

type d1Row struct {
	id      string
	snap    download.Snapshot
	pending bool
	req     download.Request
}

type speedSample struct {
	at             time.Time
	bytesPerSecond float64
}

type mainPage struct {
	mgr                                 *download.Manager
	saveDir                             string
	win                                 *mygo.Window
	svc                                 *ui.Services
	panes                               panelLayout
	sidebarReveal                       float32
	tablePaneWidth, tablePaneHeight     float32
	layoutW, layoutH                    float32
	layoutRight, layoutBottom           bool
	layoutWrites                        chan panelLayout
	layoutDone                          chan struct{}
	showAppMenu                         bool
	fileIcons                           map[string]*ui.Bitmap
	fileIconOrder                       []string
	fileIconRequests                    chan fileIconRequest
	preferences                         appPreferences
	apiServer                           *api.Server
	preferencesLoaded                   bool
	settingsWindow                      *settingsPage
	addWindow                           *mygo.Window
	settingsSaveError                   string
	updater                             updaterState
	proxyPassword                       string // runtime only; persisted credentials use DPAPI
	proxyError                          string
	proxyJob                            uint64
	settingsWrites                      chan appPreferences
	settingsDone                        chan struct{}
	defaultDirPending                   bool
	defaultConnsPending                 bool
	showColumns, showAbout              bool
	showExtensionGuide                  bool
	showShortcuts                       bool
	showDelete, deleteFiles, deleteBusy bool
	deleteRows                          []d1Row
	showDirHistory                      bool
	appMenuSection                      string

	dark           bool
	themeMode      string
	sel            string
	selAnchor      string // Shift 连续选择的锚点行
	lastRowClickID string
	lastRowClickAt time.Time
	checked        map[string]bool
	nav            int
	navList        ui.Handle
	navRows        []ui.Handle
	sidebarBox     ui.Handle
	tab            int // 详情页签：0 分段 1 信息 2 速度 3 请求头 4 日志
	query          string
	focus          string
	status         string
	statusAt       time.Time

	addWasOpen                                             bool
	browserDraft                                           *download.Request
	browserPromptSlot                                      func()
	stop                                                   chan struct{}
	showAdd                                                bool
	showSource                                             bool
	sourceSaving                                           bool
	sourceJob                                              uint64
	sourceID, sourceURL, sourceOriginalURL                 string
	sourceUser, sourcePassword, sourceHeaders, sourceError string
	sourceSHA256                                           string
	sourceSignatureKey, sourceSignature                    string
	dlgShowPage                                            bool
	dlgLinks                                               string
	dlgNameSource, dlgSuggestedName                        string
	dlgQueue                                               int
	dlgMedia                                               int
	dlgQuality                                             int
	dlgSeg                                                 int
	dlgNow                                                 bool
	dlgOpen                                                bool
	dlgLogin                                               bool
	dlgVerifySHA                                           bool
	dlgKeepExtension                                       bool
	dlgVerifySignature                                     bool
	dlgSHA256                                              string

	dlgDir, dlgPage, dlgUser, dlgPassword, dlgError, dlgName string
	dlgGrabDepth, dlgGrabType                                string
	dlgGrabProject, dlgGrabInterval                          string
	grabProjectCache                                         []grabProject
	dlgExtract, extracting                                   bool
	dlgMirror                                                bool
	extractJob                                               uint64
	dropJob                                                  uint64
	typeFilters                                              map[string]bool
	dateFilter                                               string
	showTypeMenu                                             bool
	sortDesc                                                 bool
	detailCollapsed                                          bool
	detailPlacement                                          string
	searchHandle                                             ui.Handle
	tableHandle                                              ui.Handle
	showLimit                                                bool
	limitDraft, limitError                                   string
	dlgScheduleAt                                            string
	dlgScheduleStop                                          string
	dlgScheduleDays                                          [7]bool
	dlgScheduleOn                                            bool
	scheduleError                                            string
	limitValue                                               string
	showSched                                                bool
	nightOn                                                  bool
	nightAt                                                  string
	nightStop                                                string
	nightDays                                                []int
	stopChecked                                              time.Time
	stopCheckedAt                                            string
	schedulePaused                                           []string

	limitOn     bool
	clipOn      bool
	clipLast    string
	fileMissing map[string]bool // 已完成任务的本地文件被清理；仅在界面线程读写
	quotaWarned bool

	tray     *mygo.Tray
	quitting bool

	showMenu       bool // 行右键菜单
	showDetailMenu bool // 详情“更多”弹层
	menuRow        string
	menuX, menuY   float32
	showLimitMenu  bool

	histMu sync.Mutex
	hist   map[string][]speedSample
	logs   map[string][]string
	prev   map[string]download.State

	nightMu        sync.Mutex
	night          []download.Request
	nightIDs       []string
	nightCreated   map[string]time.Time
	nextNightID    uint64
	nightChecked   time.Time
	nightCheckedAt string
	nightAwaiting  map[string]time.Time

	started sync.Once
	// windowStarted 确保窗口事件与后台工作只绑定一次。
	windowStarted bool
}

var d1NavLabels = []string{
	"全部任务", "下载中", "排队中", "已完成", "失败",
	"主队列", "夜间队列",
	"视频", "音乐", "压缩包", "程序", "文档",
}

// navLabels 返回侧栏导航项：固定视图与队列、自定义队列、内置分类、自定义分类。
func navLabels() []string {
	labels := append([]string(nil), d1NavLabels[:7]...)
	for _, spec := range activeCustomQueues {
		labels = append(labels, spec.Name)
	}
	labels = append(labels, d1NavLabels[7:]...)
	for _, spec := range activeCustomCategories {
		labels = append(labels, spec.Label)
	}
	return labels
}

// navCategoryIcon 返回分类导航项的图标键；自定义分类回退通用文件图标。
func navCategoryIcon(idx int) string {
	if idx < len(builtinCategoryKinds) {
		return builtinCategoryKinds[idx]
	}
	return "file"
}

// navRow 返回索引对应的导航行句柄，按需扩展容量。
func (pg *mainPage) navRow(idx int) *ui.Handle {
	for len(pg.navRows) <= idx {
		pg.navRows = append(pg.navRows, ui.Handle{})
	}
	return &pg.navRows[idx]
}

// navRowsFor 扩展容量并返回前 n 个导航行句柄。
func (pg *mainPage) navRowsFor(n int) []ui.Handle {
	pg.navRow(n - 1)
	return pg.navRows[:n]
}

var d1TabLabels = []string{"概览", "分段", "请求头", "日志"}

func (pg *mainPage) init() {
	pg.initState()
	pg.startWindow()
}

// initState 一次性准备视图状态，不依赖窗口句柄。
func (pg *mainPage) initState() {
	pg.started.Do(func() {
		pg.hist = map[string][]speedSample{}
		pg.logs = map[string][]string{}
		pg.prev = map[string]download.State{}
		pg.checked = map[string]bool{}
		pg.typeFilters = map[string]bool{}
		pg.panes.defaults()
		if !pg.preferencesLoaded {
			pg.preferences = appPreferences{Directory: pg.saveDir, Connections: 8, Clipboard: true, NightOn: false, NightAt: "01:00"}
		}
		pg.nightAt = pg.preferences.NightAt
		pg.nightOn = pg.preferences.NightOn
		pg.nightStop = pg.preferences.NightStop
		pg.nightDays = pg.preferences.NightDays
		pg.clipOn = pg.preferences.Clipboard
		pg.sortDesc = pg.preferences.SortDesc
		pg.dlgSeg = connectionIndex(pg.preferences.Connections)
		pg.dlgNow = true
		if pg.detailPlacement == "" {
			pg.detailPlacement = "bottom"
		}
		// 任务属性或 Alt+Enter 展开详情时恢复上次停靠位置；
		// 应用打开默认不展开底部或右侧详情面板。
		pg.detailCollapsed = true
		pg.dlgDir = pg.saveDir
		pg.limitValue = "5"
		if pg.preferences.RateMBps > 0 {
			pg.limitOn = true
			pg.limitValue = strconv.FormatFloat(pg.preferences.RateMBps, 'f', -1, 64)
			if pg.mgr != nil {
				pg.mgr.SetRateLimit(int64(pg.preferences.RateMBps * float64(1<<20)))
			}
		}
		pg.nightCreated = map[string]time.Time{}
		if pg.mgr != nil {
			for _, s := range pg.mgr.Snapshots() {
				pg.prev[s.ID] = s.State
			}
			if snaps := pg.mgr.Snapshots(); len(snaps) > 0 && pg.sel == "" {
				pg.sel = snaps[0].ID
			}
		}
		if pg.saveDir == "" {
			pg.saveDir = downloadDir()
		}
		pg.dlgDir = pg.saveDir
	})
}

// startWindow 绑定窗口事件并启动后台工作。窗口创建后立即调用，
// 不依赖首次视图构建的时机，关闭拦截与设置写入因此始终生效。
func (pg *mainPage) startWindow() {
	pg.initState()
	if pg.win == nil || pg.windowStarted {
		return
	}
	pg.windowStarted = true
	pg.stop = make(chan struct{})
	pg.win.OnClosed(func() { pg.panelLayoutChanged(); pg.saveAppPreferences(); close(pg.stop); pg.extractJob++ })
	pg.win.OnClose(func(e *mygo.CloseEvent) {
		// 后台运行开启时关闭主窗口驻留后台；退出应用时放行关闭。
		if pg.closeToBackground() {
			e.PreventDefault()
			pg.win.Hide()
		}
	})
	pg.win.OnMaximize(func() { pg.win.Invalidate() })
	pg.win.OnUnmaximize(func() { pg.win.Invalidate() })
	pg.layoutWrites = make(chan panelLayout, 1)
	pg.layoutDone = make(chan struct{})
	go pg.panelLayoutWatch()
	pg.settingsWrites = make(chan appPreferences, 1)
	pg.settingsDone = make(chan struct{})
	go pg.settingsWatch()
	if fileIconsAvailable {
		pg.fileIcons = map[string]*ui.Bitmap{}
		pg.fileIconRequests = make(chan fileIconRequest, 32)
		go pg.fileIconWatch()
	}
	go pg.clipboardWatch()
	go pg.fileStateWatch()
	go pg.trayStatusWatch()
	go pg.nightWatch()
	go pg.grabWatch()
	go pg.siteRulesRemoteWatch()
}

// onChange 由引擎在每次状态变化后调用（后台线程）。
func (pg *mainPage) onChange() {
	if pg.mgr == nil {
		return
	}
	now := time.Now()
	anyCompleted, active := false, false
	for _, s := range pg.mgr.Snapshots() {
		if s.State == download.Running || s.State == download.Queued {
			active = true
		}
		pg.histMu.Lock()
		h := pg.hist[s.ID]
		sample := speedSample{at: now, bytesPerSecond: float64(s.Speed)}
		if len(h) > 0 && h[len(h)-1].at.Unix() == now.Unix() {
			h[len(h)-1] = sample
		} else {
			h = append(h, sample)
		}
		if len(h) > 60 {
			h = h[len(h)-60:]
		}
		pg.hist[s.ID] = h
		// completed 只在观察到向 Done 的迁移时成立：恢复的已完成任务首次
		// 出现时不发通知，避免启动时补发历史完成。
		completed := false
		if prev, ok := pg.prev[s.ID]; !ok || prev != s.State {
			completed = ok && s.State == download.Done
			anyCompleted = anyCompleted || completed
			line := now.Format("15:04:05") + "  " + tr(d1StateLabel(s.State))
			if s.Err != "" {
				line += tr("：") + tr(s.Err)
			}
			pg.logs[s.ID] = append(pg.logs[s.ID], line)
			pg.prev[s.ID] = s.State
		}
		pg.histMu.Unlock()
		if completed && pg.win != nil {
			name, path, id := s.Name, s.FilePath, s.ID
			pg.win.Update(func() {
				select {
				case <-pg.stop:
					return
				default:
					if pg.preferences.NotifyComplete {
						completeNotifier.add(name, id)
					}
					pg.startPostScan(name, path)
				}
			})
		}
		if s.State == download.Done && s.OpenFolder && pg.win != nil && pg.mgr.ClaimOpenFolder(s.ID) {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
					return
				default:
					pg.openFolder(s)
				}
			})
		}
	}
	finishAction.consider(anyCompleted, !active, pg.queueFinished)
	if exhausted := pg.mgr.QuotaExhausted(); exhausted != pg.quotaWarned {
		pg.quotaWarned = exhausted
		if exhausted && pg.win != nil {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
					return
				default:
					pg.flash(tr("流量配额已用完，将在下个周期恢复"))
				}
			})
		}
	}
	if pg.svc != nil {
		pg.svc.Invalidate()
	}
}

func d1StateLabel(s download.State) string {
	switch s {
	case download.Queued:
		return "排队中"
	case download.Running:
		return "下载中"
	case download.Done:
		return "已完成"
	case download.Canceled:
		return "已暂停"
	}
	return "失败"
}

// ── 视图骨架 ─────────────────────────────────────────────────────────────

func (pg *mainPage) view(c *ui.Context) {
	pg.init()
	if !pg.showSource {
		pg.sourcePassword, pg.sourceHeaders = "", ""
	}
	if pg.addWasOpen && !pg.showAdd && pg.addWindow == nil {
		pg.dlgPassword = ""
		pg.dlgName = ""
		pg.dlgNameSource, pg.dlgSuggestedName = "", ""
		pg.browserDraft = nil
		pg.extractJob++
		pg.extracting = false
	}
	pg.addWasOpen = pg.showAdd
	// Keep active-row actions on visible results; checked selections remain
	// across filters and are explicitly counted in the filter bar.
	if pg.sel != "" {
		visible := false
		for _, r := range pg.visibleRows() {
			if r.id == pg.sel {
				visible = true
				break
			}
		}
		if !visible {
			pg.sel = ""
		}
	}
	if pg.svc == nil {
		s := c.Services()
		pg.svc = &s
	}
	p := d1AccentPalette(pg.dark, pg.preferences.AccentColor)
	c.SetTheme(p.theme())

	if !pg.showDelete && !pg.deleteBusy && !pg.showAdd && !pg.showSource && !pg.showSched && !pg.showAbout && !pg.showExtensionGuide && !pg.showShortcuts && !pg.showDirHistory && !pg.showColumns && !pg.showMenu && !pg.showAppMenu && !pg.showTypeMenu && !pg.showLimitMenu && !pg.showLimit {
		if pg.shortcutPressed(c, "search") {
			pg.focus = "d1search"
		}
		if pg.shortcutPressed(c, "add") {
			pg.openAdd(false)
		}
		if !pg.searchHandle.Focused(c) {
			pg.taskShortcuts(c)
		}
	}
	if c.Shortcut(0, ui.KeyEscape) {
		if pg.searchHandle.Focused(c) && pg.query != "" {
			// 搜索框内先清空关键词，再按一次才收起菜单。
			pg.query = ""
		} else {
			pg.showMenu = false
			pg.showDirHistory = false
			pg.appMenuSection = ""
			pg.showAppMenu = false
		}
	}

	c.Key("main-page-layout")
	root := ui.Column(c).Fill().Font(style.FontFamily).FontSize(style.TextBase).FontWeight(style.WeightRegular).LineHeight(style.LineHeight).Background(p.bg)
	// 整窗接收拖入的链接、文本与链接列表文件。
	if drop, ok := ui.DropData(root, transfer.DropOptions{
		Formats:    []transfer.Format{transfer.URIList, transfer.FileList, transfer.Text},
		Operations: transfer.Copy,
	}); ok {
		pg.takeDropped(drop)
	}
	pg.sidebarReveal = root.AnimateWith("sidebar-reveal", motionTarget(!pg.panes.SidebarCollapsed), 260*time.Millisecond, ui.EaseInOut)
	pg.preparePanelLayout(c)
	root.Children(func() {
		pg.titleBar(c, p)
		// 工具栏移入内容列（与表格同宽），左侧栏从标题栏下沿一直到顶。
		w, _ := c.Size()
		collapsiblePanelSplit(c, p, "sidebar-split", &pg.panes.Left, 180, min(float32(480), w-480), pg.sidebarReveal,
			func() { pg.sidebar(c, p) }, func() { pg.content(c, p) }, pg.panelLayoutChanged).
			Grow(1).Shrink(1).Basis(0).FillWidth()
		pg.statusBar(c, p)
	})

	if pg.showAdd && pg.win == nil {
		c.Key("add-dialog")
		pg.addDialog(c, p)
	}
	if pg.showSource {
		c.Key("source-dialog")
		pg.sourceDialog(c, p)
	}
	if pg.showSched {
		c.Key("schedule-dialog")
		pg.schedDialog(c, p)
	}
	if pg.showLimit {
		c.Key("limit-dialog")
		pg.limitDialog(c, p)
	}
	if pg.showAbout {
		pg.aboutDialog(c, p)
	}
	if pg.showExtensionGuide {
		pg.extensionGuideDialog(c, p)
	}
	if pg.showDelete {
		pg.deleteDialog(c, p)
	}
	if pg.updater.showDialog {
		c.Key("update-dialog")
		pg.updateDialog(c, p)
	}
}

// ── 标题栏（38px，原生窗口按钮在右端） ──────────────────────────────────

func (pg *mainPage) titleBar(c *ui.Context, p *d1Palette) {
	tb := c.TitleBar()
	barH := tb.Height
	if barH <= 0 {
		barH = 38 // design.html 的 .ttl 高度
	}
	ui.Row(c).Height(barH).PaddingStart(12).Gap(10).AlignItems(ui.Center).
		Background(p.card).BorderWidth(0, 0, 1, 0).BorderColor(p.bd).DragWindow().Children(func() {
		if tb.Left > 0 {
			ui.Box(c).Width(tb.Left).Height(barH)
		}
		pg.applicationMenu(c, p)
		ui.RichText(c,
			ui.Span{Text: "Open", Weight: style.WeightBrand, Color: p.brand},
			ui.Span{Text: "DL", Weight: style.WeightBrandStrong, Color: p.fg},
		).FontSize(style.TextBrand).Label(appBrand).SingleLine()
		pg.updateBadge(c, p)
		searchFocused := false
		searchBox := ui.Row(c).Width(360).Height(26).Padding(0, 10).Gap(8).AlignItems(ui.Center).
			Radius(style.RadiusSM).Border(1, p.bd).Background(p.bg).Margin(0, ui.Auto)
		searchBox.Children(func() {
			d1Icon(c, "search", style.SizeIcon, p.mu)
			c.Key("d1search")
			ui.TextInputBase(c, &pg.query).Bind(&pg.searchHandle).Grow(1).Shrink(1).Basis(0).FocusBind(&pg.focus, "d1search").
				Label(tr("搜索任务")).Placeholder(tr("搜索名称或下载地址")).FontSize(style.TextSearch).
				FontWeight(style.WeightRegular)
			searchFocused = pg.searchHandle.Focused(c)
			if pg.query != "" {
				c.Key("clear-task-search")
				clear := ui.ButtonBase(c).Size(20, 20).Shrink(0).Radius(style.RadiusSM).Center().Label(tr("清除搜索")).Tooltip(tr("清除搜索"))
				hoverMotion(clear, ui.Transparent, p.hover)
				clear.Children(func() { d1Icon(c, "x", 14, p.mu) })
				clear.OnClick(func() { pg.query = ""; pg.focus = "d1search" })
			} else {
				ui.Row(c).Height(17).Padding(0, style.KbdPadX).Radius(style.KbdRadius).Border(1, p.bd).Center().Children(func() {
					ui.Text(c, shortcutKeyLabel(shortcutText(pg.preferences, "search"))).FontSize(style.TextXS).TextColor(p.mu).SingleLine()
				})
			}
		})
		if searchFocused {
			searchBox.BorderColor(p.pri)
		}

		c.Key("titlebar-settings")
		d1IconButton(c, p, "cog").Size(28, 28).MarginEnd(4).OnClick(pg.openSettings)
		// 主题按钮紧贴原生窗口按钮区：簇内零间隙，抵消行间距。
		ui.Row(c).Gap(0).AlignItems(ui.Center).Children(func() {
			// 图标以翻转动效在目标状态间过渡，见 themeIconButton。
			if pg.themeIconButton(c, p).Clicked() {
				pg.setDarkTheme(!pg.dark)
			}
			// 原生窗口按钮（最小化/最大化/关闭）由系统画在标题栏右端。
			ui.Box(c).Width(tb.Right).Height(barH)
		})
	})
}

func (pg *mainPage) sidebarToggle(c *ui.Context, p *d1Palette) {
	c.Key("sidebar-toggle")
	label := "收起侧栏"
	if pg.panes.SidebarCollapsed {
		label = "展开侧栏"
	}
	toggle := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().
		Role(ui.RoleToggleButton).Label(tr(label)).Tooltip(tr(label)).Checked(!pg.panes.SidebarCollapsed).Expanded(!pg.panes.SidebarCollapsed)
	hoverMotion(toggle, ui.Transparent, p.sel)
	color := p.mu
	if toggle.Hovered() || toggle.FocusVisible() || toggle.Pressed() {
		color = p.fg
	}
	if toggle.Pressed() {
		toggle.Background(p.pressed)
	}
	reveal := pg.sidebarReveal
	toggle.Children(func() {
		ui.Box(c).Size(style.SizeIcon, style.SizeIcon).Draw(func(painter *ui.Painter, r ui.Rect) {
			painter.Fill(ui.Rect{X: r.X + 2.4, Y: r.Y + 2.4, W: 3.6, H: 11.2}, color.Alpha(reveal), 0.8)
		}).Children(func() { d1Icon(c, "panel-left", style.SizeIcon, color) })
	})
	toggle.OnClick(func() {
		pg.panes.SidebarCollapsed = !pg.panes.SidebarCollapsed
		pg.showLimitMenu = false
		pg.panelLayoutChanged()
	})
}

// ── 工具栏（46px） ──────────────────────────────────────────────────────

func (pg *mainPage) toolbar(c *ui.Context, p *d1Palette) {
	// 可用宽度与任务表一致；窄窗口的次要操作和筛选保留在应用菜单。
	avail := pg.taskViewportWidth(c)
	compact := avail < 1280
	narrow, tiny := avail < 800, avail < 560
	if narrow {
		pg.showTypeMenu = false
	}
	// 与表格同宽同内边距（TaskTable.PadX），分隔线由表头自带顶边承担。
	ui.Row(c).Height(style.HeightToolbar).Padding(0, style.TaskTable.PadX).Gap(4).AlignItems(ui.Center).
		Role(ui.RoleToolbar).Label(tr("任务操作与筛选")).Children(func() {
		pg.sidebarToggle(c, p)
		if !tiny {
			d1Sep(c, p)
			d1IconButton(c, p, "play").Label(tr("全部开始")).Tooltip(tr("全部开始")).Disabled(!pg.hasResumableTask()).OnClick(pg.startAllTasks)
			d1IconButton(c, p, "pause").Label(tr("全部暂停")).Tooltip(tr("全部暂停")).Disabled(!pg.hasActive() && !pg.nightOn).OnClick(pg.pauseAllTasks)
			d1Sep(c, p)
		}
		if !compact {
			d1Button(c, p, "站点抓取", "globe").OnClick(func() { pg.openAdd(true) })
		}
		if !tiny {
			d1Button(c, p, "清理已完成", "list-checks", pg.hasCompleted()).OnClick(pg.clearCompleted)
		} else {
			d1SelectionAction(c, p, "清理已完成", "list-checks", p.mu, pg.hasCompleted()).OnClick(pg.clearCompleted)
		}
		if !compact {
			d1Button(c, p, "计划任务", "clock").OnClick(pg.openSchedule)
		}
		if narrow {
			ui.Box(c).Grow(1).Shrink(1).Basis(0)
		} else {
			pg.filterControls(c, p)
		}
		d1PrimaryButton(c, p, "添加 URL", "plus").OnClick(func() { pg.openAdd(false) })
		pg.columnMenuButton(c, p).Shrink(0)

	})
}

// ── 通用控件 ────────────────────────────────────────────────────────────

func d1Button(c *ui.Context, p *d1Palette, label, icon string, enabled ...bool) ui.Element {
	return d1ButtonIcon(c, p, label, icon, p.fg, enabled...)
}

// d1ButtonIcon 是普通描边按钮：28px 高、RadiusSM、描边、悬停底色过渡。
// enabled 为假时内容弱化并禁用交互。
func d1ButtonIcon(c *ui.Context, p *d1Palette, label, icon string, iconCol ui.Color, enabled ...bool) ui.Element {
	c.Key(label)
	ok := len(enabled) == 0 || enabled[0]
	e := ui.ButtonBase(c).Height(style.HeightButton).Padding(0, 10).Radius(style.RadiusSM).Border(1, p.bd).Center().Disabled(!ok)
	col, textCol := iconCol, p.fg
	if !ok {
		col, textCol = p.mu, p.mu
	}
	hoverMotion(e, p.bg, p.hover)
	e.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if icon != "" {
				d1Icon(c, icon, 16, col)
			}
			ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(textCol)
		})
	})
	return e
}

// 切换主题按钮：图标表达目标状态，当前浅色时显示“moon”（点击变深），
// 当前深色时显示“sun”（点击变浅）；切换时以水平翻转动效在两枚图标间过渡，
// 减少动态效果时直接切换。
func (pg *mainPage) themeIconButton(c *ui.Context, p *d1Palette) ui.Element {
	target := float32(0)
	if pg.dark {
		target = 1
	}
	button := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().
		Label(tr("切换主题")).Tooltip(tr("切换主题"))
	hoverMotion(button, ui.Transparent, p.hover)
	turn := button.Animate("theme-flip", target, 220*time.Millisecond)
	pulse := 1 - absFloat32(turn*2-1)
	bulge := 1 + 0.08*pulse
	squeeze := 1 - 0.06*pulse
	twist := 5 * pulse
	// 卡片式翻转：两面随角度旋转并交叉淡入淡出，中间叠加缩放、斜切和旋转，
	// 形成轻微的立体变形；不依赖背面剔除，保证浅色只见 moon、深色只见 sun。
	moonFade := 1 - 2*turn
	if moonFade < 0 {
		moonFade = 0
	}
	sunFade := 2*turn - 1
	if sunFade < 0 {
		sunFade = 0
	}
	button.Children(func() {
		ui.Box(c).Size(16, 16).Children(func() {
			c.Key("theme-moon")
			d1Icon(c, "moon", 16, p.mu).Absolute().Perspective(300).RotateY(turn*180).Scale(bulge, squeeze).Skew(0, twist).Opacity(moonFade)
			c.Key("theme-sun")
			d1Icon(c, "sun", 16, p.mu).Absolute().Perspective(300).RotateY(180-turn*180).Scale(bulge, squeeze).Skew(0, -twist).Opacity(sunFade)
		})
	})
	return button
}

func absFloat32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func d1IconButton(c *ui.Context, p *d1Palette, name string) ui.Element {
	label := map[string]string{"sun": "切换主题", "play": "继续", "pause": "暂停", "stop": "全部停止", "folder": "打开所在文件夹", "trash": "删除", "cog": "设置", "panel-right": "右侧详情栏", "panel-bottom": "底部详情面板", "close": "收起详情", "dots": "更多操作", "copy": "复制日志"}[name]
	e := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().Label(tr(label)).Tooltip(tr(label))
	hoverMotion(e, ui.Transparent, p.hover)
	e.Children(func() {
		d1Icon(c, name, 16, p.mu)
	})
	return e
}

func d1Sep(c *ui.Context, p *d1Palette) {
	ui.Box(c).Width(1).Height(18).Background(p.bd).Margin(0, 6)
}

func d1MenuSep(c *ui.Context, p *d1Palette) {
	ui.Box(c).FillWidth().Height(1).Background(p.bd).Margin(4, 0)
}

// ── 行模型 ──────────────────────────────────────────────────────────────

func (pg *mainPage) allRows() []d1Row {
	var rows []d1Row
	if pg.mgr != nil {
		for _, s := range pg.mgr.Snapshots() {
			if s.Night {
				req, _ := pg.mgr.Request(s.ID)
				if s.Name != "" {
					req.Name = s.Name
				}
				rows = append(rows, d1Row{id: s.ID, snap: s, pending: true, req: req})
			} else {
				rows = append(rows, d1Row{id: s.ID, snap: s})
			}
		}
	}
	pg.nightMu.Lock()
	pg.ensureNightIDs()
	night := append([]download.Request(nil), pg.night...)
	ids := append([]string(nil), pg.nightIDs...)
	pg.nightMu.Unlock()
	for i, r := range night {
		rows = append(rows, d1Row{id: ids[i], pending: true, req: r})
	}
	return rows
}

func (pg *mainPage) visibleRows() []d1Row {
	q := strings.ToLower(strings.TrimSpace(pg.query))
	var out []d1Row
	for _, r := range pg.allRows() {
		if !pg.rowMatchesNav(r) || !pg.matchesFilters(r) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.rowName()), q) &&
			!strings.Contains(strings.ToLower(r.rowURL()), q) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].rowName()), strings.ToLower(out[j].rowName())
		if a == b {
			return out[i].id < out[j].id
		}
		if pg.sortDesc {
			return a > b
		}
		return a < b
	})
	return out
}

func (pg *mainPage) rowMatchesNav(r d1Row) bool {
	switch pg.nav {
	case 0:
		return true
	case 1:
		return !r.pending && r.snap.State == download.Running
	case 2:
		return r.pending || (!r.pending && r.snap.State == download.Queued)
	case 3:
		return !r.pending && r.snap.State == download.Done
	case 4:
		return r.snap.State == download.ErrorS
	case 5:
		return !r.pending && !isCustomQueue(r.snap.Queue)
	case 6:
		return r.pending
	}
	if pg.nav >= 7 {
		idx := pg.nav - 7
		if idx < len(activeCustomQueues) {
			return !r.pending && r.snap.Queue == activeCustomQueues[idx].ID
		}
		cat := idx - len(activeCustomQueues)
		if cat < len(builtinCategoryKinds) {
			return r.rowKind() == builtinCategoryKinds[cat]
		}
		custom := cat - len(builtinCategoryKinds)
		if custom < len(activeCustomCategories) {
			return customCategoryMatch(activeCustomCategories[custom], r.rowName())
		}
	}
	return true
}

func (r d1Row) rowName() string {
	if r.pending {
		if r.req.Name != "" {
			return r.req.Name
		}
		if u, err := url.Parse(r.req.URL); err == nil {
			if b := filepath.Base(u.Path); b != "." && b != "/" && b != "" {
				return b
			}
		}
		return r.req.URL
	}
	return r.snap.Name
}

func (r d1Row) rowURL() string {
	if r.pending {
		return r.req.URL
	}
	return r.snap.URL
}

func (r d1Row) rowHost() string {
	if h := hostOf(r.rowURL()); h != "" {
		return h
	}
	return r.rowURL()
}

func (r d1Row) rowKind() string {
	return categoryIcon(r.rowName())
}

func (pg *mainPage) rowStatus(r d1Row) string {
	if r.pending {
		if r.snap.State == download.ErrorS {
			return "失败"
		}
		return "排队中"
	}
	if r.snap.Verifying {
		return "校验中"
	}
	if r.snap.Reencoding {
		return "重新编码中"
	}
	if r.snap.FinishingLive {
		return "正在保存录制"
	}
	return d1StateLabel(r.snap.State)
}

func (pg *mainPage) rowQueue(r d1Row) string {
	if r.pending {
		return tr("夜间 ") + pg.nightAt
	}
	if isCustomQueue(r.snap.Queue) {
		return customQueueName(r.snap.Queue)
	}
	if r.snap.Queue == "night" {
		return tr("夜间 ") + pg.nightAt
	}
	return tr("主队列")
}

func (pg *mainPage) rowPct(r d1Row) int {
	if (r.pending && r.snap.ID == "") || r.snap.Total <= 0 {
		if !r.pending && r.snap.State == download.Done {
			return 100
		}
		return 0
	}
	v := int(float64(r.snap.Done) / float64(r.snap.Total) * 100)
	if v > 100 {
		v = 100
	}
	return v
}

func (pg *mainPage) rowSize(r d1Row) string {
	if r.pending && r.snap.ID == "" {
		return "—"
	}
	if r.snap.Total > 0 {
		return bytesText(r.snap.Total)
	}
	if r.snap.Done > 0 {
		return bytesText(r.snap.Done)
	}
	return "—"
}

func (pg *mainPage) rowSpeed(r d1Row) string {
	if !r.pending && r.snap.Speed > 0 {
		return bytesText(r.snap.Speed) + "/s"
	}
	return "-"
}

func (pg *mainPage) rowRemain(r d1Row) string {
	if r.pending {
		return "-"
	}
	s := r.snap
	if s.State != download.Running || s.Speed <= 0 || s.Total <= s.Done || s.Total <= 0 {
		return "-"
	}
	sec := (s.Total - s.Done) / s.Speed
	if sec < 60 {
		return fmt.Sprintf(tr("%d 秒"), sec)
	}
	return fmt.Sprintf(tr("%d 分"), sec/60)
}

func (pg *mainPage) rowAdded(r d1Row) string {
	if r.pending && r.snap.ID == "" {
		return pg.pendingCreated(r.id).Format("15:04")
	}
	return r.snap.Created.Format("15:04")
}

func (pg *mainPage) selectedRow() (d1Row, bool) {
	for _, r := range pg.allRows() {
		if r.id == pg.sel {
			return r, true
		}
	}
	return d1Row{}, false
}

// selection 返回勾选集合；没有勾选时退化为当前选中行。
func (pg *mainPage) selection() []d1Row {
	var out []d1Row
	for _, r := range pg.allRows() {
		if pg.checked[r.id] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		if r, ok := pg.selectedRow(); ok {
			out = append(out, r)
		}
	}
	return out
}

// ── 侧栏（220px） ───────────────────────────────────────────────────────

func (pg *mainPage) sidebar(c *ui.Context, p *d1Palette) {
	root := ui.Column(c).Fill().Padding(10, 8).Background(p.card).Bind(&pg.sidebarBox)
	root.Children(func() {
		rows := pg.allRows()
		running, queued, done, failed := 0, 0, 0, 0
		queueRunning := map[string]int{}
		for _, r := range rows {
			if r.pending {
				if r.snap.State == download.ErrorS {
					failed++
				} else {
					queued++
				}
				continue
			}
			switch r.snap.State {
			case download.Running:
				running++
				if isCustomQueue(r.snap.Queue) {
					queueRunning[r.snap.Queue]++
				}
			case download.Queued:
				queued++
			case download.Done:
				done++
			case download.ErrorS:
				failed++
			}
		}
		counts := []string{strconv.Itoa(len(rows)), strconv.Itoa(running), strconv.Itoa(queued), strconv.Itoa(done), strconv.Itoa(failed), fmt.Sprintf("%d / %d", running, pg.parallelism()), pg.nightAt}
		categoryCounts := make([]int, len(builtinCategoryKinds)+len(activeCustomCategories))
		for _, r := range rows {
			kind := r.rowKind()
			for i, k := range builtinCategoryKinds {
				if kind == k {
					categoryCounts[i]++
				}
			}
			for i, spec := range activeCustomCategories {
				if customCategoryMatch(spec, r.rowName()) {
					categoryCounts[len(builtinCategoryKinds)+i]++
				}
			}
		}
		queueCounts := make([]string, 0, len(activeCustomQueues))
		for _, spec := range activeCustomQueues {
			if spec.Limit > 0 {
				queueCounts = append(queueCounts, fmt.Sprintf("%d / %d", queueRunning[spec.ID], spec.Limit))
			} else {
				queueCounts = append(queueCounts, strconv.Itoa(queueRunning[spec.ID]))
			}
		}

		ui.Scroll(c).Grow(1).Shrink(1).Basis(0).FillWidth().Children(func() {
			c.Key("main-nav-list")
			labels := navLabels()
			rows := pg.navRowsFor(len(labels))
			list := ui.Column(c).FillWidth().Bind(&pg.navList)
			var selected *ui.Handle
			if pg.nav >= 0 && pg.nav < len(rows) {
				selected = &rows[pg.nav]
			}
			slide := navSlidePosition(c, list, &pg.navList, selected)
			queues := len(activeCustomQueues)
			groups := []navGroup{{0, 4}, {5, 6}}
			next := 7
			if queues > 0 {
				groups = append(groups, navGroup{7, 6 + queues})
				next += queues
			}
			groups = append(groups, navGroup{next, len(labels) - 1})
			navSurfaces(c, list, &pg.navList, rows, groups, p, slide)
			list.Children(func() {
				// 与 d1NavLabels 前 7 项一一对应。
				icons := []string{"layout-list", "down", "clock", "check-check", "circle-off", "layers", "orbit"}
				for i := 0; i < 7; i++ {
					if i == 5 {
						d1SectionHead(c, p, "队列")
					}
					pg.navItem(c, p, i, icons[i], counts[i], slide)
				}
				for i := 0; i < queues; i++ {
					pg.navItem(c, p, 7+i, "layers", queueCounts[i], slide)
				}
				d1SectionHead(c, p, "分类")
				for i := 7 + queues; i < len(labels); i++ {
					cat := i - 7 - queues
					count := ""
					if cat < len(categoryCounts) {
						count = strconv.Itoa(categoryCounts[cat])
					}
					pg.navItem(c, p, i, navCategoryIcon(cat), count, slide)
				}
			})
		})
	})
}

func (pg *mainPage) navItem(c *ui.Context, p *d1Palette, idx int, icon, count string, slide navSlide) {
	on := pg.nav == idx
	labels := navLabels()
	if idx >= len(labels) {
		return
	}
	label := labels[idx]
	// Custom names are user content, even if they match a catalog message.
	if idx < 7 || (idx >= 7+len(activeCustomQueues) && idx < 7+len(activeCustomQueues)+len(builtinCategoryKinds)) {
		label = tr(label)
	}
	c.Key("nav:" + strconv.Itoa(idx))
	// 尾边距让开纵向滚动条：滑块贴在容器右缘内 3px、宽 6px，行内容再留间距。
	row := ui.ButtonBase(c).FillWidth().Padding(6, 14, 6, 8).Gap(8).AlignItems(ui.Center).Justify(ui.Start).Radius(style.RadiusSM).
		Margin(0, 0, style.NavItemGap, 0).Label(label).Role(ui.RoleToggleButton).Checked(on).Cursor(ui.CursorPointer).Bind(pg.navRow(idx))
	navMotion(row, p, on, slide)
	row.Children(func() {
		ic, tx := p.mu, p.mu
		if on {
			ic, tx = p.fg, p.fg
		}
		d1Icon(c, icon, 16, ic)
		ui.Text(c, label).FontSize(style.TextBase).FontWeight(map[bool]int{false: style.WeightRegular, true: style.WeightMedium}[on]).TextColor(tx).
			Grow(1).Shrink(1).Basis(0).SingleLine()
		if count != "" {
			ui.Text(c, tr(count)).FontSize(style.TextXS).TextColor(p.mu2).SingleLine()
		}
	})
	if row.Clicked() {
		pg.nav = idx
	}
}

func d1SectionHead(c *ui.Context, p *d1Palette, label string) {
	ui.Text(c, tr(label)).FontSize(style.TextXS).TextColor(p.mu2).Margin(14, 8, 4, 8)
}

// diskStatusControl 用间距和细分隔线区分各卷，完整状态保留在提示中。
func (pg *mainPage) diskStatusControl(c *ui.Context, p *d1Palette) {
	c.Key("disk-status")
	stats := diskStats(pg.saveDir)
	if len(stats) == 0 {
		return
	}
	row := ui.Row(c).Height(24).Padding(0, 6).Gap(12).Radius(style.RadiusSM).Shrink(0).AlignItems(ui.Center)
	hoverMotion(row, ui.Transparent, p.hover)
	color, nameColor := p.mu, p.mu2
	if row.Hovered() || row.Pressed() || row.FocusVisible() {
		color, nameColor = p.fg, p.mu
	}
	row.Children(func() {
		d1Icon(c, style.IconDisk, 14, color)
		for i, stat := range stats {
			stat := stat
			if i > 0 {
				ui.Box(c).Width(1).Height(12).Background(p.bd)
			}
			ui.Row(c).Gap(6).Shrink(0).AlignItems(ui.Center).Children(func() {
				label, tip, pct, low := "—", "—", 0, false
				if stat.total > 0 {
					label = diskFreeText(stat.free)
					tip = diskFreeLabel(stat.free)
					pct = 100 - int(float64(stat.free)/float64(stat.total)*100)
					if pct < 0 {
						pct = 0
					} else if pct > 100 {
						pct = 100
					}
					low = float64(stat.free) < 0.1*float64(stat.total)
				}
				valueColor, fill := color, p.mu
				if low {
					valueColor, fill = p.bad, p.bad
				}
				ui.Text(c, tr(stat.name)).FontSize(style.TextSM).TextColor(nameColor).SingleLine()
				ui.Text(c, label).FontSize(style.TextSM).FontWeight(style.WeightMedium).FontFeatures("tnum").
					TextColor(valueColor).SingleLine().Tooltip(tr(stat.name) + "：" + tip)
				ui.Box(c).Width(28).Height(4).MarginStart(2).Tooltip(tr(stat.name) + "：" + tip).Draw(func(pnt *ui.Painter, r ui.Rect) {
					pnt.Fill(r, p.bd, 2)
					if pct > 0 {
						pnt.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * float32(pct) / 100, H: r.H}, fill, 2)
					}
				})
			})
		}
	})
}

// diskFreeText 返回剩余空间的紧凑数值；不足 1 GB 用 MB，避免显示「0 GB」。
func diskFreeText(free int64) string {
	if free >= 1<<30 {
		return fmt.Sprintf("%.0f GB", float64(free)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(free)/(1<<20))
}

// diskFreeLabel 返回 Tooltip 里的完整说法「214 GB 可用」。
func diskFreeLabel(free int64) string {
	if free >= 1<<30 {
		return fmt.Sprintf(tr("%.0f GB 可用"), float64(free)/(1<<30))
	}
	return fmt.Sprintf(tr("%.0f MB 可用"), float64(free)/(1<<20))
}

// ── 右侧内容：任务表 + 详情面板 ────────────────────────

func (pg *mainPage) content(c *ui.Context, p *d1Palette) {
	if pg.rightDetailOpen() {
		contentWidth := pg.contentWidth(c)
		panelSplit(c, p, "right-detail-split", &pg.tablePaneWidth, 320, contentWidth-281,
			func() { pg.taskColumn(c, p) }, func() { pg.detailPanel(c, p) }, func() {
				pg.panes.Right = clampPanel(contentWidth-1-pg.tablePaneWidth, 280, contentWidth-321)
				pg.tablePaneWidth = contentWidth - 1 - pg.panes.Right
				pg.panelLayoutChanged()
			}).Fill()
		return
	}
	pg.taskColumn(c, p)
}

func (pg *mainPage) taskColumn(c *ui.Context, p *d1Palette) {
	c.Key("task-pane")
	ui.Column(c).Fill().Children(func() {
		pg.toolbar(c, p)
		if !pg.detailCollapsed && pg.detailPlacement != "right" {
			areaHeight := pg.tableAreaHeight(c)
			panelSplitVertical(c, p, "bottom-detail-split", &pg.tablePaneHeight, 150, areaHeight-141, func() {
				ui.Column(c).Fill().Children(func() { pg.table(c, p, pg.visibleRows()) })
			}, func() { pg.detailPanel(c, p) }, func() {
				pg.panes.Bottom = clampPanel(areaHeight-1-pg.tablePaneHeight, 140, areaHeight-151)
				pg.tablePaneHeight = areaHeight - 1 - pg.panes.Bottom
				pg.panelLayoutChanged()
			}).Grow(1).Shrink(1).Basis(0).FillWidth()
			return
		}
		pg.table(c, p, pg.visibleRows())
	})
}

// Keep the checkbox in the layout/input tree; reveal only its painting.
// Selected/mixed state and keyboard focus remain visible without hover.
func d1Checkbox(c *ui.Context, p *d1Palette, on, reveal, mixed bool) ui.Element {
	e := ui.CheckboxBase(c, &on).Size(style.SizeCheckbox, style.SizeCheckbox).JustifySelf(ui.Start).Radius(4).Center()
	if mixed {
		e.Mixed()
	}
	show := on || mixed || reveal || e.Hovered() || e.FocusVisible()
	e.Children(func() {
		mark := ui.Box(c).Size(style.SizeCheckbox, style.SizeCheckbox)
		if !show {
			mark.Opacity(0)
		}
		mark.Children(func() {
			if mixed {
				ui.Box(c).Size(style.SizeCheckbox, style.SizeCheckbox).Radius(4).Border(1, p.pri).Background(p.pri).Center().Children(func() { style.Icon(c, style.IconMinus, 13, p.onpri) })
			} else {
				d1CheckMark(c, p, on)
			}
		})
	})
	return e
}

func d1CheckMark(c *ui.Context, p *d1Palette, on bool) {
	box := ui.Box(c).Size(style.SizeCheckbox, style.SizeCheckbox).Radius(4).Border(1, p.bd2)
	if on {
		box.Background(p.pri).Border(1, p.pri).Center().Children(func() {
			style.Icon(c, style.IconCheck, 11, p.onpri)
		})
	}
}

// d1FileSlot 是行首 28px 图标槽：平时显示系统文件图标，失败时回退 Lucide，
// 悬停/勾选/聚焦时同一位置显示复选框，几何尺寸不变。
func d1FileSlot(c *ui.Context, p *d1Palette, r d1Row, bitmap *ui.Bitmap, on, reveal, mixed, missing bool) ui.Element {
	// 复选框不绘制背景与边框，悬停/勾选/聚焦只在原位显示标记。
	e := ui.CheckboxBase(c, &on).Size(28, 28).Radius(style.RadiusSM).Center()
	if mixed {
		e.Mixed()
	}
	show := on || mixed || reveal || e.Hovered() || e.FocusVisible()
	e.Children(func() {
		if show {
			if mixed {
				ui.Box(c).Size(style.SizeCheckbox, style.SizeCheckbox).Radius(4).Border(1, p.pri).Background(p.pri).
					Center().Children(func() {
					style.Icon(c, style.IconMinus, 13, p.onpri)
				})
			} else {
				d1CheckMark(c, p, on)
			}
			return
		}
		if bitmap != nil {
			icon := ui.Image(c, bitmap).Size(24, 24)
			if missing {
				icon.Opacity(0.4)
			}
			return
		}
		color := p.mu
		if missing {
			color = p.mu2.Mix(p.card, 0.35)
		}
		d1Icon(c, r.rowKind(), 16, color)
	})
	return e
}

// contextMenu 是任务右键菜单（JS: .menu）。
func (pg *mainPage) contextMenu(c *ui.Context, p *d1Palette) {
	var row d1Row
	found := false
	for _, r := range pg.allRows() {
		if r.id == pg.menuRow {
			row, found = r, true
		}
	}
	if !found {
		pg.showMenu = false
		return
	}
	ui.Overlay(c, func() {
		w, h := c.Size()
		dim := ui.Box(c).Absolute().Left(0).Top(0).Width(w).Height(h).Modal()
		if dim.Clicked() {
			pg.showMenu = false
		}
		dim.Children(func() {
			menu := ui.Column(c).Width(230).Absolute().Left(max(4, min(pg.menuX, w-234))).Top(max(4, min(pg.menuY, h-340))).
				Padding(4).Radius(8).Border(1, p.bd2).Background(p.bg).
				Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.14))
			menu.Role(ui.RoleMenu).FocusGroup(ui.Vertical).Children(func() {
				if menu.OverlayShortcut(0, ui.KeyEscape) {
					pg.showMenu = false
				}
				pg.taskMenuBody(c, p, row, true, func() { pg.showMenu = false })
			})
		})
	})
}

// taskMenuBody 是行右键菜单与详情“更多”弹层共用的快捷键与条目；dismiss 收起所在菜单。
// withProperties 仅在行右键菜单显示“属性”（打开/恢复详情面板），面板内的“更多”不提供该入口。
func (pg *mainPage) taskMenuBody(c *ui.Context, p *d1Palette, row d1Row, withProperties bool, dismiss func()) {
	pg.menuShortcut(c, "toggle", func() { pg.toggleRow(row); dismiss() })
	c.OnShortcut(ui.Alt, ui.KeyEnter, func() { pg.tab = 0; pg.showDetails(); dismiss() })
	pg.menuShortcut(c, "delete", func() { pg.removeRowPrompt(row); dismiss() })
	pg.menuShortcut(c, "open-folder", func() { pg.openRowFolder(row); dismiss() })
	pg.menuShortcut(c, "copy-url", func() {
		pg.copyDownloadURL(row.rowURL())
		dismiss()
	})
	icon, label := pg.primaryRowAction(row)
	pg.d1MenuIconItem(c, p, icon, label, p.fg, func() { pg.toggleRow(row); dismiss() })

	if label != "打开文件" {
		pg.d1MenuIconItem(c, p, "file", "打开文件", p.fg, func() {
			pg.openRowFile(row)
			dismiss()
		}, !row.pending && row.snap.State == download.Done && row.snap.FilePath != "")
	}
	pg.d1MenuIconItem(c, p, "folder", "打开所在文件夹", p.fg, func() {
		pg.openRowFolder(row)
		dismiss()
	})
	pg.d1MenuIconItem(c, p, "link", "复制下载链接", p.fg, func() {
		pg.copyDownloadURL(row.rowURL())
		dismiss()
	})
	pg.d1MenuIconItem(c, p, "redo", "重新下载", p.fg, func() { pg.redownload(row); dismiss() })
	if signature.Available {
		pg.d1MenuIconItem(c, p, "check", "验证文件数字签名", p.fg, func() { pg.verifyFileSignature(row); dismiss() }, !row.pending && row.snap.State == download.Done && !row.snap.Verifying)
	}
	pg.d1MenuIconItem(c, p, "link", "更新下载信息", p.fg, func() {
		pg.openSource(row)
		dismiss()
	}, row.snap.ID != "" && row.snap.State != download.Done && row.snap.State != download.Running && (row.snap.State != download.Queued || row.snap.Held))
	pg.d1MenuIconItem(c, p, "layers", map[bool]string{true: "移至主队列（等待）", false: "移至夜间队列"}[row.pending], p.fg, func() {
		pg.moveQueue(row)
		dismiss()
	}, row.pending || row.snap.State != download.Done)
	for _, spec := range activeCustomQueues {
		queue := spec
		pg.d1MenuIconItem(c, p, "layers", tr("移至 ")+queue.Name, p.fg, func() {
			pg.moveRowToQueue(row, queue.ID)
			dismiss()
		}, !row.pending && row.snap.State != download.Done && row.snap.Queue != queue.ID)
	}
	d1MenuSep(c, p)
	if withProperties {
		pg.d1MenuIconItem(c, p, "info", "属性", p.fg, func() {
			pg.sel = row.id
			pg.tab = 0
			pg.showDetails()
			dismiss()
		})
	}
	pg.d1MenuIconItem(c, p, "trash", "删除任务", p.bad, func() {
		pg.removeRowPrompt(row)
		dismiss()
	})
}

func (pg *mainPage) d1MenuIconItem(c *ui.Context, p *d1Palette, icon, label string, col ui.Color, act func(), enabled ...bool) {
	c.Key(label)
	b := ui.ButtonBase(c).Role(ui.RoleMenuItem).Label(tr(label)).FillWidth().Padding(6, 8).Radius(5).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(p.hover)
	}
	if len(enabled) > 0 {
		b.Disabled(!enabled[0])
		if !enabled[0] {
			col = p.mu
		}
	}
	b.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			d1Icon(c, icon, 16, col)
			ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(col).Grow(1).Shrink(1).Basis(0).SingleLine()
		})
	})
	if b.Clicked() {
		if act != nil {
			act()
		}
	}
}

// ── 详情面板（.dp：页签 + 概览） ─────────────────────────────────────────

// overviewTab 是「概览」页：文件标识、进度、分段概要、速度曲线与任务信息。
// 右侧停靠按参考稿纵向排布；底部停靠保持三栏。
func (pg *mainPage) overviewTab(c *ui.Context, p *d1Palette, r d1Row) {
	if pg.detailPlacement == "right" {
		ui.Column(c).FillWidth().Children(func() {
			pg.ovSection(c, p, func() {
				pg.ovHeader(c, p, r)
				pg.ovProgress(c, p, r)
			})
			pg.ovDivider(c, p)
			pg.ovSection(c, p, func() { pg.ovChart(c, p, r, false) })
			pg.ovDivider(c, p)
			pg.ovSection(c, p, func() { pg.ovInfo(c, p, r) })
		})
		return
	}
	ui.Grid(c).ColumnTracks(ui.Fr(1.5), ui.Fr(1.2), ui.Fr(1)).Padding(14).Gap(24).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Children(func() {
			pg.ovHeader(c, p, r)
			pg.ovProgress(c, p, r)
		})
		ui.Column(c).Children(func() { pg.ovChart(c, p, r, true) })
		ui.Column(c).Children(func() { pg.ovInfo(c, p, r) })
	})
}

func (pg *mainPage) ovSection(c *ui.Context, p *d1Palette, build func()) {
	ui.Column(c).FillWidth().Padding(14, 16).Children(build)
}

func (pg *mainPage) ovDivider(c *ui.Context, p *d1Palette) {
	ui.Box(c).FillWidth().Height(1).Background(p.bd)
}

// ovHeader 是概览的文件标识：图标 + 文件名 + 来源主机。
func (pg *mainPage) ovHeader(c *ui.Context, p *d1Palette, r d1Row) {
	host := "—"
	if u, err := url.Parse(r.rowURL()); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Margin(0, 0, 14, 0).Children(func() {
		ui.Box(c).Size(40, 40).Shrink(0).Radius(style.RadiusMD).Background(p.pri.Alpha(0.08)).Center().Children(func() {
			if bitmap := pg.taskFileIcon(r); bitmap != nil {
				ui.Image(c, bitmap).Size(22, 22)
				return
			}
			d1Icon(c, r.rowKind(), 20, p.pri)
		})
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, r.rowName()).FillWidth().MinWidth(0).FontSize(style.TextTitle).FontWeight(style.WeightBrand).TextColor(p.fg).SingleLine().Tooltip(r.rowName())
			ui.Text(c, host).FillWidth().MinWidth(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine().Tooltip(host)
		})
	})
}

// ovProgress 是概览的进度区：大号百分比、状态、数值行与分段胶囊。
func (pg *mainPage) ovProgress(c *ui.Context, p *d1Palette, r d1Row) {
	ui.Row(c).FillWidth().AlignItems(ui.End).Justify(ui.SpaceBetween).Children(func() {
		ui.Row(c).AlignItems(ui.End).Gap(2).Children(func() {
			ui.Text(c, strconv.Itoa(pg.rowPct(r))).FontSize(style.TextHero).FontWeight(style.WeightBrand).TextColor(p.fg)
			ui.Text(c, tr("%")).FontSize(style.TextLG).FontWeight(style.WeightMedium).TextColor(p.mu).Margin(0, 0, 7, 0)
		})
		ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(p.pri).Shrink(0)
			ui.Text(c, tr(pg.rowStatus(r))).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
		})
	})
	ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Margin(10, 0, 0, 0).Children(func() {
		ui.Text(c, tr(pg.rowDone(r)+" / "+pg.rowSize(r))).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
		ui.Text(c, tr(pg.ovSpeedLine(r))).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
	})
	// 分段胶囊推到列底，与图表 0 刻度线在同一水平线上。
	ui.Box(c).Grow(1).Shrink(1).Basis(0)
	pg.ovSegments(c, p, r)
}

// ovSpeedLine 拼接“速度，剩余”一行；缺失的部分不显示。
func (pg *mainPage) ovSpeedLine(r d1Row) string {
	if !r.pending && r.snap.State == download.Done {
		if r.snap.Elapsed <= 0 {
			return tr("用时未知")
		}
		seconds := max(int64(1), int64(r.snap.Elapsed/time.Second))
		if seconds < 60 {
			return fmt.Sprintf(tr("用时 %d 秒"), seconds)
		}
		if seconds < 3600 {
			return fmt.Sprintf(tr("用时 %d 分 %d 秒"), seconds/60, seconds%60)
		}
		return fmt.Sprintf(tr("用时 %d 小时 %d 分"), seconds/3600, seconds%3600/60)
	}
	speed, remain := pg.rowSpeed(r), pg.rowRemain(r)
	switch {
	case speed == "-" && remain == "-":
		return ""
	case remain == "-":
		return speed
	case speed == "-":
		return tr("剩余 ") + remain
	}
	return speed + tr("，剩余 ") + remain
}

// ovSegments 是连接级进度胶囊：按连接数量划分文件区间，每格显示该区间的完成度。
func (pg *mainPage) ovSegments(c *ui.Context, p *d1Palette, r d1Row) {
	connections := r.snap.Connections
	if r.pending {
		connections = r.req.Connections
	}
	pills := connections
	if pills == -1 {
		pills = 0
		for _, segment := range r.snap.Segments {
			if segment.Active {
				pills++
			}
		}
		if pills == 0 && len(r.snap.Segments) > 0 {
			pills = len(r.snap.Segments)
		}
	}
	if !r.pending && r.snap.State == download.Done {
		pills = 1
	}
	if len(r.snap.Segments) <= 1 {
		pills = 1
	}
	pills = max(1, min(pills, 32))
	total := r.snap.Total
	fill := p.progressFill(pg.rowStatus(r))
	ui.Row(c).FillWidth().Gap(4).Margin(12, 0, 0, 0).Children(func() {
		for i := 0; i < pills; i++ {
			fraction := float32(pg.rowPct(r)) / 100
			if (r.pending || r.snap.State != download.Done) && total > 0 && len(r.snap.Segments) > 0 {
				start := total/int64(pills)*int64(i) + total%int64(pills)*int64(i)/int64(pills)
				end := total/int64(pills)*int64(i+1) + total%int64(pills)*int64(i+1)/int64(pills)
				fraction = sliceCoverage(total, r.snap.Segments, start, end)
			}
			c.Key(i)
			bar := ui.Box(c).Grow(1).Shrink(1).Basis(0).MinWidth(3).Height(8)
			animated := bar.Animate("segment-progress", fraction, 700*time.Millisecond)
			bar.Draw(func(pnt *ui.Painter, rr ui.Rect) {
				pnt.Fill(rr, p.bd, 4)
				if animated > 0 {
					width := rr.W * animated
					pnt.Fill(ui.Rect{X: rr.X, Y: rr.Y, W: width, H: rr.H}, fill, min(width/2, float32(4)))
				}
			})
		}
	})
}

// ovChart 是概览的速度曲线区；fill 模式让曲线占满容器高度。
func (pg *mainPage) ovChart(c *ui.Context, p *d1Palette, r d1Row, fill bool) {
	pg.h4(c, p, "速度（最近 60 秒，MB/s）", tr("峰值 ")+pg.peakText(r))
	pg.chart(c, p, r, fill)
}

// ovInfo 是概览的任务信息区。
func (pg *mainPage) ovInfo(c *ui.Context, p *d1Palette, r d1Row) {
	pg.h4(c, p, "任务信息", "")
	pg.kvList(c, p, pg.taskKV(r))
}

// segTab 是「分段」页：分块位图 + 连接列表。
func (pg *mainPage) segTab(c *ui.Context, p *d1Palette, r d1Row) {
	ui.Column(c).Padding(14).Children(func() {
		label := "总进度概览（非分片）"
		if len(r.snap.Segments) > 0 && r.snap.Total > 0 {
			label = "分段进度"
		}
		pg.h4(c, p, label, pg.rowDone(r)+" / "+pg.rowSize(r))
		ui.Text(c, pg.rowSpeed(r)).FontSize(style.TextSM).TextColor(p.fg).Margin(0, 0, 8, 0)
		if r.snap.Total > 0 {
			pg.blockMap(c, p, r)
		} else {
			ui.Text(c, tr("文件大小未知，无法显示区间比例")).FontSize(style.TextSM).TextColor(p.mu)
		}
		pg.connList(c, p, r)
	})
}

func (pg *mainPage) h4(c *ui.Context, p *d1Palette, left, right string) {
	ui.Row(c).Margin(0, 0, 8, 0).Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, tr(left)).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
		if right != "" {
			ui.Text(c, tr(right)).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
		}
	})
}

// blockMap 按文件偏移展示已下载区间和活跃分段。
func (pg *mainPage) blockMap(c *ui.Context, p *d1Palette, r d1Row) {
	total := r.snap.Total
	segments := r.snap.Segments
	pct := pg.rowPct(r)
	ui.Box(c).FillWidth().Height(72).Draw(func(pnt *ui.Painter, rr ui.Rect) {
		pnt.Fill(rr, p.bd, 6)
		if !r.pending && r.snap.State == download.Done {
			pnt.Fill(ui.Rect{X: rr.X, Y: rr.Y + 1, W: rr.W, H: rr.H - 2}, p.pri, 0)
		} else if len(segments) > 0 {
			for _, segment := range segments {
				if segment.Start < 0 || segment.Start >= total {
					continue
				}
				downloaded := max(segment.Downloaded, int64(0))
				if segment.End >= segment.Start {
					downloaded = min(downloaded, segment.End-segment.Start+1)
				}
				downloaded = min(downloaded, total-segment.Start)
				if downloaded > 0 {
					pnt.Fill(ui.Rect{X: rr.X + rr.W*float32(segment.Start)/float32(total), Y: rr.Y + 1, W: rr.W * float32(downloaded) / float32(total), H: rr.H - 2}, p.pri, 0)
				}
			}
		} else if pct > 0 {
			pnt.Fill(ui.Rect{X: rr.X, Y: rr.Y + 1, W: rr.W * float32(pct) / 100, H: rr.H - 2}, p.pri, 0)
		}
		if total > 0 {
			for _, segment := range segments {
				if segment.Start < 0 || segment.Start >= total {
					continue
				}
				x := rr.X + rr.W*float32(segment.Start)/float32(total)
				color := p.mu
				if segment.Active {
					color = p.ok
				}
				pnt.Line(x, rr.Y+1, x, rr.Y+rr.H-1, 2, color)
			}
		}
	})
	ui.Row(c).FillWidth().Margin(6, 0, 0, 0).Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, "0").FontSize(style.TextXS).TextColor(p.mu)
		ui.Text(c, fmt.Sprintf("%d%%", pct)).FontSize(style.TextXS).TextColor(p.mu)
		ui.Text(c, pg.rowSize(r)).FontSize(style.TextXS).TextColor(p.mu)
	})
	ui.Row(c).FillWidth().Gap(12).Margin(10, 0, 0, 0).Children(func() {
		for _, item := range []struct {
			label string
			color ui.Color
		}{{"已下载", p.pri}, {"活跃分段", p.ok}, {"分段边界", p.mu}} {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Width(8).Height(8).Radius(2).Background(item.color)
				ui.Text(c, tr(item.label)).FontSize(style.TextXS).TextColor(p.mu)
			})
		}
	})
}

// Each cell represents the corresponding file interval, including partial
// coverage when a real segment boundary falls inside that interval.
func segmentCoverage(total int64, segments []download.Segment, index int) float32 {
	if total <= 0 || index < 0 || index >= 128 {
		return 0
	}
	boundary := func(i int64) int64 { return (total/128)*i + (total%128)*i/128 }
	return sliceCoverage(total, segments, boundary(int64(index)), boundary(int64(index+1)))
}

// sliceCoverage 返回文件区间 [start,end) 被分段已下载数据覆盖的比例；
// 单流分段按连续前缀计入，未知长度的结束值不截断统计。
func sliceCoverage(total int64, segments []download.Segment, start, end int64) float32 {
	if total <= 0 || end <= start {
		return 0
	}
	var covered int64
	for _, segment := range segments {
		if segment.Start < 0 || segment.Start >= total {
			continue
		}
		downloaded := max(segment.Downloaded, int64(0))
		if segment.End >= segment.Start {
			downloaded = min(downloaded, segment.End-segment.Start+1)
		}
		left, right := max(start, segment.Start), min(end, segment.Start+downloaded)
		if right > left {
			covered += right - left
		}
	}
	return min(float32(1), float32(covered)/float32(end-start))
}

// connList 展示实际分段的进度与活跃速度。
func (pg *mainPage) connList(c *ui.Context, p *d1Palette, r d1Row) {
	segments := r.snap.Segments
	active := 0
	for _, segment := range segments {
		if segment.Active {
			active++
		}
	}
	ui.Column(c).FillWidth().Gap(8).Margin(16, 0, 0, 0).Children(func() {
		if len(segments) == 0 {
			ui.Text(c, tr("暂无分段明细")).FontSize(style.TextXS).TextColor(p.mu)
			return
		}
		ui.Text(c, fmt.Sprintf(tr("活跃分段：%d，当前分段：%d"), active, len(segments))).FontSize(style.TextXS).TextColor(p.mu)
		ui.Grid(c).Columns(2).Gap(6).Children(func() {
			for i, segment := range segments {
				fraction := float32(0)
				if segment.End >= segment.Start {
					fraction = min(float32(1), float32(max(segment.Downloaded, int64(0)))/float32(segment.End-segment.Start+1))
				}
				ui.Column(c).Padding(8).Gap(6).Border(1, p.bd).Radius(4).Children(func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Children(func() {
						ui.Text(c, fmt.Sprintf("C%02d", i+1)).FontSize(style.TextXS).TextColor(p.mu)
						if segment.End >= segment.Start {
							ui.Text(c, fmt.Sprintf("%.0f%%", fraction*100)).FontSize(style.TextXS).TextColor(p.fg)
						}
					})
					ui.Box(c).FillWidth().Height(5).Draw(func(pnt *ui.Painter, rr ui.Rect) {
						pnt.Fill(rr, p.bd, 2)
						if fraction > 0 {
							pnt.Fill(ui.Rect{X: rr.X, Y: rr.Y, W: rr.W * fraction, H: rr.H}, p.pri, 2)
						}
					})
					if segment.Active {
						ui.Text(c, bytesText(segment.Speed)+"/s").FontSize(style.TextXS).TextColor(p.ok).SingleLine()
					} else {
						ui.Text(c, bytesText(segment.Downloaded)).FontSize(style.TextXS).TextColor(p.mu).SingleLine()
					}
				})
			}
		})
	})
}

// chart 是速度曲线（JS: svg viewBox 0 0 300 110）。fill 模式拉伸占满容器高度，
// 否则按 300:110 固定比例随宽度自适应。
func (pg *mainPage) chart(c *ui.Context, p *d1Palette, r d1Row, fill bool) {
	pg.histMu.Lock()
	samples := append([]speedSample(nil), pg.hist[r.id]...)
	pg.histMu.Unlock()
	vals := make([]float64, 61)
	now := time.Now().Unix()
	for _, sample := range samples {
		age := now - sample.at.Unix()
		if age >= 0 && age <= 60 {
			vals[60-int(age)] = sample.bytesPerSecond
		}
	}
	box := ui.Box(c).FillWidth()
	if fill {
		box.Grow(1).Shrink(1).Basis(0).MinHeight(120)
	} else {
		box.AspectRatio(300.0 / 110.0)
	}
	box.Draw(func(pnt *ui.Painter, rr ui.Rect) {
		sx := rr.W / 300
		// 0 刻度线固定在容器底部上方 4px 处，与左侧进度胶囊的中线对齐；
		// 顶部留出刻度标签半高，使标签完整落在容器内。
		pad := style.TextXS / 2
		sy := (rr.H - 4 - pad) / 80
		px := func(x float32) float32 { return rr.X + x*sx }
		py := func(y float32) float32 { return rr.Y + pad + (y-6)*sy }
		m := 20.0
		for _, v := range vals {
			if v/1e6 > m {
				m = v / 1e6 * 1.15
			}
		}
		for _, v := range []float64{0, m / 2, m} {
			y := 86 - v/m*80
			pnt.Line(px(26), py(float32(y)), px(296), py(float32(y)), 1, p.bd)
			// Painter.Text 以左上角定位：把标签垂直居中到网格线上，
			// 字号用图表标注令牌，不随容器缩放。
			pnt.Text(px(0), py(float32(y))-style.TextXS/2, strconv.FormatFloat(v, 'f', 0, 64), style.TextXS, p.mu2)
		}
		var line, area ui.Path
		for i, v := range vals {
			x := px(26 + float32(i)*270/float32(len(vals)-1))
			y := py(float32(6 + 80 - v/1e6/m*80))
			if i == 0 {
				line.MoveTo(x, y)
				area.MoveTo(x, py(86)).LineTo(x, y)
			} else {
				line.LineTo(x, y)
				area.LineTo(x, y)
			}
		}
		area.LineTo(px(296), py(86))
		area.Close()
		pnt.FillPath(&area, p.pri.Alpha(0.08))
		pnt.StrokePath(&line, 1.5*sx, p.pri)
	})
}

func (pg *mainPage) kvList(c *ui.Context, p *d1Palette, rows [][2]string) {
	ui.Column(c).Gap(6).Children(func() {
		for _, kv := range rows {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr(kv[0])).Width(70).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
				ui.Text(c, kv[1]).Selectable().Tooltip(kv[1]).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
			})
		}
	})
}

func (pg *mainPage) taskKV(r d1Row) [][2]string {
	save := pg.saveDir
	if r.pending && r.req.Directory != "" {
		save = r.req.Directory
	}
	if !r.pending {
		if r.snap.FilePath != "" {
			save = filepath.Dir(r.snap.FilePath)
		} else if req, ok := pg.mgr.Request(r.id); ok && req.Directory != "" {
			save = req.Directory
		}
	}
	scheme := "HTTPS"
	authProtocol := r.snap.AuthProtocol
	if authProtocol == "" {
		authProtocol = tr("尚未使用集成认证")
	}
	if u, err := url.Parse(r.rowURL()); err == nil && u.Scheme != "" {
		scheme = strings.ToUpper(u.Scheme)
	}
	mediaMode, mediaQuality, audioLanguage := r.req.MediaMode, r.req.MediaQuality, r.req.AudioLanguage
	if !r.pending {
		if req, ok := pg.mgr.Request(r.id); ok {
			mediaMode, mediaQuality, audioLanguage = req.MediaMode, req.MediaQuality, req.AudioLanguage
		}
	}
	rows := [][2]string{
		{"地址", r.rowURL()},
		{"保存到", save},
		{"协议", scheme},
		{"最近认证", authProtocol},
		{"断点续传", rangeLabel(r)},
		{"校验", pg.hashText(r)},
		{"数字签名", signatureLabel(r)},
		{"添加时间", pg.rowAdded(r)},
	}
	if !r.pending && r.snap.State == download.Done && pg.fileMissing[r.id] {
		rows = append(rows, [2]string{"文件状态", tr("已失效（本地文件不存在）")})
	}
	if !r.pending && r.snap.Err != "" {
		rows = append(rows, [2]string{"错误信息", r.snap.Err})
	}
	if mediaMode != "" {
		rows = append(rows, [2]string{"媒体模式", mediaModeLabel(mediaMode)})
	}
	if mediaQuality != "" {
		rows = append(rows, [2]string{"画质上限", "≤ " + mediaQuality + "p"})
	}
	if audioLanguage != "" {
		rows = append(rows, [2]string{"音轨语言", audioLanguage})
	}
	return rows
}

// mediaModeLabel 返回媒体任务的显示名。
func mediaModeLabel(mode string) string {
	switch mode {
	case "hls-ts":
		return tr("HLS 媒体流")
	case "dash-fmp4":
		return tr("DASH 媒体流")
	default:
		return mode
	}
}

func (pg *mainPage) headersTab(c *ui.Context, p *d1Palette, r d1Row) {
	req := r.req
	if !r.pending {
		if original, ok := pg.mgr.Request(r.id); ok {
			req = original
		}
	}
	rows := [][2]string{{"地址", r.rowURL()}, {"来源页", requestPage(r)}, {"说明", tr("任务配置头；非网络抓包")}}
	keys := make([]string, 0, len(req.Headers))
	for key := range req.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := req.Headers[key]
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie":
			value = tr("已配置（隐藏）")
		}
		rows = append(rows, [2]string{key, value})
	}
	if len(keys) == 0 {
		rows = append(rows, [2]string{"请求头", tr("未配置额外请求头")})
	}
	ui.Column(c).Padding(14).Children(func() { pg.h4(c, p, "请求头", ""); pg.kvList(c, p, rows) })
}

func (pg *mainPage) logTab(c *ui.Context, p *d1Palette, r d1Row) {
	pg.histMu.Lock()
	lines := append([]string(nil), pg.logs[r.id]...)
	pg.histMu.Unlock()
	if len(lines) == 0 {
		lines = []string{"暂无日志"}
	}
	ui.Column(c).Padding(14).Gap(6).Children(func() {
		ui.Row(c).Margin(0, 0, 8, 0).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr("任务日志")).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
			copyLog := d1IconButton(c, p, "copy").Size(22, 22)
			if copyLog.Clicked() {
				text := strings.Join(lines, "\n")
				if pg.svc != nil {
					pg.clipLast = text
					pg.svc.WriteClipboard(text)
					pg.flash(tr("已复制日志"))
				}
			}
		})
		for _, l := range lines {
			ui.Text(c, tr(l)).FontSize(style.TextXS).TextColor(p.mu).LineHeight(1.5).Selectable()
		}
	})
}

func (pg *mainPage) rowDone(r d1Row) string {
	if r.pending {
		return "0 B"
	}
	return bytesText(r.snap.Done)
}

func (pg *mainPage) peakText(r d1Row) string {
	pg.histMu.Lock()
	defer pg.histMu.Unlock()
	var peak float64
	for _, sample := range pg.hist[r.id] {
		if time.Since(sample.at) <= 60*time.Second && sample.bytesPerSecond > peak {
			peak = sample.bytesPerSecond
		}
	}
	return bytesText(int64(peak)) + "/s"
}

// ── 状态栏 ──────────────────────────────────────────────────────────────

// d1StatusText 是状态栏右侧文字的统一样式：与「N 个排队」一致（12px/Muted）。
func d1StatusText(c *ui.Context, p *d1Palette, s string) ui.Element {
	return ui.Text(c, tr(s)).Shrink(0).SingleLine().FontSize(style.TextSM).
		FontWeight(style.WeightRegular).TextColor(p.mu)
}

func (pg *mainPage) statusBar(c *ui.Context, p *d1Palette) {
	var speed int64
	running, queued := 0, 0
	for _, r := range pg.allRows() {
		if r.pending {
			queued++
			continue
		}
		speed += r.snap.Speed
		switch r.snap.State {
		case download.Running:
			running++
		case download.Queued:
			queued++
		}
	}

	ui.Row(c).FillWidth().Height(28).Padding(0, 14).Gap(18).AlignItems(ui.Center).
		BorderWidth(1, 0, 0, 0).BorderColor(p.bd).Children(func() {
		// 左侧：磁盘空间与限速入口。
		pg.diskStatusControl(c, p)
		pg.limitStatusControl(c, p)
		ui.Row(c).Shrink(1).MinWidth(0).Gap(2).AlignItems(ui.Center).Children(func() {
			c.Key("status-directory-choose")
			choose := ui.ButtonBase(c).Size(24, 24).Shrink(0).Center().Radius(style.RadiusSM).
				Label(tr("选择保存文件夹")).Tooltip(tr("选择保存文件夹"))
			hoverMotion(choose, ui.Transparent, p.hover)
			choose.Children(func() { style.Icon(c, style.IconFolderOpen, 14, p.mu) })
			choose.OnClick(func() {
				path, cancelled := chooseFolder(pg.win, tr("选择保存文件夹"), pg.saveDir)
				if !cancelled && pg.setDefaultDirectory(path) {
					pg.refreshSettingsWindow()
					if pg.win != nil {
						pg.win.Invalidate()
					}
				}
			})
			c.Key("status-directory-open")
			pathButton := ui.ButtonBase(c).Height(24).Shrink(1).MinWidth(0).Padding(0, 4).Radius(style.RadiusSM).
				Label(tr("打开下载文件夹")).Tooltip(pg.saveDir)
			hoverMotion(pathButton, ui.Transparent, p.hover)
			pathButton.Children(func() {
				ui.Text(c, pg.saveDir).Shrink(1).MinWidth(0).SingleLine().FontSize(style.TextSM).TextColor(p.mu)
			})
			pathButton.OnClick(func() { pg.openLocalPath(pg.saveDir) })
		})
		// 夜间队列开始时间（点击打开计划任务）；关闭时不显示。
		if pg.nightOn {
			nightText := tr("夜间队列 ") + pg.nightAt + tr(" 开始")
			night := ui.ButtonBase(c).Height(24).Padding(0, 4).Radius(4).Shrink(1).MinWidth(0).Label(tr("打开计划任务"))
			night.Children(func() {
				ui.Text(c, tr(nightText)).Shrink(1).MinWidth(0).SingleLine().Tooltip(tr(nightText)).FontSize(style.TextSM).TextColor(p.mu)
			})
			if night.Clicked() {
				pg.openSchedule()
			}
		}
		// 短时状态提示居中，其余空间推到右侧。
		if pg.status != "" && time.Since(pg.statusAt) < 5*time.Second {
			ui.Text(c, tr(pg.status)).MarginStart(ui.Auto).Shrink(1).MinWidth(0).Role(ui.RoleStatus).FontSize(style.TextSM).FontWeight(style.WeightMedium).TextColor(p.fg).SingleLine().Tooltip(tr(pg.status))
		}
		// 右侧：下载/排队数量、下载速度（文字与「N 个排队」同款字型）。
		ui.Row(c).MarginStart(ui.Auto).Shrink(0).Gap(12).AlignItems(ui.Center).Children(func() {
			d1StatusText(c, p, fmt.Sprintf(tr("%d 个下载中"), running))
			d1StatusText(c, p, fmt.Sprintf(tr("%d 个排队"), queued))
			ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
				style.Icon(c, style.IconDownload, 14, p.mu).Shrink(0)
				speedText := bytesText(speed) + "/s"
				d1StatusText(c, p, speedText).Tooltip(tr(speedText))
			})
		})
	})
}

// ── 对话框（shadcn nova 风格） ──────────────────────────────────────────

// addDialog restores the original compact header/body/footer composition.
// Editable fields and validation remain; optional fields expand only on demand.
func (pg *mainPage) addDialog(c *ui.Context, p *d1Palette) {
	if pg.dlgLinks != pg.dlgNameSource {
		if pg.dlgName == "" || pg.dlgName == pg.dlgSuggestedName {
			pg.dlgName = ""
			if links := d1ParseLinks(pg.dlgLinks); len(links) == 1 {
				if name := pathrec.FromURLPath(links[0]); name != "" {
					pg.dlgName = pathrec.SanitizeFilename(name)
				}
			}
			pg.dlgSuggestedName = pg.dlgName
		}
		pg.dlgNameSource = pg.dlgLinks
	}
	_, h := c.Size()
	ui.Column(c).Fill().Background(p.bg).Children(func() {
		ui.Column(c).Padding(16, 18, 4, 18).Children(func() {
			ui.Text(c, map[bool]string{false: tr("添加下载"), true: tr("从站点提取下载链接")}[pg.dlgExtract]).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
			ui.Text(c, tr("每行一个链接，支持 HTTP、HTTPS、FTP、FTPS；保存目录与连接数作用于本批任务")).FontSize(style.TextSM).TextColor(p.mu).Margin(2, 0, 0, 0)
		})
		ui.Scroll(c).FillWidth().Grow(1).Shrink(1).Basis(0).MaxHeight(h).Children(func() {
			ui.Column(c).FillWidth().Padding(10, 18, 4, 18).Gap(12).Children(func() {
				ui.Column(c).Children(func() {
					c.Key("d1links")
					linksField := ui.TextArea(c, &pg.dlgLinks).Height(95).Padding(8, 10).FontSize(style.TextSM).LineHeight(1.6).Label(tr("下载链接")).FocusBind(&pg.focus, "d1links").Background(p.card).Border(1, p.bd).Radius(6)
					if linksField.Focused() {
						linksField.BorderColor(p.pri)
					}
					ui.Row(c).FillWidth().Margin(6, 0, 0, 0).Justify(ui.End).AlignItems(ui.Center).Children(func() {
						c.Key("download-source-disclosure")
						source := ui.ButtonBase(c).Radius(4).Label(tr("编辑来源页")).Expanded(pg.dlgShowPage || pg.dlgExtract)
						source.Children(func() {
							label := "来源页可选"
							if strings.TrimSpace(pg.dlgPage) != "" {
								label = "来源页已填写"
							}
							ui.Text(c, tr(label)).FontSize(style.TextSM).TextColor(p.mu)
						})
						source.OnClick(func() { pg.dlgShowPage = !pg.dlgShowPage })
					})
				})
				if !pg.dlgExtract {
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.End).Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
							d1Input(c, p, "文件名（可选，仅单个链接）", "download-filename", &pg.dlgName, false)
						})
						ui.Row(c).Shrink(0).Height(30).AlignItems(ui.Center).Children(func() {
							d1Switch(c, p, "保持原文件后缀", "", &pg.dlgKeepExtension)
						})
					})
				}
				if pg.dlgShowPage || pg.dlgExtract {
					ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
						d1Input(c, p, "来源页（可选）", "download-page", &pg.dlgPage, false)
						if pg.dlgExtract {
							d1Button(c, p, map[bool]string{false: "提取链接", true: "提取中…"}[pg.extracting], "globe").Disabled(pg.extracting).OnClick(pg.extractPage)
						}
					})
					if pg.dlgExtract {
						ui.Row(c).Gap(12).Children(func() {
							d1Choice(c, p, "抓取深度", &pg.dlgGrabDepth, []d1Option{{value: "0", label: "仅本页"}, {value: "1", label: "跟进 1 层"}, {value: "2", label: "跟进 2 层"}, {value: "3", label: "跟进 3 层"}}).Width(150).Shrink(0)
							d1Choice(c, p, "文件类型", &pg.dlgGrabType, []d1Option{{value: "", label: "全部类型"}, {value: "video", label: "视频"}, {value: "music", label: "音乐"}, {value: "doc", label: "文档"}, {value: "archive", label: "压缩包"}, {value: "program", label: "程序"}}).Width(150).Shrink(0)
						})
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							options := []d1Option{{value: "", label: "未选择项目"}}
							for _, project := range pg.grabProjectCache {
								options = append(options, d1Option{value: project.Name, label: project.Name})
							}
							d1Choice(c, p, "项目", &pg.dlgGrabProject, options).Width(130).Shrink(0).OnChange(pg.loadGrabProject)
							d1Choice(c, p, "周期", &pg.dlgGrabInterval, []d1Option{{value: "", label: "仅手动"}, {value: "60", label: "每小时"}, {value: "360", label: "每 6 小时"}, {value: "1440", label: "每天"}}).Width(110).Shrink(0)
							d1Button(c, p, "保存项目", "").OnClick(pg.saveGrabProject)
							d1Button(c, p, "删除", "trash").OnClick(pg.removeGrabProject)
						})
						ui.Text(c, tr("提取静态文件与媒体链接；抓取只跟进同一站点的页面，动态资源可使用浏览器扩展嗅探")).FontSize(style.TextXS).TextColor(p.mu)
					}
				}
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					ui.Column(c).FillWidth().Gap(5).Children(func() {
						ui.Text(c, tr("保存到")).FontSize(style.TextSM).TextColor(p.mu)
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							dirFocused := false
							dirBox := ui.Row(c).Height(32).Padding(0, 10).Gap(8).AlignItems(ui.Center).Radius(6).Border(1, p.bd).Background(p.card).Grow(1).Shrink(1).Basis(0).MinWidth(0)
							dirBox.Children(func() {
								d1Icon(c, "folder", 16, p.mu)
								c.Key("download-dir")
								dirInput := ui.TextInputBase(c, &pg.dlgDir).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg).Label(tr("保存到")).Tooltip(pg.dlgDir)
								dirFocused = dirInput.Focused()
								c.Key("download-dir-history")
								historyBtn := ui.ButtonBase(c).Size(24, 24).Shrink(0).Radius(style.RadiusSM).Center().Role(ui.RoleMenuButton).Label(tr("最近目录")).Tooltip(tr("最近目录")).Expanded(pg.showDirHistory)
								hoverMotion(historyBtn, ui.Transparent, p.hover)
								historyBtn.Children(func() { d1Icon(c, "chevron-down", 14, p.mu) })
								historyBtn.OnClick(func() { pg.showDirHistory = !pg.showDirHistory }).OnShortcut(0, ui.KeyDown, func() { pg.showDirHistory = true })
							})
							if dirFocused {
								dirBox.BorderColor(p.pri)
							}
							ui.PopoverBase(c, dirBox, &pg.showDirHistory, func(panel ui.Element) {
								d1DropdownPanel(panel, p).Width(dirBox.Bounds().W).AttachTo(dirBox, ui.AnchorBottomStart, ui.AnchorTopStart).
									Role(ui.RoleMenu).Label(tr("最近目录")).FocusGroup(ui.Vertical).Children(func() {
									for _, dir := range pg.recentDirs() {
										d := dir
										d1DropdownItem(c, p, d, "folder", true).OnClick(func() {
											pg.dlgDir = d
											pg.showDirHistory = false
										})
									}
								})
							})
							c.Key("download-dir-browse")
							browse := ui.ButtonBase(c).Height(32).Shrink(0).Padding(0, 8).Radius(6).Border(1, p.bd).Background(p.card).Label(tr("选择文件夹")).Tooltip(tr("选择文件夹"))
							hoverMotion(browse, ui.Transparent, p.hover)
							browse.Children(func() {
								ui.Text(c, tr("选择文件夹")).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
							})
							browse.OnClick(func() { pg.chooseDownloadDir() })
						})
					})
				})
				ui.Grid(c).FillWidth().Columns(2).Gap(12).Children(func() {
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, tr("队列")).FontSize(style.TextSM).TextColor(p.mu)
						c.Key("download-queue")
						queue := ui.SelectBase(c, &pg.dlgQueue)
						queue.Trigger.FillWidth().Height(32).Padding(0, 10).Gap(8).Radius(6).Border(1, p.bd).Background(p.card).Label(tr("队列")).Children(func() {
							d1Icon(c, "layers", 16, p.mu)
							ui.Text(c, pg.queueLabel()).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg)
							d1Icon(c, "chev", 16, p.mu)
						})
						queue.Popup(func(menu ui.Element) {
							d1DropdownPanel(menu, p).Width(queue.Trigger.Bounds().W).Children(func() {
								for i, choice := range pg.queueChoices() {
									item := queue.Item(i).FillWidth().Padding(6, 8).Radius(5)
									if item.Highlighted() {
										item.Background(p.hover)
									}
									label := choice.label
									item.Children(func() {
										ui.Text(c, label).Shrink(1).MinWidth(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(label)
									})
								}
							})
						})
					})
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, tr("媒体下载")).FontSize(style.TextSM).TextColor(p.mu)
						c.Key("download-media")
						media := ui.SelectBase(c, &pg.dlgMedia)
						media.Trigger.FillWidth().Height(32).Padding(0, 10).Gap(8).Radius(6).Border(1, p.bd).Background(p.card).Label(tr("媒体下载")).Children(func() {
							d1Icon(c, "film", 16, p.mu)
							ui.Text(c, mediaChoices()[pg.dlgMedia]).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg)
							d1Icon(c, "chev", 16, p.mu)
						})
						media.Popup(func(menu ui.Element) {
							d1DropdownPanel(menu, p).Width(media.Trigger.Bounds().W).Children(func() {
								for i, label := range mediaChoices() {
									item := media.Item(i).FillWidth().Padding(6, 8).Radius(5)
									if item.Highlighted() {
										item.Background(p.hover)
									}
									name := label
									item.Children(func() {
										ui.Text(c, name).Shrink(1).MinWidth(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(name)
									})
								}
							})
						})
					})
				})
				if pg.dlgMedia != 0 {
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, tr("画质上限")).FontSize(style.TextSM).TextColor(p.mu)
						c.Key("download-quality")
						quality := ui.SelectBase(c, &pg.dlgQuality)
						quality.Trigger.FillWidth().Height(32).Padding(0, 10).Gap(8).Radius(6).Border(1, p.bd).Background(p.card).Label(tr("画质上限")).Children(func() {
							d1Icon(c, "film", 16, p.mu)
							ui.Text(c, mediaQualities()[pg.dlgQuality]).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg)
							d1Icon(c, "chev", 16, p.mu)
						})
						quality.Popup(func(menu ui.Element) {
							d1DropdownPanel(menu, p).Width(quality.Trigger.Bounds().W).Children(func() {
								for i, label := range mediaQualities() {
									item := quality.Item(i).FillWidth().Padding(6, 8).Radius(5)
									if item.Highlighted() {
										item.Background(p.hover)
									}
									name := label
									item.Children(func() {
										ui.Text(c, name).Shrink(1).MinWidth(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(name)
									})
								}
							})
						})
					})
				}
				ui.Column(c).FillWidth().Children(func() {
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, tr("每个任务的连接数")).FontSize(style.TextSM).TextColor(p.mu)
						ui.RadioGroup(c, func() {
							segments := ui.Row(c).FillWidth().Padding(2).Gap(2).Radius(6).Border(1, p.bd).Background(p.card)
							position := segmentedSlide(segments, pg.dlgSeg, len(connectionOptions), 2, 2, 5, p.pri)
							segments.Children(func() {
								for i, count := range connectionOptions {
									label := strconv.Itoa(count)
									if count == -1 {
										label = "自动"
									}
									c.Key(label)
									segment := ui.RadioBase(c, &pg.dlgSeg, i).Grow(1).Shrink(1).Basis(0).Padding(4, 0).Radius(5).Center().Label(fmt.Sprintf(tr("%s 个连接"), label))
									weight := max(float32(0), 1-float32(math.Abs(float64(position.x-float32(i)))))
									col := p.mu.Mix(p.onpri, weight)
									segment.Children(func() { ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(col) })
								}
							})
						})
					})
				})
				ui.Row(c).Gap(20).Wrap().Children(func() {
					ui.Row(c).Disabled(pg.queueIsNight()).Children(func() { d1Check(c, p, &pg.dlgNow, "立即开始") })
					d1Check(c, p, &pg.dlgOpen, "完成后打开文件夹")
					d1Check(c, p, &pg.dlgMirror, "按来源页保留目录")
					ui.Row(c).Tooltip(tr("HTTP 基本认证")).Children(func() { d1Check(c, p, &pg.dlgLogin, "需要登录") })
				})
				ui.Row(c).Gap(20).Wrap().Children(func() {
					d1Check(c, p, &pg.dlgVerifySHA, "校验 SHA-256")
					if signature.Available {
						d1Check(c, p, &pg.dlgVerifySignature, "验证文件数字签名")
					}
				})
				if pg.dlgVerifySHA {
					d1Input(c, p, "SHA-256", "download-sha256", &pg.dlgSHA256, false)
				}
				if pg.queueIsNight() {
					ui.Text(c, fmt.Sprintf(tr("夜间队列将在 %s 启动；不会立即下载"), pg.nightAt)).FontSize(style.TextSM).TextColor(p.mu)
				}
				if pg.dlgLogin {
					ui.Grid(c).FillWidth().Columns(2).Gap(12).Children(func() {
						d1Input(c, p, "用户名", "download-user", &pg.dlgUser, false)
						d1Input(c, p, "密码", "download-password", &pg.dlgPassword, true)
					})
				}
				if pg.dlgError != "" {
					ui.Text(c, tr(pg.dlgError)).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
				}
			})
		})
		ui.Row(c).FillWidth().Padding(14, 18).Gap(8).Justify(ui.End).AlignItems(ui.Center).Margin(14, 0, 0, 0).Background(p.card).BorderWidth(1, 0, 0, 0).BorderColor(p.bd).Children(func() {
			d1Button(c, p, "取消", "").OnClick(pg.closeAddWindow)
			if !pg.queueIsNight() {
				d1Button(c, p, "加入队列", "").OnClick(func() { pg.submitDownloads(false) })
			}
			label := "开始下载"
			if pg.queueIsNight() {
				label = "加入夜间队列"
			} else if !pg.dlgNow {
				label = "保存任务"
			}
			d1PrimaryButton(c, p, label, "down").OnClick(func() { pg.submitDownloads(pg.dlgNow) })
		})
	})
}

func d1Check(c *ui.Context, p *d1Palette, on *bool, label string) ui.Element {
	box := ui.CheckboxBase(c, on).Gap(8).Justify(ui.Start).AlignItems(ui.Center).Radius(5).Label(tr(label))
	box.Children(func() {
		d1CheckMark(c, p, *on)
		ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(p.fg)
	})
	return box
}

func (pg *mainPage) schedDialog(c *ui.Context, p *d1Palette) {
	ui.DialogBase(c, &pg.showSched, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, .42))
		panel.Width(420).Padding(18).Radius(8).Border(1, p.bd2).Background(p.bg).Label(tr("计划任务")).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, .14))
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, tr("计划任务")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
			ui.Text(c, tr("夜间队列按本机时间启动，主队列等待任务不受影响")).FontSize(style.TextSM).TextColor(p.mu)
			d1Check(c, p, &pg.dlgScheduleOn, "启用夜间队列")
			d1Input(c, p, "开始时间（24 小时制 HH:mm）", "d1night", &pg.dlgScheduleAt, false)
			d1Input(c, p, "停止时间（可选 HH:mm）", "d1nightstop", &pg.dlgScheduleStop, false)
			ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr("重复日期")).FontSize(style.TextSM).TextColor(p.mu).Shrink(0)
				for i, label := range []string{"日", "一", "二", "三", "四", "五", "六"} {
					d1Check(c, p, &pg.dlgScheduleDays[i], label)
				}
			})
			if pg.scheduleError != "" {
				ui.Text(c, tr(pg.scheduleError)).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				d1Button(c, p, "取消", "").OnClick(func() { pg.showSched = false })
				d1PrimaryButton(c, p, "保存", "").OnClick(func() {
					parsed, err := time.Parse("15:04", strings.TrimSpace(pg.dlgScheduleAt))
					if err != nil {
						pg.scheduleError = tr("请输入有效时间，例如 01:00")
						return
					}
					stop := ""
					if value := strings.TrimSpace(pg.dlgScheduleStop); value != "" {
						parsedStop, err := time.Parse("15:04", value)
						if err != nil {
							pg.scheduleError = tr("请输入有效的停止时间，例如 07:00")
							return
						}
						stop = parsedStop.Format("15:04")
					}
					days, ok := pg.selectedScheduleDays()
					if !ok {
						pg.scheduleError = tr("请至少选择一个重复日期")
						return
					}
					pg.nightAt = parsed.Format("15:04")
					pg.nightStop = stop
					pg.nightDays = days
					pg.nightOn = pg.dlgScheduleOn
					pg.saveAppPreferences()
					pg.showSched = false
					pg.flash(tr("计划任务已保存"))
				})
			})
		})
	})
}

// selectedScheduleDays 读取重复日期勾选；全部勾选等价于每天（空表），
// 一个都不勾视为无效。
func (pg *mainPage) selectedScheduleDays() ([]int, bool) {
	days := make([]int, 0, 7)
	for i, on := range pg.dlgScheduleDays {
		if on {
			days = append(days, i)
		}
	}
	if len(days) == 0 {
		return nil, false
	}
	return normalizeNightDays(days), true
}

// ── 动作 ────────────────────────────────────────────────────────────────

func d1ParseLinks(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Host == "" {
			continue
		}
		switch u.Scheme {
		case "http", "https", "ftp", "ftps":
		default:
			continue
		}
		out = append(out, line)
	}
	return out
}

// mediaChoices 是添加下载对话框的媒体下载方式，索引与 mediaModeIDs 对应。
func mediaChoices() []string {
	return []string{tr("普通下载"), mediaModeLabel("hls-ts"), mediaModeLabel("dash-fmp4")}
}

// mediaModeIDs 把对话框选择映射到请求的媒体模式。
func mediaModeIDs() []string { return []string{"", "hls-ts", "dash-fmp4"} }

// mediaQualities 是媒体任务的画质上限选项（自动 = 不限制），索引与
// mediaQualityIDs 对应。
func mediaQualities() []string {
	return []string{tr("自动"), "360p", "480p", "720p", "1080p", "1440p", "2160p"}
}

// mediaQualityIDs 把画质选择映射为分辨率上限值。
func mediaQualityIDs() []string { return []string{"", "360", "480", "720", "1080", "1440", "2160"} }

// queueChoice 是添加下载对话框里的一个队列选项。
type queueChoice struct{ queue, label string }

// queueChoices 返回队列选项：主队列、夜间队列、自定义队列。
func (pg *mainPage) queueChoices() []queueChoice {
	choices := []queueChoice{{"", tr("主队列")}, {"night", tr("夜间队列")}}
	for _, spec := range activeCustomQueues {
		choices = append(choices, queueChoice{spec.ID, spec.Name})
	}
	return choices
}

// queueID 返回对话框当前选择的队列标识。
func (pg *mainPage) queueID() string {
	choices := pg.queueChoices()
	if pg.dlgQueue < 0 || pg.dlgQueue >= len(choices) {
		return ""
	}
	return choices[pg.dlgQueue].queue
}

// queueLabel 返回对话框当前选择的队列名称。
func (pg *mainPage) queueLabel() string {
	choices := pg.queueChoices()
	if pg.dlgQueue < 0 || pg.dlgQueue >= len(choices) {
		return choices[0].label
	}
	return choices[pg.dlgQueue].label
}

// queueIsNight 报告对话框当前是否选择夜间队列。
func (pg *mainPage) queueIsNight() bool { return pg.queueID() == "night" }

func (pg *mainPage) addLinks(links []string, queue string) {
	if len(links) == 0 {
		pg.flash(tr("没有可添加的链接"))
		return
	}
	first := ""
	for _, link := range links {
		req := pg.downloadRequest(link)
		req.Queue = queue
		if queue == "night" {
			id := pg.enqueueNight(req)
			if first == "" {
				first = id
			}
			pg.histMu.Lock()
			pg.logs[id] = append(pg.logs[id], time.Now().Format("15:04:05")+tr("  已加入")+tr("夜间队列"))
			pg.histMu.Unlock()
			continue
		}
		var id string
		if pg.dlgNow {
			id = pg.mgr.Add(req)
		} else {
			id = pg.mgr.AddPaused(req)
		}
		if first == "" {
			first = id
		}
		label := "主队列"
		if isCustomQueue(queue) {
			label = customQueueName(queue)
		}
		pg.histMu.Lock()
		pg.logs[id] = append(pg.logs[id], time.Now().Format("15:04:05")+tr("  已加入")+label)
		pg.histMu.Unlock()
	}
	// 提交后定位首个新任务，结果一眼可见。
	if first != "" {
		pg.sel = first
	}
	pg.flash(fmt.Sprintf(tr("已添加 %d 个任务"), len(links)))
}

func (pg *mainPage) toggleRow(r d1Row) {
	if r.pending {
		if r.snap.ID != "" {
			if !pg.mgr.MoveToMain(r.id, true) {
				if r.snap.Err != "" {
					pg.flash(r.snap.Err)
				} else {
					pg.flash(tr("任务正在暂停，请稍后重试"))
				}
			}
			return
		}
		if req, ok := pg.takeNight(r.id); ok {
			id := pg.mgr.Add(req)
			pg.sel = id
			delete(pg.checked, r.id)
		}
		return
	}
	if r.snap.Held {
		if !pg.mgr.Resume(r.id) && r.snap.Err != "" {
			pg.flash(r.snap.Err)
		}
		return
	}

	switch r.snap.State {
	case download.Running, download.Queued:
		pg.mgr.Cancel(r.id)
		pg.flash(tr("已暂停：") + r.rowName())
	case download.Done:
		pg.openRowFile(r)
	default:
		if !pg.mgr.Resume(r.id) && r.snap.Err != "" {
			pg.flash(r.snap.Err)
		}
	}
}

func (pg *mainPage) redownload(r d1Row) {
	var id string
	if r.snap.ID != "" {
		var err error
		id, err = pg.mgr.Redownload(r.id)
		if err != nil {
			pg.flash(err.Error())
			return
		}
	} else if r.pending {
		req, ok := pg.takeNight(r.id)
		if !ok {
			return
		}
		req.Queue = ""
		id = pg.mgr.Add(req)
	} else {
		return
	}
	pg.histMu.Lock()
	pg.logs[id] = append(pg.logs[id], time.Now().Format("15:04:05")+tr("  重新下载"))
	pg.histMu.Unlock()
	if pg.checked[r.id] {
		delete(pg.checked, r.id)
		pg.checked[id] = true
	}
	pg.sel = id
}

// redownloadSelection 批量重新下载所选任务：逐个重建，失败的静默跳过（单行
// 重试保留逐条报错），结果合并为一条提示并定位首个新建任务。
func (pg *mainPage) redownloadSelection() {
	if pg.mgr == nil {
		return
	}
	first := ""
	count := 0
	for _, r := range pg.selection() {
		id := ""
		if r.snap.ID != "" {
			newID, err := pg.mgr.Redownload(r.id)
			if err != nil {
				continue
			}
			id = newID
			if pg.checked[r.id] {
				delete(pg.checked, r.id)
				pg.checked[id] = true
			}
		} else if r.pending {
			req, ok := pg.takeNight(r.id)
			if !ok {
				continue
			}
			req.Queue = ""
			id = pg.mgr.Add(req)
			delete(pg.checked, r.id)
		}
		if id == "" {
			continue
		}
		count++
		if first == "" {
			first = id
		}
		pg.histMu.Lock()
		pg.logs[id] = append(pg.logs[id], time.Now().Format("15:04:05")+tr("  重新下载"))
		pg.histMu.Unlock()
	}
	if count == 0 {
		pg.flash(tr("没有可重新下载的任务"))
		return
	}
	pg.sel = first
	pg.flash(fmt.Sprintf(tr("已重新下载 %d 个任务"), count))
}

// removeRow 删除一行任务；返回是否执行与完成文件是否保留在磁盘。提示由调用
// 方给出：单行用 removeRowPrompt，批量用 deleteSelection 合并。
func (pg *mainPage) removeRow(r d1Row) (bool, bool) {
	keptFile := false
	if r.pending {
		if r.snap.ID != "" {
			pg.mgr.Remove(r.id)
		} else {
			pg.takeNight(r.id)
		}
	} else {
		keptFile = r.snap.State == download.Done && r.snap.FilePath != ""
		pg.mgr.Cancel(r.id)
		pg.mgr.Remove(r.id)
	}
	delete(pg.checked, r.id)
	pg.histMu.Lock()
	delete(pg.hist, r.id)
	delete(pg.logs, r.id)
	delete(pg.logs, "sha:"+r.id)
	delete(pg.prev, r.id)
	pg.histMu.Unlock()
	if pg.sel == r.id {
		pg.sel = ""
	}
	return true, keptFile
}

// removeRowPrompt 单行删除并提示结果。
func (pg *mainPage) removeRowPrompt(r d1Row) {
	pg.requestDelete([]d1Row{r})
}

// deleteSelection 批量删除所选任务，提示合并为一条并注明保留的文件数。
func (pg *mainPage) deleteSelection() {
	pg.requestDelete(pg.selection())
}

func (pg *mainPage) pauseSelection() {
	for _, r := range pg.selection() {
		if !r.pending {
			pg.mgr.Cancel(r.id)
		}
	}
	pg.flash(tr("已暂停所选任务"))
}

func (pg *mainPage) resumeSelection() {
	var resumeError string
	for _, r := range pg.selection() {
		if r.pending {
			pg.toggleRow(r)
		} else if r.snap.Held {
			pg.mgr.Resume(r.id)
		} else if r.snap.State == download.Canceled || r.snap.State == download.ErrorS {
			if !pg.mgr.Resume(r.id) && r.snap.Err != "" {
				resumeError = r.snap.Err
			}
		}
	}
	if resumeError != "" {
		pg.flash(resumeError)
		return
	}
	pg.flash(tr("已继续所选任务"))
}

func (pg *mainPage) pauseAll() {
	if pg.mgr == nil {
		return
	}
	for _, s := range pg.mgr.Snapshots() {
		if !s.Night && (s.State == download.Running || s.State == download.Queued) {
			pg.mgr.Cancel(s.ID)
		}
	}
	pg.flash(tr("已停止全部任务"))
}

func (pg *mainPage) toggleLimit() {
	if pg.limitOn {
		pg.setLimit("0")
	} else {
		value := pg.limitValue
		if value == "" || value == "0" {
			value = "5"
		}
		pg.setLimit(value)
	}
}

// openRowFile 打开已完成任务的文件；本地文件已被清理时提示并不发起打开。
func (pg *mainPage) openRowFile(r d1Row) {
	if r.pending || r.snap.FilePath == "" {
		return
	}
	if rowFileMissing(r) {
		if pg.fileMissing == nil {
			pg.fileMissing = make(map[string]bool)
		}
		pg.fileMissing[r.id] = true
		pg.flash(tr("文件已失效，请重新下载"))
		return
	}
	pg.openLocalPath(r.snap.FilePath)
}

// rowFileMissing 判断已完成任务的本地文件是否已被清理；仅对已完成任务统计。
func rowFileMissing(r d1Row) bool {
	if r.pending || r.snap.State != download.Done || r.snap.FilePath == "" {
		return false
	}
	_, err := os.Stat(r.snap.FilePath)
	return err != nil
}

func (pg *mainPage) openRowFolder(r d1Row) {
	if r.pending {
		dir := r.req.Directory
		if dir == "" {
			dir = pg.saveDir
		}
		pg.openLocalPath(dir)
		return
	}
	if r.snap.FilePath == "" && pg.mgr != nil {
		if req, ok := pg.mgr.Request(r.id); ok && req.Directory != "" {
			pg.openLocalPath(req.Directory)
			return
		}
	}
	pg.openFolder(r.snap)
}

func (pg *mainPage) openFolder(s download.Snapshot) {
	dir := pg.saveDir
	if s.FilePath != "" {
		dir = filepath.Dir(s.FilePath)
		// 能定位到文件时在文件管理器中选中它；平台不支持或文件缺失退回打开目录。
		if s.State == download.Done {
			if _, err := os.Stat(s.FilePath); err == nil {
				go mygo.Shell.ShowItemInFolder(s.FilePath)
				return
			}
		}
	}
	pg.openLocalPath(dir)
}

func (pg *mainPage) openLocalPath(path string) {
	if path == "" {
		return
	}
	win := pg.win
	go func() {
		if err := mygo.Shell.OpenPath(path); err != nil && win != nil {
			win.Update(func() { pg.flash(tr("无法打开文件或文件夹") + ": " + err.Error()) })
		}
	}()
}

// hashText 给出完成文件的 SHA-256 摘要（后台计算并缓存）。
func (pg *mainPage) hashText(r d1Row) string {
	if r.pending || r.snap.State != download.Done || r.snap.FilePath == "" {
		return tr("SHA-256 待校验")
	}
	if r.snap.VerifiedSHA256 != "" {
		return tr("下载时 SHA-256 校验通过：") + r.snap.VerifiedSHA256
	}
	pg.histMu.Lock()
	defer pg.histMu.Unlock()
	key := "sha:" + r.id
	if l, ok := pg.logs[key]; ok && len(l) > 0 {
		return l[0]
	}
	pg.logs[key] = []string{"计算中…"}
	path := r.snap.FilePath
	go func() {
		sha, md5 := hashFile(path)
		result := "SHA-256 " + sha
		if sha == "—" {
			result = "校验失败：无法读取文件"
		} else if md5 != "—" {
			result = "SHA-256 " + sha + "，MD5 " + md5
		}
		pg.histMu.Lock()
		if _, ok := pg.logs[key]; ok {
			pg.logs[key] = []string{result}
		}
		pg.histMu.Unlock()
		if pg.svc != nil {
			pg.svc.Invalidate()
		}
	}()
	return tr("计算中…")
}

func (pg *mainPage) flash(msg string) {
	pg.status = msg
	pg.statusAt = time.Now()
	if pg.svc != nil {
		pg.svc.Invalidate()
	}
}

// ── 后台监听：剪贴板与夜间队列 ──────────────────────────────────────────

// Clipboard reads and writes are performed on the window update thread.
// Remember our own write so clipboard monitoring does not create a duplicate.
func (pg *mainPage) copyDownloadURL(address string) {
	if pg.svc == nil {
		return
	}
	pg.clipLast = address
	pg.svc.WriteClipboard(address)
	pg.flash(tr("已复制下载链接"))
}

func (pg *mainPage) clipboardWatch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-pg.stop:
			return
		case <-t.C:
		}
		pg.win.Update(func() {
			if !pg.clipOn || pg.svc == nil {
				return
			}
			txt := pg.svc.ReadClipboard()
			if txt == "" || txt == pg.clipLast {
				return
			}
			pg.clipLast = txt
			links := d1ParseLinks(txt)
			if len(links) == 0 || !clipboardIsLinks(txt, links) {
				return
			}
			// 克制：进入添加下载对话框由用户确认，不自动开下；对话框打开时
			// 不覆盖正在填写的内容，同一份剪贴板内容只提示一次。
			if pg.showAdd {
				return
			}
			pg.dlgLinks = strings.Join(links, "\n")
			pg.openAdd(false)
		})
	}
}

// recentDirs 给出最近使用的保存目录：默认目录在前，其后按任务由新到旧去重，
// 上限六个，只做提示不自动改写输入框。
func (pg *mainPage) recentDirs() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 6)
	if dir := strings.TrimSpace(pg.dlgDir); dir != "" {
		out = append(out, dir)
		seen[dir] = true
	}
	if pg.saveDir != "" && !seen[pg.saveDir] {
		out = append(out, pg.saveDir)
		seen[pg.saveDir] = true
	}
	snapshots := pg.mgr.Snapshots()
	for i := len(snapshots) - 1; i >= 0 && len(out) < 6; i-- {
		file := snapshots[i].FilePath
		if file == "" {
			continue
		}
		dir := filepath.Dir(file)
		if dir == "." || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// duplicateDialogLinks 统计对话框链接中已存在于任务列表（含夜间等待）的
// 条数，仅作提示不拦截提交。
func (pg *mainPage) duplicateDialogLinks(links []string) int {
	if len(links) == 0 {
		return 0
	}
	existing := make(map[string]bool, 64)
	for _, r := range pg.allRows() {
		existing[r.rowURL()] = true
	}
	count := 0
	for _, link := range links {
		if existing[link] {
			count++
		}
	}
	return count
}

// fileStateWatch 周期核对已完成任务的本地文件，被清理的任务在列表与详情
// 标注失效。文件检查在后台执行，结果只在界面线程更新。
func (pg *mainPage) fileStateWatch() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		checked := map[string]string{}
		missing := map[string]bool{}
		for _, snapshot := range pg.mgr.Snapshots() {
			if snapshot.State == download.Done && snapshot.FilePath != "" {
				checked[snapshot.ID] = snapshot.FilePath
				if _, err := os.Stat(snapshot.FilePath); err != nil {
					missing[snapshot.ID] = true
				}
			}
		}
		pg.win.Update(func() {
			// Ignore stale results if a task was removed or its path changed.
			current := map[string]bool{}
			for _, snapshot := range pg.mgr.Snapshots() {
				if snapshot.State == download.Done && checked[snapshot.ID] == snapshot.FilePath && missing[snapshot.ID] {
					current[snapshot.ID] = true
				}
			}
			pg.fileMissing = current
		})
		select {
		case <-pg.stop:
			return
		case <-t.C:
		}
	}
}

// clipboardIsLinks 仅当剪贴板的非空行都是已解析的链接时返回 true：正文夹带
// 链接的复制不应触发下载提示，行数上限避免整份链接清单刷屏。
func clipboardIsLinks(text string, links []string) bool {
	set := make(map[string]bool, len(links))
	for _, link := range links {
		set[link] = true
	}
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		if lines > 20 || !set[line] {
			return false
		}
	}
	return lines > 0
}

// runNightStop 到达停止时间时暂停运行中的任务并记住它们；下次开始时间
// 自动恢复，停用计划时立即恢复。手动暂停的任务不在此列。
func (pg *mainPage) runNightStop(now time.Time) {
	if pg.nightStop == "" {
		pg.stopChecked = time.Time{}
		pg.stopCheckedAt = ""
		return
	}
	if pg.stopCheckedAt != pg.nightStop {
		pg.stopChecked = time.Time{}
		pg.stopCheckedAt = pg.nightStop
	}
	_, crossed := nightDue(pg.stopChecked, now, pg.nightStop)
	pg.stopChecked = now
	if !crossed {
		return
	}
	paused := 0
	for _, snapshot := range pg.mgr.Snapshots() {
		if snapshot.State != download.Running {
			continue
		}
		pg.mgr.Cancel(snapshot.ID)
		pg.schedulePaused = append(pg.schedulePaused, snapshot.ID)
		paused++
	}
	if paused > 0 {
		pg.flash(tr("已到停止时间，暂停 ") + strconv.Itoa(paused) + tr(" 个任务"))
	}
}

// resumeSchedulePaused 恢复被停止时间暂停的任务。
func (pg *mainPage) resumeSchedulePaused() {
	if len(pg.schedulePaused) == 0 {
		return
	}
	for _, id := range pg.schedulePaused {
		pg.mgr.Resume(id)
	}
	pg.schedulePaused = nil
}

func (pg *mainPage) nightWatch() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-pg.stop:
			return
		case <-t.C:
		}
		pg.win.Update(func() {
			now := time.Now()
			if !pg.nightOn {
				pg.nightChecked = time.Time{}
				pg.nightCheckedAt = ""
				pg.nightAwaiting = nil
				pg.stopChecked = time.Time{}
				pg.stopCheckedAt = ""
				pg.resumeSchedulePaused()
				return
			}
			pg.runNightStop(now)
			if pg.nightCheckedAt != pg.nightAt {
				pg.nightChecked = time.Time{}
				pg.nightCheckedAt = pg.nightAt
				pg.nightAwaiting = nil
			}
			due, crossed := nightDue(pg.nightChecked, now, pg.nightAt)
			pg.nightChecked = now
			if crossed && !nightDaysAllow(pg.nightDays, now) {
				crossed = false
			}
			if crossed {
				pg.resumeSchedulePaused()
			}
			if !crossed && len(pg.nightAwaiting) == 0 {
				return
			}
			// Preserve the former whole-minute trigger window, but do not start
			// tasks added after that window when resuming from sleep.
			cutoff := due.Add(time.Minute)
			var pending []download.Request
			var ids []string
			if crossed {
				pg.nightMu.Lock()
				pg.ensureNightIDs()
				remaining, remainingIDs := pg.night[:0], pg.nightIDs[:0]
				for i, req := range pg.night {
					id := pg.nightIDs[i]
					if pg.nightCreated[id].Before(cutoff) {
						pending = append(pending, req)
						ids = append(ids, id)
						delete(pg.nightCreated, id)
					} else {
						remaining = append(remaining, req)
						remainingIDs = append(remainingIDs, id)
					}
				}
				pg.night, pg.nightIDs = remaining, remainingIDs
				pg.nightMu.Unlock()
			}
			started := 0
			seen := make(map[string]bool, len(pg.nightAwaiting))
			for _, snapshot := range pg.mgr.Snapshots() {
				entry, awaiting := pg.nightAwaiting[snapshot.ID]
				if awaiting {
					seen[snapshot.ID] = true
					if !snapshot.Night || !snapshot.NightSince.Equal(entry) || snapshot.State == download.ErrorS {
						delete(pg.nightAwaiting, snapshot.ID)
						continue
					}
				}
				eligible := crossed && snapshot.Night && !snapshot.NightSince.IsZero() && snapshot.NightSince.Before(cutoff)
				if !eligible && !awaiting {
					continue
				}
				if snapshot.Held {
					delete(pg.nightAwaiting, snapshot.ID)
					if pg.mgr.MoveToMain(snapshot.ID, true) {
						started++
					}
				} else if eligible && (snapshot.State == download.Running || snapshot.State == download.Queued) {
					if pg.nightAwaiting == nil {
						pg.nightAwaiting = make(map[string]time.Time)
					}
					pg.nightAwaiting[snapshot.ID] = snapshot.NightSince
					seen[snapshot.ID] = true
				}
			}
			for id := range pg.nightAwaiting {
				if !seen[id] {
					delete(pg.nightAwaiting, id)
				}
			}
			for i, req := range pending {
				pg.mgr.Add(req)
				delete(pg.checked, ids[i])
				started++
			}
			if started > 0 {
				pg.sel = ""
				pg.flash(fmt.Sprintf(tr("夜间队列已开始 %d 个任务"), started))
			}
		})
	}
}
