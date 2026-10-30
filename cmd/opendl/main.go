package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"openDL/api"
	"openDL/download"
	"openDL/i18n"
	"openDL/nativehost"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const appBrand = "OpenDL"

// app holds the dashboard state. The UI lives in dashboard.go.
type app struct {
	mgr     *download.Manager
	query   string
	input   string
	status  string
	saveDir string
	dark    bool

	showAdd    bool
	showDetail bool
	detailID   string
	checks     map[string]*bool
	focus      string

	list ui.ListState
	sel  int

	nav int // sidebar selection
	tab int // filter tab: 全部/视频/文档/压缩包/软件

	// Detail-panel options.
	seg                           int
	limitOn, shutdownOn, verifyOn bool
	limitSpeed, threads           float64

	// Speed history for the charts.
	histMu sync.Mutex
	hist   []float64

	hashMu sync.Mutex
	hashes map[string]fileHash
	svc    *ui.Services
}

type fileHash struct {
	sha, md5 string
	done     bool
}

func (a *app) view(c *ui.Context) {
	if a.svc == nil {
		s := c.Services()
		a.svc = &s
		a.sel = -1
		a.nav = 1 // 下载中
		a.threads = 16
		a.limitSpeed = 100
		a.list.Selected = &a.sel
		a.list.Sort = &ui.SortOrder{Column: "创建时间", Descending: true}
	}
	p := style.Dark()
	if !a.dark {
		p = style.Light()
	}
	c.SetTheme(p.Theme())
	if c.Shortcut(ui.Ctrl, ui.KeyK) {
		a.focus = "search"
	}
	w, _ := c.Size()
	cols := a.columnsFor(w)
	snaps := a.visible()

	ui.Column(c).Fill().Background(p.Background).Children(func() {
		a.topBar(c, p)
		ui.Row(c).Fill().Padding(16).Gap(16).Children(func() {
			a.sidebar(c, p)
			ui.Column(c).Fill().Gap(16).Children(func() {
				a.kpiRow(c, p, snaps)
				a.tabRow(c, p, snaps)
				a.tableCard(c, p, cols, snaps)
				a.queueCard(c, p, snaps)
				ui.Text(c, tr(a.status)).SingleLine().FontSize(11).TextColor(p.TextFaint).AlignSelf(ui.End)
			})
			a.detailPanel(c, p, snaps)
		})
	})
}

// ── selection helpers ────────────────────────────────────────────────────

func (a *app) checkbox(id string) *bool {
	if a.checks == nil {
		a.checks = map[string]*bool{}
	}
	if b, ok := a.checks[id]; ok {
		return b
	}
	b := new(bool)
	a.checks[id] = b
	return b
}

func (a *app) checked(id string) bool {
	b, ok := a.checks[id]
	return ok && *b
}

func (a *app) totalSpeed(snaps []download.Snapshot) int64 {
	var sum int64
	for _, s := range snaps {
		sum += s.Speed
	}
	return sum
}

// pushHistory records one speed sample for the charts.
func (a *app) pushHistory(v float64) {
	a.histMu.Lock()
	a.hist = append(a.hist, v)
	if len(a.hist) > 60 {
		a.hist = a.hist[len(a.hist)-60:]
	}
	a.histMu.Unlock()
}

// histValues returns up to the last n samples.
func (a *app) histValues(n int) []float64 {
	a.histMu.Lock()
	defer a.histMu.Unlock()
	if len(a.hist) > n {
		return append([]float64(nil), a.hist[len(a.hist)-n:]...)
	}
	return append([]float64(nil), a.hist...)
}

func (a *app) peakSpeed() int64 {
	a.histMu.Lock()
	defer a.histMu.Unlock()
	var peak float64
	for _, v := range a.hist {
		if v > peak {
			peak = v
		}
	}
	return int64(peak)
}

// ── filtering and ordering ───────────────────────────────────────────────

