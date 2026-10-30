package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openDL/api"
	"openDL/appupdate"
	"openDL/download"
	"openDL/i18n"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// maxConnectionsLimit 是全局连接上限允许的最大值，0 表示无限制。
const maxConnectionsLimit = 256

// maxFileTypes 是自动下载文件类型列表的条目上限。
const maxFileTypes = 64

type appPreferences struct {
	Shortcuts            map[string]string          `json:"shortcuts,omitempty"`
	MediaMuxProgram      string                     `json:"media_mux_program,omitempty"`
	SiteRulesURL         string                     `json:"site_rules_url,omitempty"`
	WindowsAuth          []download.WindowsAuthSite `json:"windows_auth,omitempty"`
	Updates              appupdate.Config           `json:"updates,omitempty"`
	Language             string                     `json:"language,omitempty"`
	AccentColor          string                     `json:"accent_color,omitempty"`
	Directory            string                     `json:"directory"`
	Connections          int                        `json:"connections"`
	MaxConnections       int                        `json:"max_connections"`
	Clipboard            bool                       `json:"clipboard"`
	DragFileAction       string                     `json:"drag_file_action,omitempty"`
	MinTrayStart         bool                       `json:"min_tray_start"`
	MinTrayClose         bool                       `json:"min_tray_close"`
	NotifyComplete       bool                       `json:"notify_complete"`
	FinishAction         string                     `json:"finish_action,omitempty"`
	FinishDays           []int                      `json:"finish_days,omitempty"`
	FinishProgram        string                     `json:"finish_program,omitempty"`
	FinishArgs           string                     `json:"finish_args,omitempty"`
	ScanProgram          string                     `json:"scan_program,omitempty"`
	ScanArgs             string                     `json:"scan_args,omitempty"`
	RateMBps             float64                    `json:"rate_mbps"`
	QuotaPeriod          string                     `json:"quota_period,omitempty"`
	QuotaMB              int                        `json:"quota_mb,omitempty"`
	NightOn              bool                       `json:"night_on"`
	NightAt              string                     `json:"night_at"`
	NightStop            string                     `json:"night_stop,omitempty"`
	NightDays            []int                      `json:"night_days,omitempty"`
	SettingsSidebarWidth float32                    `json:"settings_sidebar_width"`
	Proxy                download.ProxyConfig       `json:"proxy,omitempty"`
	ProxySecret          string                     `json:"proxy_secret,omitempty"`
	SiteLoginsSecret     string                     `json:"site_logins_secret,omitempty"`
	CategoryDirs         map[string]string          `json:"category_dirs,omitempty"`
	CategoryExts         map[string][]string        `json:"category_exts,omitempty"`
	CustomCategories     []customCategorySpec       `json:"custom_categories,omitempty"`
	SiteConnections      []siteConnSpec             `json:"site_connections,omitempty"`
	MainQueueLimit       int                        `json:"main_queue_limit,omitempty"`
	NightQueueLimit      int                        `json:"night_queue_limit,omitempty"`
	MainQueueRate        int                        `json:"main_queue_rate,omitempty"`
	NightQueueRate       int                        `json:"night_queue_rate,omitempty"`
	CustomQueues         []customQueueSpec          `json:"custom_queues,omitempty"`
	QueueSeq             int                        `json:"queue_seq,omitempty"`
	SortDesc             bool                       `json:"sort_desc,omitempty"`
}

func defaultAppPreferences() appPreferences {
	return appPreferences{Shortcuts: map[string]string{}, Language: i18n.Chinese, AccentColor: "blue", Directory: downloadDir(), Connections: 4, Clipboard: true, NotifyComplete: true, NightOn: false, NightAt: "01:00", SettingsSidebarWidth: settingsSidebarDefault}
}

var connectionOptions = [...]int{-1, 1, 4, 8, 16, 32}

func connectionIndex(value int) int {
	for i, option := range connectionOptions {
		if option == value {
			return i
		}
	}
	return 0 // Automatic mode.
}

// fileTypesText 把已保存的文件类型渲染成设置输入框的文本。
func fileTypesText(types []string) string { return strings.Join(types, " ") }

