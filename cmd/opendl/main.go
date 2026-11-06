package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openDL/api"
	"openDL/download"
	"openDL/i18n"
	"openDL/installerconfig"
	"openDL/nativehost"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const appBrand = "OpenDL"

// ── text helpers ─────────────────────────────────────────────────────────

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

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// hashFile computes a completed file's SHA-256 and MD5 in one read.
func hashFile(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "—", "—"
	}
	defer f.Close()
	sha := sha256.New()
	md := md5.New()
	if _, err := io.Copy(io.MultiWriter(sha, md), f); err != nil {
		return "—", "—"
	}
	return shortHash(hex.EncodeToString(sha.Sum(nil))), shortHash(hex.EncodeToString(md.Sum(nil)))
}

// shortHash renders "3f2a6e…d9c4e7" like the reference.
func shortHash(h string) string {
	if len(h) <= 11 {
		return h
	}
	return h[:6] + "…" + h[len(h)-5:]
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

// ── theme persistence ────────────────────────────────────────────────────

const (
	themeBlender      = "blender" // persisted key for Blender Dark
	themeBlenderLight = "blender-light"
)

func isBlenderThemeMode(mode string) bool {
	return mode == themeBlender || mode == themeBlenderLight
}

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
	mode := loadThemeMode()
	return mode == "dark" || mode == themeBlender
}

func loadThemeMode() string {
	p := themePath()
	if p == "" {
		return themeBlenderLight
	}
	b, err := os.ReadFile(p)
	if err == nil {
		switch mode := strings.TrimSpace(string(b)); mode {
		case "light", "dark", "system", themeBlender, themeBlenderLight:
			return mode
		}
	}
	return themeBlenderLight
}

func applyNativeThemeMode(mode string) {
	if !mygo.App.IsReady() {
		return
	}
	source := mygo.ThemeLight
	switch mode {
	case "dark", themeBlender:
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
		nativehost.RunWithVersion(mygo.App.Version())
		return
	}
	if runUpdateHelper() {
		return
	}
	// 诊断日志：标准 log 输出滚动写入日志文件，stderr 输出保持不变。
	setupDiagLog()
	// 注册界面字体：Geist + MiSans。
	if err := style.RegisterFonts(); err != nil {
		log.Printf("register fonts: %v", err)
	}
	firstRun := false
	if path, err := appPreferencesPath(); err == nil {
		_, statErr := os.Stat(path)
		firstRun = os.IsNotExist(statErr)
	}
	preferences := loadAppPreferences()
	i18n.Set(preferences.Language)
	dir := preferences.Directory
	mgr := download.NewManager(dir, preferences.TaskConcurrency, preferences.Connections)
	_ = mgr.SetUserAgent(preferences.UserAgent) // Preferences were normalized before initialization.
	mgr.SetMediaMuxProgram(preferences.MediaMuxProgram)
	mgr.SetRateLimit(int64(preferences.RateMBps * float64(1<<20)))
	mgr.SetMaxConnections(preferences.MaxConnections)
	mgr.SetAllowUnverifiedResume(preferences.AllowUnverifiedResume)
	mgr.SetOverwriteExisting(preferences.FileExistsAction != "copy")
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
		// A transient port or token-file failure should not require restarting
		// the desktop app. StartWithNewToken reserves the port before writing
		// the token, so retries cannot invalidate another running instance.
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := srv.StartWithNewToken(); err == nil {
					log.Printf("api server: browser integration restored")
					return
				}
			}
		}()
	}
	if err := nativehost.Register(); err != nil {
		log.Printf("browser integration: %v", err)
	}

	themeMode := loadThemeMode()
	var mainWindowPage *mainPage
	mygo.App.WhenReady(func() {
		// Apply the installer's explicit choice once, without replacing other settings.
		executable, exeErr := os.Executable()
		if exeErr != nil {
			log.Printf("读取程序路径: %v", exeErr)
		}
		if enabled, present, err := installerconfig.ReadAutoStart(executable); err != nil {
			log.Printf("读取安装器启动设置: %v", err)
		} else if present {
			if err := mygo.App.SetOpenAtLogin(enabled); err != nil {
				log.Printf("应用安装器启动设置: %v", err)
			} else {
				preferences.AutoStart = &enabled
				if err := writeAppPreferences(preferences); err != nil {
					log.Printf("保存安装器启动设置: %v", err)
				} else if err := installerconfig.ClearAutoStart(); err != nil {
					log.Printf("清理安装器启动设置: %v", err)
				}
			}
		}
		// 开机自启动默认开启：从未在设置中选择过时登记一次并记录，之后由
		// 设置开关与系统状态维护，任务管理器等外部禁用不覆盖。
		autoStartRecorded := false
		if preferences.AutoStart == nil {
			if err := mygo.App.SetOpenAtLogin(true); err != nil {
				log.Printf("无法启用开机自动启动: %v", err)
			} else {
				chosen := true
				preferences.AutoStart = &chosen
				autoStartRecorded = true
			}
		}
		applyNativeThemeMode(themeMode)
		dark := mygo.Theme.IsDark()
		// 启动时最小化到托盘：窗口以隐藏状态创建，由托盘入口唤回。
		hidden := preferences.MinTrayStart
		// 主界面：自定义标题栏（原生窗口按钮）
		// + 工具栏 + 侧栏/统计/任务表/详情面板，数据由 download.Manager 驱动。
		pg := &mainPage{mgr: mgr, apiServer: srv, saveDir: dir, dark: dark, themeMode: themeMode, preferences: preferences, preferencesLoaded: true, detailPlacement: loadDetailPlacement(), panes: loadPanelLayout()}
		pg.syncExtensionTheme()
		pg.proxyPassword = proxyPassword
		pg.proxyError = proxySettingsStatus(proxyErr)
		if persistenceErr != nil {
			pg.flash("下载记录恢复失败：%s", persistenceErr.Error())
		} else if apiErr != nil {
			pg.flash("浏览器接入不可用：%s", apiErr.Error())
		} else if windowsAuthErr != nil {
			pg.flash("Windows 集成认证配置无效，请在设置中修正：%s", windowsAuthErr.Error())
		} else if proxyErr != nil {
			pg.flash(pg.proxyError)
		}
		if message := updateFailureMessage(os.Args); message != "" {
			pg.updater.message = message
			if pg.status == "" {
				pg.flash(message)
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
		if firstRun || autoStartRecorded {
			pg.saveAppPreferences()
		}
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
				pg.invalidateThemeWindows()
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
	// ioWatch 退出前排干已排队的小文件写入，等待收尾避免落盘被进程退出截断。
	if mainWindowPage != nil && mainWindowPage.ioDone != nil {
		<-mainWindowPage.ioDone
	}
}