// visible narrows the rows by sidebar view, filter tab and search box, then
// orders them by the sorted column.
func (a *app) visible() []download.Snapshot {
	all := a.mgr.Snapshots()
	cat := ""
	if a.tab >= 0 && a.tab < len(tabDefs) {
		cat = tabDefs[a.tab].cat
	}
	q := strings.ToLower(strings.TrimSpace(a.query))
	snaps := all[:0:0]
	for _, s := range all {
		switch a.nav {
		case 2: // 已完成
			if s.State != download.Done {
				continue
			}
		case 3: // 已暂停
			if s.State != download.Canceled {
				continue
			}
		case 4, 6: // 计划任务 / 回收站: empty views
			continue
		}
		if cat != "" && !inCategory(cat, s) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Name), q) &&
			!strings.Contains(strings.ToLower(s.URL), q) {
			continue
		}
		snaps = append(snaps, s)
	}
	so := a.list.Sort
	if so == nil || so.Column == "" {
		return snaps
	}
	less := func(i, j int) bool {
		x, y := snaps[i], snaps[j]
		switch so.Column {
		case "文件名":
			return strings.ToLower(x.Name) < strings.ToLower(y.Name)
		case "大小":
			return x.Total < y.Total
		case "状态":
			return x.State < y.State
		case "速度":
			return x.Speed < y.Speed
		case "创建时间":
			return x.Created.Before(y.Created)
		}
		return false
	}
	sort.SliceStable(snaps, func(i, j int) bool {
		if so.Descending {
			return less(j, i)
		}
		return less(i, j)
	})
	return snaps
}

// ── commands ─────────────────────────────────────────────────────────────

func (a *app) command(item string) {
	snaps := a.visible()
	var s *download.Snapshot
	if a.sel >= 0 && a.sel < len(snaps) {
		s = &snaps[a.sel]
	}
	switch item {
	case "添加":
		a.enqueue(a.input)
		a.input = ""
	case "粘贴":
		raw := ""
		if a.svc != nil {
			raw = a.svc.ReadClipboard()
		}
		if strings.TrimSpace(raw) == "" {
			a.status = tr("剪贴板里没有链接")
			return
		}
		a.enqueue(raw)
	case "停止":
		if s == nil {
			a.status = tr("请先在列表里选中一个任务")
			return
		}
		a.mgr.Cancel(s.ID)
		a.status = tr("已暂停：") + s.Name
	case "停止全部":
		for _, x := range snaps {
			if x.State == download.Running || x.State == download.Queued {
				a.mgr.Cancel(x.ID)
			}
		}
		a.status = tr("已暂停全部任务")
	case "开始队列":
		n := 0
		for _, x := range snaps {
			if x.State == download.ErrorS || x.State == download.Canceled {
				a.mgr.Add(download.Request{URL: x.URL})
				n++
			}
		}
		a.status = fmt.Sprintf(tr("已继续 %d 个任务"), n)
	case "删除所选":
		n := 0
		for _, x := range snaps {
			if a.checked(x.ID) {
				a.mgr.Cancel(x.ID)
				a.mgr.Remove(x.ID)
				n++
			}
		}
		if n == 0 && s != nil {
			a.mgr.Cancel(s.ID)
			a.mgr.Remove(s.ID)
			n = 1
		}
		a.checks = map[string]*bool{}
		a.status = fmt.Sprintf(tr("已删除 %d 个任务"), n)
	}
}

func (a *app) toggleTheme() {
	a.dark = !a.dark
	applyNativeTheme(a.dark)
	saveTheme(a.dark)
	a.status = fmt.Sprintf(tr("已切换到%s主题"), tr(map[bool]string{false: "浅色", true: "深色"}[a.dark]))
}

func (a *app) enqueue(raw string) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ftp" && u.Scheme != "ftps") || u.Host == "" {
		a.status = tr("请输入 http(s) URL")
		return
	}
	a.mgr.Add(download.Request{URL: raw})
	a.status = tr("已加入队列")
}

// onChange runs after every engine state change, off the UI thread.
func (a *app) onChange() {
	var sum int64
	for _, s := range a.mgr.Snapshots() {
		sum += s.Speed
	}
	a.pushHistory(float64(sum))
	if a.svc != nil {
		a.svc.Invalidate()
	}
}

// ── text helpers ─────────────────────────────────────────────────────────