// parseFileTypes 解析空格或逗号分隔的扩展名，去掉前导点并转小写。
func parseFileTypes(text string) ([]string, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', ',', '，', ';', '；':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(field)), ".")
		if name == "" {
			continue
		}
		if !validFileType(name) {
			return nil, fmt.Errorf(tr("文件类型格式无效：%s"), field)
		}
		out = append(out, name)
	}
	out = normalizeFileTypes(out)
	if len(out) > maxFileTypes {
		return nil, fmt.Errorf(tr("文件类型最多 %d 个"), maxFileTypes)
	}
	return out, nil
}

// normalizeFileTypes 去重并丢弃不合法的条目，用于读取旧配置。
func normalizeFileTypes(types []string) []string {
	out := make([]string, 0, len(types))
	seen := make(map[string]bool, len(types))
	for _, item := range types {
		name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item)), ".")
		if name == "" || seen[name] || !validFileType(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= maxFileTypes {
			break
		}
	}
	return out
}

func validFileType(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func (p *appPreferences) validate() {
	if p.DragFileAction != "copy" {
		p.DragFileAction = ""
	}
	p.Language = i18n.Normalize(p.Language)
	if p.AccentColor == "" {
		p.AccentColor = "blue"
	} else if p.AccentColor != "gray" {
		if _, ok := style.Accent(p.AccentColor); !ok {
			p.AccentColor = "blue"
		}
	}
	if p.Shortcuts == nil {
		p.Shortcuts = map[string]string{}
	}
	if !updateSourceAvailable(p.Updates) {
		p.Updates.Automatic = false
	}
	if p.Updates.LastChecked.After(time.Now()) {
		p.Updates.LastChecked = time.Time{}
	}
	// Do not replace an invalid manual proxy with an environment/direct
	// route. Initialization reports it and blocks downloads until corrected.
	p.Proxy.Password = ""
	if normalized, err := download.NormalizeProxyConfig(p.Proxy); err == nil {
		p.Proxy = normalized
	}
	if math.IsNaN(float64(p.SettingsSidebarWidth)) || math.IsInf(float64(p.SettingsSidebarWidth), 0) || p.SettingsSidebarWidth < settingsSidebarMin || p.SettingsSidebarWidth > settingsSidebarMax {
		p.SettingsSidebarWidth = settingsSidebarDefault
	}
	if !filepath.IsAbs(p.Directory) {
		p.Directory = downloadDir()
	}
	switch p.Connections {
	case -1, 1, 4, 8, 16, 32:
	default:
		p.Connections = 8
	}
	if p.MaxConnections < 0 || p.MaxConnections > maxConnectionsLimit {
		p.MaxConnections = 0
	}
	// 托盘入口由两个最小化选项按需创建，没有需要互斥的总开关。
	if math.IsNaN(p.RateMBps) || math.IsInf(p.RateMBps, 0) || p.RateMBps < 0 || p.RateMBps > 10240 {
		p.RateMBps = 0
	}
	switch p.FinishAction {
	case "", "shutdown", "sleep", "run":
	default:
		p.FinishAction = ""
	}
	p.FinishDays = normalizeNightDays(p.FinishDays)
	p.SiteRulesURL = strings.TrimSpace(p.SiteRulesURL)
	if source := p.SiteRulesURL; source != "" {
		if u, err := url.Parse(source); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			p.SiteRulesURL = ""
		}
	}
	if len(p.FinishProgram) > 1024 {
		p.FinishProgram = ""
	}
	if len(p.FinishArgs) > 512 {
		p.FinishArgs = ""
	}
	if len(p.SiteLoginsSecret) > 512<<10 {
		p.SiteLoginsSecret = ""
	}
	if len(p.CategoryExts) > 0 {
		cleaned := make(map[string][]string, len(p.CategoryExts))
		for id, exts := range p.CategoryExts {
			if _, ok := catExt[id]; !ok || len(exts) == 0 {
				continue
			}
			out := make([]string, 0, len(exts))
			seen := map[string]bool{}
			for _, ext := range exts {
				name := strings.ToLower(strings.TrimSpace(ext))
				if !strings.HasPrefix(name, ".") || len(name) > 16 || !validFileType(name[1:]) || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
				if len(out) >= maxFileTypes {
					break
				}
			}
			if len(out) > 0 {
				cleaned[id] = out
			}
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		p.CategoryExts = cleaned
	}
	if len(p.SiteConnections) > 0 {
		cleaned := make([]siteConnSpec, 0, len(p.SiteConnections))
		seen := map[string]bool{}
		for _, spec := range p.SiteConnections {
			host := strings.ToLower(strings.TrimSpace(spec.Host))
			if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\:@ ?") || seen[host] || len(cleaned) >= 32 {
				continue
			}
			if spec.Limit < 1 || spec.Limit > 64 {
				continue
			}
			seen[host] = true
			cleaned = append(cleaned, siteConnSpec{Host: host, Limit: spec.Limit})
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		p.SiteConnections = cleaned
	}
	if p.MainQueueLimit < 0 || p.MainQueueLimit > 64 {
		p.MainQueueLimit = 0
	}
	if p.NightQueueLimit < 0 || p.NightQueueLimit > 64 {
		p.NightQueueLimit = 0
	}
	if p.MainQueueRate < 0 || p.MainQueueRate > 1048576 {
		p.MainQueueRate = 0
	}
	if p.NightQueueRate < 0 || p.NightQueueRate > 1048576 {
		p.NightQueueRate = 0
	}
	if len(p.CustomQueues) > 0 {
		cleaned := make([]customQueueSpec, 0, len(p.CustomQueues))
		seen := map[string]bool{}
		for _, spec := range p.CustomQueues {
			name := strings.TrimSpace(spec.Name)
			if name == "" || len([]rune(name)) > 24 || len(cleaned) >= 16 || seen[spec.ID] {
				continue
			}
			if !download.ValidQueueID(spec.ID) || !isCustomQueue(spec.ID) || spec.Limit < 0 || spec.Limit > 64 {
				continue
			}
			if spec.Rate < 0 || spec.Rate > 1048576 {
				continue
			}
			seen[spec.ID] = true
			cleaned = append(cleaned, customQueueSpec{ID: spec.ID, Name: name, Limit: spec.Limit, Rate: spec.Rate})
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		p.CustomQueues = cleaned
	}
	if p.QueueSeq < 0 || p.QueueSeq > 1000000 {
		p.QueueSeq = 0
	}
	if len(p.CustomCategories) > 0 {
		cleaned := make([]customCategorySpec, 0, len(p.CustomCategories))
		for _, spec := range p.CustomCategories {
			label := strings.TrimSpace(spec.Label)
			if label == "" || len([]rune(label)) > 24 || len(cleaned) >= 16 {
				continue
			}
			seen := map[string]bool{}
			exts := make([]string, 0, len(spec.Exts))
			for _, ext := range spec.Exts {
				name := strings.ToLower(strings.TrimSpace(ext))
				if !strings.HasPrefix(name, ".") || len(name) > 16 || !validFileType(name[1:]) || seen[name] {
					continue
				}
				seen[name] = true
				exts = append(exts, name)
				if len(exts) >= maxFileTypes {
					break
				}
			}
			if len(exts) == 0 {
				continue
			}
			cleaned = append(cleaned, customCategorySpec{Label: label, Exts: exts, Dir: download.CleanCategoryDir(spec.Dir)})
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		p.CustomCategories = cleaned
	}
	if len(p.CategoryDirs) > 0 {
		cleaned := make(map[string]string, len(p.CategoryDirs))
		for id, subdir := range p.CategoryDirs {
			if len(id) > 32 || len(subdir) > 240 {
				continue
			}
			if value := download.CleanCategoryDir(subdir); value != "" {
				cleaned[id] = value
			}
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		p.CategoryDirs = cleaned
	}
	if len(p.ScanProgram) > 1024 {
		p.ScanProgram = ""
	}
	if len(p.ScanArgs) > 512 {
		p.ScanArgs = ""
	}
	switch p.QuotaPeriod {
	case "", "day", "week", "month":
	default:
		p.QuotaPeriod = ""
	}
	if p.QuotaMB < 0 || p.QuotaMB > 4194304 {
		p.QuotaMB = 0
	}
	parsed, err := time.Parse("15:04", p.NightAt)
	if err != nil {
		p.NightAt = "01:00"
	} else {
		p.NightAt = parsed.Format("15:04")
	}
	if p.NightStop != "" {
		if stop, err := time.Parse("15:04", p.NightStop); err != nil {
			p.NightStop = ""
		} else {
			p.NightStop = stop.Format("15:04")
		}
	}
	p.NightDays = normalizeNightDays(p.NightDays)
}

func appPreferencesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "settings.json"), nil
}

func loadAppPreferences() appPreferences {
	value := defaultAppPreferences()
	if path, err := appPreferencesPath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &value) != nil {
				value = defaultAppPreferences()
			} else {
				// 旧版「关闭窗口时后台运行」就是现在的关闭时最小化到托盘；
				// 旧「系统托盘」只是常驻开关，不等于启动最小化，不迁移。
				var legacy struct {
					BackgroundOnClose bool `json:"background_on_close"`
				}
				if json.Unmarshal(data, &legacy) == nil && legacy.BackgroundOnClose {
					value.MinTrayClose = true
				}
			}
		}
	}
	value.validate()
	return value
}

// Called on the UI thread. The worker receives a copy, never live page state.
func (pg *mainPage) saveAppPreferences() {
	pg.syncExtensionTheme()
	pg.preferences.Directory = pg.saveDir
	pg.preferences.Clipboard = pg.clipOn
	pg.preferences.SortDesc = pg.sortDesc
	pg.preferences.NightOn = pg.nightOn
	pg.preferences.NightAt = pg.nightAt
	pg.preferences.NightStop = pg.nightStop
	pg.preferences.NightDays = pg.nightDays
	pg.preferences.RateMBps = 0
	if pg.limitOn {
		pg.preferences.RateMBps, _ = strconv.ParseFloat(pg.limitValue, 64)
	}
	if pg.settingsWrites != nil {
		snapshot := pg.preferences
		select {
		case pg.settingsWrites <- snapshot:
		default:
			select {
			case <-pg.settingsWrites:
			default:
			}
			select {
			case pg.settingsWrites <- snapshot:
			default:
			}
		}
	}
	pg.refreshSettingsWindow()
}

// syncExtensionTheme publishes both modes without exposing mutable page state.
func (pg *mainPage) syncExtensionTheme() {
	if pg.apiServer == nil {
		return
	}
	hex := func(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
	tokens := func(dark bool) api.ThemeColors {
		c := style.NewAccentRules(dark, pg.preferences.AccentColor).Colors
		return api.ThemeColors{Primary: hex(c.Primary), OnPrimary: hex(c.OnPrimary), Selection: hex(c.Selection), Focus: hex(c.Focus)}
	}
	active := "light"
	if pg.dark {
		active = "dark"
	}
	pg.apiServer.SetTheme(api.Theme{Light: tokens(false), Dark: tokens(true), Active: active})
}

func writeAppPreferences(value appPreferences) error {
	path, err := appPreferencesPath()
	if err != nil {
		return err
	}
	return writeAppPreferencesTo(path, value)
}

func writeAppPreferencesTo(path string, value appPreferences) error {
	var err error
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (pg *mainPage) settingsWatch() {
	defer close(pg.settingsDone)
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	var latest appPreferences
	dirty := false
	flush := func() {
		if !dirty {
			return
		}
		dirty = false
		if err := writeAppPreferences(latest); err != nil {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
				default:
					pg.settingsSaveError = tr("无法保存设置：") + err.Error()
					pg.flash(pg.settingsSaveError)
					if s := pg.settingsWindow; s != nil && s.win != nil {
						s.win.Invalidate()
					}
				}
			})
		} else {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
				default:
					if pg.settingsSaveError != "" {
						pg.settingsSaveError = ""
						if s := pg.settingsWindow; s != nil && s.win != nil {
							s.win.Invalidate()
						}
					}
				}
			})
		}
	}
	for {
		select {
		case latest = <-pg.settingsWrites:
			dirty = true
		case <-ticker.C:
			flush()
		case <-pg.stop:
			select {
			case latest = <-pg.settingsWrites:
				dirty = true
			default:
			}
			flush()
			return
		}
	}
}