// stateText marks tasks whose file disappeared from disk.
func stateText(s download.Snapshot) string {
	if s.State == download.Done && s.FilePath != "" {
		if _, err := os.Stat(s.FilePath); os.IsNotExist(err) {
			return "文件已删除"
		}
	}
	return stateLabel(s.State)
}

func stateLabel(s download.State) string {
	switch s {
	case download.Queued:
		return "排队中"
	case download.Running:
		return "下载中"
	case download.Done:
		return "已完成"
	case download.Canceled:
		return "已暂停"
	default:
		return "出错"
	}
}

func progress(s download.Snapshot) float64 {
	if s.Total <= 0 {
		if s.State == download.Done {
			return 1
		}
		return -1
	}
	return float64(s.Done) / float64(s.Total)
}

func pctText(s download.Snapshot) string {
	if s.Total <= 0 {
		if s.State == download.Done {
			return "100%"
		}
		return "—"
	}
	return strconv.Itoa(int(float64(s.Done)/float64(s.Total)*100)) + "%"
}

func sizeText(s download.Snapshot) string {
	if s.Total > 0 {
		return bytesText(s.Total)
	}
	if s.Done > 0 {
		return bytesText(s.Done)
	}
	return "—"
}

func speedText(s download.Snapshot) string {
	if s.Speed > 0 {
		return bytesText(s.Speed) + "/s"
	}
	return "—"
}

func remainText(s download.Snapshot) string {
	if s.State == download.Done || s.Speed <= 0 || s.Total <= s.Done {
		return "—"
	}
	sec := (s.Total - s.Done) / s.Speed
	return durationText(time.Duration(sec) * time.Second)
}

func durationText(d time.Duration) string {
	sec := int64(d / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, (sec/60)%60, sec%60)
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// urlParse returns the host of a URL for the 来源 column glyph.
func urlParse(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

func categoryLabel(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	for _, ct := range categories {
		for _, e := range catExt[ct.id] {
			if ext == e {
				return ct.label
			}
		}
	}
	return "其他"
}

// Retain the semantic category keys used by filters; style.Icon maps them to
// official Lucide names for both native page implementations.
func categoryIcon(name string) string {
	if ext := strings.ToLower(filepath.Ext(name)); ext == ".iso" || ext == ".img" {
		return "disc"
	}
	switch categoryLabel(name) {
	case "视频":
		return "film"
	case "音乐":
		return "music"
	case "压缩包":
		return "zip"
	case "程序", "软件":
		return "box"
	default:
		return "file"
	}
}

func extColor(p *style.Palette, name string) ui.Color {
	switch categoryLabel(name) {
	case "程序":
		return style.BadgeBlueFg
	case "文档":
		return style.BadgeGreenFg
	case "音乐":
		return style.DetailIconFg
	case "视频":
		return style.BadgeOrangeFg
	case "压缩包":
		return style.BadgeYellowFg
	default:
		return p.TextSoft
	}
}

// ── categories ───────────────────────────────────────────────────────────

type category struct {
	id, label string
}

var categories = []category{
	{"all", "全部下载"},
	{"active", "未完成"},
	{"done", "已完成"},
	{"program", "软件"},
	{"doc", "文档"},
	{"music", "音乐"},
	{"video", "视频"},
	{"archive", "压缩包"},
	{"other", "其他"},
}

var catExt = map[string][]string{
	"program": {".exe", ".msi", ".apk", ".dmg", ".pkg", ".bat", ".msix"},
	"doc":     {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".epub"},
	"music":   {".mp3", ".m4a", ".flac", ".wav", ".ogg", ".aac", ".wma"},
	"video":   {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".ts"},
	"archive": {".zip", ".rar", ".7z", ".gz", ".tar", ".bz2", ".iso", ".xz"},
}

// customCategorySpec 是用户自定义分类：按扩展名识别，可选归档子目录。
type customCategorySpec struct {
	Label string   `json:"label"`
	Exts  []string `json:"exts"`
	Dir   string   `json:"dir,omitempty"`
}

// builtinCategoryKinds 是内置分类的顺序与图标键，与侧栏分类区一一对应。
var builtinCategoryKinds = []string{"film", "music", "zip", "box", "file"}

// activeCustomCategories 是生效的自定义分类，按侧栏顺序排列。
var activeCustomCategories []customCategorySpec

// customQueueSpec 是用户自定义队列：独立并发上限（0 表示只受全局约束）
// 与速度额度（KiB/秒，0 表示不限速）。
type customQueueSpec struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Limit int    `json:"limit,omitempty"`
	Rate  int    `json:"rate,omitempty"`
}

// siteConnSpec 是用户配置的每站点连接上限；匹配含子域。
type siteConnSpec struct {
	Host  string `json:"host"`
	Limit int    `json:"limit"`
}

// siteConnectionTable 展开给引擎的每站点连接上限表。
func siteConnectionTable(specs []siteConnSpec) map[string]int {
	limits := make(map[string]int, len(specs))
	for _, spec := range specs {
		if spec.Limit > 0 {
			limits[spec.Host] = spec.Limit
		}
	}
	return limits
}

// activeCustomQueues 是生效的自定义队列，按侧栏顺序排列。
var activeCustomQueues []customQueueSpec

// isCustomQueue 报告队列标识是否为自定义队列。
func isCustomQueue(id string) bool { return id != "" && id != "main" && id != "night" }

// customQueueName 返回自定义队列的显示名；未知标识原样返回。
func customQueueName(id string) string {
	for _, spec := range activeCustomQueues {
		if spec.ID == id {
			return spec.Name
		}
	}
	return id
}

// queueLimitTable 展开给引擎的队列并发上限表；主队列以 main 标识。
func queueLimitTable(prefs appPreferences, queues []customQueueSpec) map[string]int {
	limits := make(map[string]int, len(queues)+2)
	if prefs.MainQueueLimit > 0 {
		limits["main"] = prefs.MainQueueLimit
	}
	if prefs.NightQueueLimit > 0 {
		limits["night"] = prefs.NightQueueLimit
	}
	for _, spec := range queues {
		limits[spec.ID] = spec.Limit
	}
	return limits
}

// queueRateTable 展开给引擎的队列速度额度表（字节/秒）；主队列以 main 标识。
func queueRateTable(prefs appPreferences, queues []customQueueSpec) map[string]int64 {
	rates := make(map[string]int64, len(queues)+2)
	if prefs.MainQueueRate > 0 {
		rates["main"] = int64(prefs.MainQueueRate) * 1024
	}
	if prefs.NightQueueRate > 0 {
		rates["night"] = int64(prefs.NightQueueRate) * 1024
	}
	for _, spec := range queues {
		if spec.Rate > 0 {
			rates[spec.ID] = int64(spec.Rate) * 1024
		}
	}
	return rates
}

// applyQueues 应用自定义队列配置。
func applyQueues(prefs appPreferences) {
	activeCustomQueues = append([]customQueueSpec(nil), prefs.CustomQueues...)
}

// activeCatExt 是分类识别实际使用的扩展名表，可被用户规则替换。
var activeCatExt = catExt

// customCategoryMatch 报告文件名扩展名是否命中自定义分类。
func customCategoryMatch(spec customCategorySpec, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, want := range spec.Exts {
		if ext == want {
			return true
		}
	}
	return false
}

// applyCategories 应用用户的分类配置：扩展名规则与自定义分类。
func applyCategories(prefs appPreferences) {
	activeCatExt = effectiveCatExt(prefs.CategoryExts)
	activeCustomCategories = append([]customCategorySpec(nil), prefs.CustomCategories...)
}

// effectiveCatExt 合并用户的分类扩展名规则；未自定义的分类保持内置表。
func effectiveCatExt(overrides map[string][]string) map[string][]string {
	out := make(map[string][]string, len(catExt))
	for id, exts := range catExt {
		out[id] = append([]string(nil), exts...)
	}
	for id, exts := range overrides {
		if _, ok := out[id]; ok && len(exts) > 0 {
			out[id] = append([]string(nil), exts...)
		}
	}
	return out
}

// categoryDirTable 把分类子目录设置展开成引擎用的扩展名表；other 作为
// 未匹配扩展名的兜底子目录。分类按固定顺序取扩展名，重叠时前者优先。
func categoryDirTable(dirs map[string]string) (map[string]string, string) {
	byExt := map[string]string{}
	// 自定义分类优先：用户的显式规则覆盖内置映射。
	for _, spec := range activeCustomCategories {
		subdir := download.CleanCategoryDir(spec.Dir)
		if subdir == "" {
			continue
		}
		for _, ext := range spec.Exts {
			if _, exists := byExt[ext]; !exists {
				byExt[ext] = subdir
			}
		}
	}
	for _, id := range []string{"program", "doc", "music", "video", "archive"} {
		subdir := dirs[id]
		if subdir == "" {
			continue
		}
		for _, ext := range activeCatExt[id] {
			if _, exists := byExt[ext]; !exists {
				byExt[ext] = subdir
			}
		}
	}
	return byExt, dirs["other"]
}

func inCategory(id string, s download.Snapshot) bool {
	switch id {
	case "", "all":
		return true
	case "active":
		return s.State != download.Done
	case "done":
		return s.State == download.Done
	}
	ext := strings.ToLower(filepath.Ext(s.Name))
	if id == "other" {
		for _, bucket := range activeCatExt {
			for _, e := range bucket {
				if ext == e {
					return false
				}
			}
		}
		return true
	}
	for _, e := range activeCatExt[id] {
		if ext == e {
			return true
		}
	}
	return false
}

// ── theme persistence ────────────────────────────────────────────────────

var themeOverride string

func themePath() string {
	if themeOverride != "" {
		return themeOverride
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "opendl", "theme")
}

func loadTheme() bool {
	return loadThemeMode() == "dark"
}

func loadThemeMode() string {
	p := themePath()
	if p == "" {
		return "light"
	}
	b, err := os.ReadFile(p)
	if err == nil {
		switch mode := strings.TrimSpace(string(b)); mode {
		case "light", "dark", "system":
			return mode
		}
	}
	return "light"
}

// Context.SetTheme styles client content only. Native caption controls and
// system menus follow the application appearance source instead.
func applyNativeTheme(dark bool) {
	// Headless views have no native event loop or caption to update.
	if !mygo.App.IsReady() {
		return
	}
	source := mygo.ThemeLight
	if dark {
		source = mygo.ThemeDark
	}
	mygo.Theme.SetSource(source)
}

func applyNativeThemeMode(mode string) {
	if !mygo.App.IsReady() {
		return
	}
	source := mygo.ThemeLight
	switch mode {
	case "dark":
		source = mygo.ThemeDark
	case "system":
		source = mygo.ThemeSystem
	}
	mygo.Theme.SetSource(source)
}

func saveTheme(dark bool) {
	mode := "light"
	if dark {
		mode = "dark"
	}
	saveThemeMode(mode)
}

func saveThemeMode(mode string) {
	p := themePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(mode), 0o600)
}