func (pg *mainPage) setDefaultDirectory(dir string) bool {
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		return false
	}
	pg.saveDir = filepath.Clean(dir)
	if !pg.showAdd {
		pg.dlgDir = pg.saveDir
	} else {
		pg.defaultDirPending = true
	}
	if pg.mgr != nil {
		pg.mgr.SetDownloadDefaults(pg.saveDir, pg.preferences.Connections)
	}
	pg.saveAppPreferences()
	return true
}

func (pg *mainPage) setDefaultConnections(value int) {
	switch value {
	case -1, 1, 4, 8, 16, 32:
	default:
		return
	}
	pg.preferences.Connections = value
	if !pg.showAdd {
		pg.dlgSeg = connectionIndex(value)
	} else {
		pg.defaultConnsPending = true
	}
	if pg.mgr != nil {
		pg.mgr.SetDownloadDefaults(pg.saveDir, value)
	}
	pg.saveAppPreferences()
}

// setMaxConnections 调整所有任务合计的连接上限，0 表示无限制。
func (pg *mainPage) setMaxConnections(value int) {
	if value < 0 || value > maxConnectionsLimit {
		return
	}
	pg.preferences.MaxConnections = value
	if pg.mgr != nil {
		pg.mgr.SetMaxConnections(value)
	}
	pg.saveAppPreferences()
}