// downloadDir is where downloads land: the user's Downloads folder.
func downloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dl := filepath.Join(home, "Downloads")
	if st, err := os.Stat(dl); err == nil && st.IsDir() {
		return filepath.Join(dl, "open_dl")
	}
	return filepath.Join(home, "open_dl")
}

func main() {
	if nativehost.IsBrowserInvocation(os.Args[1:]) {
		nativehost.Run()
		return
	}
	if runUpdateHelper() {
		return
	}
	// 设计规则字体：Geist + MiSans（docs/design-rules.md）。
	if err := style.RegisterFonts(); err != nil {
		log.Printf("register fonts: %v", err)
	}
	preferences := loadAppPreferences()
	i18n.Set(preferences.Language)
	dir := preferences.Directory
	mgr := download.NewManager(dir, 3, preferences.Connections)
	mgr.SetMediaMuxProgram(preferences.MediaMuxProgram)
	mgr.SetRateLimit(int64(preferences.RateMBps * float64(1<<20)))
	mgr.SetMaxConnections(preferences.MaxConnections)
	mgr.SetAllowUnverifiedResume(preferences.AllowUnverifiedResume)
	proxyPassword, proxyErr := initializeDownloadProxy(mgr, preferences)
	setSiteRulesSource(preferences.SiteRulesURL)
	if proxyErr != nil {
		log.Printf(tr("下载代理: %v"), proxyErr)
	}
	if logins, err := openSiteLogins(preferences.SiteLoginsSecret); err == nil {
		mgr.SetSiteLogins(siteLoginEngine(logins))
	} else {
		log.Printf(tr("站点登录: %v"), err)
	}

	windowsAuthErr := mgr.SetWindowsAuthSites(preferences.WindowsAuth)
	if windowsAuthErr != nil {
		mgr.BlockWindowsAuth(windowsAuthErr)
		log.Printf("Windows integrated authentication: %v", windowsAuthErr)
	}
	var persistenceErr error
	if configDir, err := os.UserConfigDir(); err == nil {
		if err := mgr.EnablePersistence(filepath.Join(configDir, "opendl", "tasks")); err != nil {
			persistenceErr = err
			log.Printf(tr("恢复下载记录: %v"), err)
		}
	} else {
		persistenceErr = err
		log.Printf(tr("获取下载记录目录: %v"), err)
	}

	if err := mgr.SetTrafficQuota(preferences.QuotaPeriod, int64(preferences.QuotaMB)<<20); err != nil {
		log.Printf(tr("流量配额: %v"), err)
	}
	applyCategories(preferences)
	applyQueues(preferences)
	mgr.SetQueueLimits(queueLimitTable(preferences, preferences.CustomQueues))
	mgr.SetQueueRates(queueRateTable(preferences, preferences.CustomQueues))
	mgr.SetSiteConnectionLimits(siteConnectionTable(preferences.SiteConnections))
	{
		byExt, fallback := categoryDirTable(preferences.CategoryDirs)
		mgr.SetCategoryDirs(byExt, fallback)
	}

	promptQueue := make(chan download.Request, 64)
	promptSlots := make(chan struct{}, 64)
	installExit := make(chan struct{}, 1)
	srv := &api.Server{Mgr: mgr, Prompt: func(req download.Request) bool {
		select {
		case promptSlots <- struct{}{}:
		default:
			return false
		}
		select {
		case promptQueue <- req:
			return true
		default:
			<-promptSlots
			return false
		}
	}}
	srv.InstallExit = func(target string) bool {
		current, err := os.Executable()
		if err != nil || !filepath.IsAbs(target) || !strings.EqualFold(filepath.Clean(target), filepath.Clean(current)) {
			return false
		}
		select {
		case installExit <- struct{}{}:
		default:
		}
		return true
	}
	srv.OpenTask = func(id string, folder bool) error {
		for _, snap := range mgr.Snapshots() {
			if snap.ID != id || !snap.BrowserOrigin || snap.State != download.Done || snap.FilePath == "" {
				continue
			}
			if _, err := os.Stat(snap.FilePath); err != nil {
				return err
			}
			if folder {
				mygo.Shell.ShowItemInFolder(snap.FilePath)
				return nil
			}
			return mygo.Shell.OpenPath(snap.FilePath)
		}
		return fmt.Errorf("task unavailable")
	}
	apiErr := srv.StartWithNewToken()
	if apiErr != nil {
		log.Printf("api server: %v", apiErr)
	}
	if err := nativehost.Register(); err != nil {
		log.Printf("browser integration: %v", err)
	}

	themeMode := loadThemeMode()
	var mainWindowPage *mainPage
	mygo.App.WhenReady(func() {
		applyNativeThemeMode(themeMode)
		dark := mygo.Theme.IsDark()
		// 启动时最小化到托盘：窗口以隐藏状态创建，由托盘入口唤回。
		hidden := preferences.MinTrayStart
		// docs/design.html 的 1:1 复刻页面：自定义标题栏（原生窗口按钮）
		// + 工具栏 + 侧栏/统计/任务表/详情面板，数据由 download.Manager 驱动。
		pg := &mainPage{mgr: mgr, apiServer: srv, saveDir: dir, dark: dark, themeMode: themeMode, preferences: preferences, preferencesLoaded: true, detailPlacement: loadDetailPlacement(), panes: loadPanelLayout()}
		pg.showExtensionGuide = shouldShowExtensionGuide()
		pg.syncExtensionTheme()
		pg.proxyPassword = proxyPassword
		pg.proxyError = proxySettingsStatus(proxyErr)
		if persistenceErr != nil {
			pg.status = tr("下载记录恢复失败：") + persistenceErr.Error()
			pg.statusAt = time.Now()
		} else if apiErr != nil {
			pg.status = tr("浏览器接入不可用：") + apiErr.Error()
			pg.statusAt = time.Now()
		} else if windowsAuthErr != nil {
			pg.status = tr("Windows 集成认证配置无效，请在设置中修正：") + windowsAuthErr.Error()
		} else if proxyErr != nil {
			pg.status = pg.proxyError
			pg.statusAt = time.Now()
		}
		if message := updateFailureMessage(os.Args); message != "" {
			pg.updater.message = message
			if pg.status == "" {
				pg.status = message
				pg.statusAt = time.Now()
			}
		}
		mainWindowPage = pg
		pg.win = mygo.NewWindow(mygo.WindowOptions{
			Title: appBrand,
			// 首次启动采用紧凑的桌面窗口；StateKey 优先恢复用户保存的尺寸。
			// 最小宽度收到 680：1366 及更宽的屏幕都能把窗口缩到半屏（Win+←/→）。
			Width:     1200,
			Height:    720,
			MinWidth:  680,
			MinHeight: 640,
			StateKey:  "main",
			Hidden:    hidden,
			Content:   ui.View(pg.view),

			// 隐藏原生标题栏，但保留框架的原生窗口按钮（最小化/最大
			// 化/关闭，带 Win11 贴靠布局、系统菜单与原生命中测试），
			// 它们由 Windows 绘制在我们顶栏的右端。
			TitleBarStyle:  mygo.TitleBarHidden,
			TitleBarHeight: 38,
		})
		// 窗口图标使用与托盘和关于窗口相同的资源，也覆盖旧的可执行文件图标。
		if err := pg.win.SetIcon(appIconPNG); err != nil {
			log.Printf("设置窗口图标失败: %v", err)
		}
		// 窗口事件与后台工作在窗口创建后立即绑定，不等首次视图构建。
		pg.startWindow()
		go func() {
			for {
				select {
				case <-installExit:
					pg.mgr.PauseAll()
					deadline := time.NewTimer(25 * time.Second)
					ticker := time.NewTicker(250 * time.Millisecond)
					for {
						if pg.mgr.PrepareForUpdate() {
							deadline.Stop()
							ticker.Stop()
							pg.win.Update(func() {
								pg.quitting = true
								pg.saveAppPreferences()
								mygo.App.Quit()
							})
							return
						}
						select {
						case <-ticker.C:
						case <-deadline.C:
							ticker.Stop()
							goto retryInstallExit
						case <-pg.stop:
							deadline.Stop()
							ticker.Stop()
							return
						}
					}
				case <-pg.stop:
					return
				}
			retryInstallExit:
			}
		}()
		pg.browserPromptSlot = func() { <-promptSlots }
		go func() {
			for {
				select {
				case req := <-promptQueue:
					pg.win.Update(func() { pg.openBrowserDownload(req) })
				case <-pg.stop:
					return
				}
			}
		}()
		pg.watchUpdates()
		acknowledgeUpdate()
		offTheme := mygo.Theme.OnUpdated(func() {
			if pg.themeMode == "system" {
				pg.dark = mygo.Theme.IsDark()
				pg.syncExtensionTheme()
				pg.refreshSettingsWindow()
				pg.win.Invalidate()
			}
		})
		pg.win.OnClosed(offTheme)
		mgr.OnChange(pg.onChange)
		pg.setupNotifications()
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { pg.quitting = true })
		mygo.App.OnWillQuit(func(*mygo.QuitEvent) { pg.teardownTray() })
		if preferences.MinTrayStart || preferences.MinTrayClose {
			if err := pg.setupTray(); err != nil {
				log.Printf(tr("系统托盘: %v"), err)
				if hidden {
					pg.win.Show()
				}
			}
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if mainWindowPage != nil && mainWindowPage.layoutDone != nil {
		<-mainWindowPage.layoutDone
	}
	if mainWindowPage != nil && mainWindowPage.settingsDone != nil {
		<-mainWindowPage.settingsDone
	}
}