// setAutoStart 登记或移除登录启动项；注册状态由系统维护。
func (pg *mainPage) setAutoStart(enabled bool) error {
	if !mygo.App.IsReady() {
		return errors.New(tr("开机自动启动需要桌面应用"))
	}
	return mygo.App.SetOpenAtLogin(enabled)
}

// autoStartEnabled 读取系统登记的登录启动状态，任务管理器禁用后如实反映。
func autoStartEnabled() bool {
	return mygo.App.IsReady() && mygo.App.OpenAtLogin()
}

// applyTrayPrefs 落实两个最小化到托盘选项：任一开启都需要托盘入口，全部
// 关闭时移除托盘并恢复关闭即退出。失败时回滚两个选项。
func (pg *mainPage) applyTrayPrefs() error {
	if pg.preferences.MinTrayStart || pg.preferences.MinTrayClose {
		if pg.tray == nil {
			if err := pg.setupTray(); err != nil {
				pg.preferences.MinTrayStart, pg.preferences.MinTrayClose = false, false
				return err
			}
		}
	} else {
		pg.teardownTray()
		if pg.win != nil {
			// 不留无入口的隐藏主窗口。
			pg.win.Show()
		}
	}
	pg.saveAppPreferences()
	return nil
}

func (pg *mainPage) selectedThemeMode() string {
	if pg.themeMode == "light" || pg.themeMode == "dark" || pg.themeMode == "system" {
		return pg.themeMode
	}
	if pg.dark {
		return "dark"
	}
	return "light"
}

func (pg *mainPage) setDarkTheme(dark bool) {
	mode := "light"
	if dark {
		mode = "dark"
	}
	pg.setThemeMode(mode)
}

func (pg *mainPage) setThemeMode(mode string) {
	if mode != "light" && mode != "dark" && mode != "system" {
		return
	}
	pg.themeMode = mode
	applyNativeThemeMode(mode)
	pg.dark = mode == "dark"
	if mode == "system" && mygo.App.IsReady() {
		pg.dark = mygo.Theme.IsDark()
	}
	pg.syncExtensionTheme()
	saveThemeMode(mode)
	pg.refreshSettingsWindow()
	if pg.win != nil {
		pg.win.Invalidate()
	}
}
