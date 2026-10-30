package main

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openDL/download"
	"openDL/i18n"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type settingsPage struct {
	updateDraft                                            updateDraft
	owner                                                  *mainPage
	win                                                    *mygo.Window
	section, query                                         string
	searchSection, previousQuery                           string
	shortcutEditing, shortcutError                         string
	shortcutHandles                                        [10]ui.Handle
	navList                                                ui.Handle
	navRows                                                [5]ui.Handle
	sidebarWidth                                           float32
	theme, connections, maxconn, detail                    string
	language                                               string
	directory, rate, schedule                              string
	scheduleTime                                           time.Time
	scheduleHour, scheduleMinute                           string
	schedulePickerOpen                                     bool
	quotaPeriod, quotaMB                                   string
	autoStart                                              bool
	directoryDirty, rateDirty, scheduleDirty               bool
	directoryError, rateError, scheduleError               string
	quotaDirty                                             bool
	quotaError                                             string
	categoryDirDraft                                       [6]string
	categoryDirDirty                                       bool
	categoryDirError                                       string
	categoryDirErrors                                      map[string]string
	categoryExtDraft                                       [5]string
	categoryExtDirty                                       bool
	categoryExtError                                       string
	categoryExtErrors                                      map[string]string
	customCatDraft                                         []customCatRow
	customCatDirty                                         bool
	customCatError                                         string
	queueDraft                                             []queueRow
	queueDirty                                             bool
	queueError                                             string
	queueBaseErrors                                        map[string]string
	finishDays                                             [7]bool
	finishDaysError                                        string
	autostartError                                         string
	trayStartError, trayCloseError                         string
	proxyMode, proxyAddress, proxyBypass                   string
	proxyHTTPAddress, proxyHTTPSAddress                    string
	proxyIntegrated                                        bool
	proxyUser, proxyPassword, proxyError                   string
	proxyRemember, proxyDirty, proxySaving                 bool
	proxyRevision                                          uint64
	siteLogins                                             []siteLoginEntry
	loginHost, loginUser, loginPassword                    string
	loginError                                             string
	windowsOrigin, windowsPolicy, windowsSPN, windowsError string
	mediaMuxDraft, mediaMuxError                           string
	siteRulesDraft, siteRulesError                         string
	siteRulesRevision                                      uint64
	queueMainRate, queueNightRate                          string
	queueMainLimit, queueNightLimit                        string
	siteConnDraft                                          []siteConnRow
	siteConnError                                          string
	siteConnDirty                                          bool
	advancedOpen                                           map[string]bool
	nextDraftKey                                           uint64
}

const (
	settingsSidebarDefault float32 = 190
	settingsSidebarMin     float32 = 150
	settingsSidebarMax     float32 = 360
	settingsContentMin     float32 = 440
	settingsControlWidth   float32 = 140
)

type settingsEntry struct{ id, section, title, description, keywords string }

// categoryDirIDs 是分类保存目录设置的展示顺序。
var categoryDirIDs = []string{"program", "doc", "music", "video", "archive", "other"}

// categoryExtIDs 是分类扩展名规则的展示顺序。
var categoryExtIDs = []string{"program", "doc", "music", "video", "archive"}

// customCatRow 是自定义分类的编辑草稿。
type customCatRow struct {
	label, exts, dir string
	err              string
	committed        bool
	key              uint64
	last             customCategorySpec
}

// siteConnRow 是每站点连接上限的编辑草稿。
type siteConnRow struct {
	host, limit string
	err         string
	key         uint64
	committed   bool
	last        siteConnSpec
}

// queueRow 是自定义队列的编辑草稿。
type queueRow struct {
	id, name, limit, rate string
	err                   string
	key                   uint64
}

func (s *settingsPage) newDraftKey() uint64 {
	s.nextDraftKey++
	return s.nextDraftKey
}

// categoryTitle 返回分类设置行显示的分类名称。
func categoryTitle(id string) string {
	for _, entry := range categories {
		if entry.id == id {
			return entry.label
		}
	}
	return id
}

var settingsEntries = []settingsEntry{
	{"updates", "常规", "应用更新", "使用可信发布源检查更新，安装失败时尝试恢复程序文件。", "更新 版本 发布 签名 自动 update version release signature"},
	{"language", "常规", "语言", "选择应用显示语言，立即生效。", "语言 language English 中文"},
	{"appearance", "界面", "外观", "选择主题或跟随系统。", "主题 深色 浅色 跟随系统 颜色"},
	{"accent", "界面", "主题色", "选择全局强调色，适配亮色和深色主题。", "颜色 主题色 色盘 强调色 radix color accent"},
	{"clipboard", "下载", "剪贴板监听", "检测到剪贴板链接时打开添加下载对话框确认。", "剪贴板 监听 链接"},
	{"drag-file-action", "下载", "拖出文件时", "将已完成的文件拖到窗口外时使用的操作。移动后原路径可能失效。", "拖拽 拖放 文件 复制 移动"},
	{"file-exists", "下载", "文件已存在时", "下载目录已有同名文件时，覆盖原文件或创建副本。", "重名 覆盖 副本 已存在 同名 duplicate overwrite"},
	{"notify-system", "常规", "系统通知", "任务下载完成时显示系统通知。", "通知 提醒 系统 完成 声音 notify system"},
	{"notify-app", "常规", "应用通知", "任务下载完成时在屏幕右下角弹出完成卡片。", "通知 提醒 应用 卡片 弹窗 完成 notify card"},
	{"autostart", "常规", "开机自动启动", "登录系统后自动启动 OpenDL。", "开机 自动 启动 登录 autostart"},
	{"min-tray-start", "常规", "启动时最小化到托盘", "启动后主窗口不显示，驻留托盘，可从托盘重新打开。", "托盘 启动 最小化 隐藏 驻留 tray start"},
	{"min-tray-close", "常规", "关闭时最小化到托盘", "关闭主窗口后驻留托盘继续运行，可从托盘重新打开或退出。", "托盘 关闭 最小化 后台 运行 驻留 tray close background"},
	{"directory", "下载", "默认保存位置", "新下载默认保存到此文件夹，已有任务不受影响。", "目录 路径 文件夹 保存"},
	{"connections", "下载", "默认连接数", "新任务默认使用的连接数，可在添加下载时单独调整。", "分段 连接 数量"},
	{"max-connections", "下载", "最大连接数", "所有任务合计的连接数上限，0 表示无限制。", "连接 上限 并发 全局"},
	{"unverified-resume", "下载", "允许未校验续传", "没有 SHA-256 时，允许在无法确认文件一致的情况下保留已下载分段；文件可能损坏。", "续传 分段 校验 风险 SHA-256"},
	{"media-mux", "下载", "媒体合并程序", "独立音视频轨需要本机 FFmpeg；留空时从系统 PATH 查找。", "媒体 音轨 合并 FFmpeg 路径"},
	{"category-dirs", "下载", "分类保存目录", "按文件类型归档到默认保存目录下的子目录。", "分类 目录 归档 子目录 category"},
	{"category-exts", "下载", "分类扩展名规则", "按分类自定义识别的文件扩展名，留空恢复内置规则。", "分类 扩展名 文件类型 规则 category"},
	{"custom-categories", "下载", "自定义分类", "按扩展名自定义分类，可选归档子目录。", "自定义 分类 扩展名 目录 category"},
	{"post-scan", "下载", "扫描程序", "下载完成后调用该程序检查文件，留空不启用。", "扫描 杀毒 防病毒 病毒 检查 scan"},
	{"scan-args", "下载", "扫描参数", "空格分隔，{file} 表示文件路径；留空时文件作为唯一参数。", "参数 命令行 占位符 args"},
	{"proxy", "网络", "下载代理", "", "网络 代理 HTTP HTTPS SOCKS5 直连 环境 认证 用户名 密码 排除"},
	{"site-logins", "网络", "站点登录", "匹配站点的下载自动使用保存的登录凭据。", "登录 站点 认证 账号 用户名 密码 login"},
	{"windows-auth", "网络", "系统集成认证", "仅对明确配置的站点使用当前系统用户身份。", "Kerberos NTLM SSPI GSSAPI Negotiate SSO Windows Linux macOS"},
	{"site-rules", "网络", "站点规则源", "定时从 HTTPS 地址同步站点识别规则，本地规则优先。", "站点 规则 远程 同步 下发 rules"},
	{"site-conns", "网络", "站点连接数", "为指定站点限制下载连接数，匹配含子域。", "站点 连接 并发 上限 site"},
	{"speed", "下载", "下载速度限制", "限制所有任务合计速度，输入 0 表示无限制。", "速度 限速 无限制 网络 MB/s 流量"},
	{"traffic-quota", "下载", "流量配额", "按日、周或月限制所有任务的正文流量，用完暂停到下个周期。", "速度 流量 配额 限额 每日 每周 每月 quota"},
	{"night-enabled", "计划", "夜间队列", "按计划启动夜间队列中的任务。", "计划 队列 定时 启用"},
	{"night-time", "计划", "开始时间", "夜间队列按本机时间启动。", "计划 时间 定时 HH:mm"},
	{"finish-action", "计划", "全部完成后", "所有队列结束后执行的动作。", "完成 关机 睡眠 运行 程序 动作 shutdown"},
	{"finish-program", "计划", "完成后运行程序", "“全部完成后”选择运行程序时执行的程序。", "运行 程序 命令 执行 program"},
	{"finish-args", "计划", "运行参数", "空格分隔的程序参数。", "参数 命令行 args"},
	{"custom-queues", "计划", "队列管理", "自定义队列及其并发上限，留空表示只受全局上限约束。", "队列 并发 上限 调度 queue"},
	{"detail", "界面", "详情面板位置", "选择任务详情显示在底部或右侧。", "停靠 侧栏 底部"},
	{"shortcuts", "界面", "键盘快捷键", "查看并调整全局快捷键。", "快捷键 键盘 组合键 shortcut keyboard"},
	{"reset-layout", "界面", "面板尺寸", "恢复侧栏和详情面板的默认尺寸。", "重置 宽度 高度"},
}

// settingsGroup 是一张设置卡片：同类设置共用一张卡片，组内每项一行，
// 组标题显示在卡片外。只有一项的组不显示组标题，行名即卡片标题。
type settingsGroup struct {
	title   string
	section string
	ids     []string
}

// settingsGroups 是设置卡片的展示顺序。
var settingsGroups = []settingsGroup{
	{"常规", "常规", []string{"language", "notify-system", "notify-app", "autostart", "min-tray-start", "min-tray-close"}},
	{"应用更新", "常规", []string{"updates"}},

	{"外观", "界面", []string{"appearance"}},
	{"主题色", "界面", []string{"accent"}},
	{"面板", "界面", []string{"detail", "reset-layout"}},
	{"键盘快捷键", "界面", []string{"shortcuts"}},

	{"下载", "下载", []string{"directory", "clipboard", "drag-file-action", "file-exists", "connections", "max-connections", "unverified-resume"}},
	{"速度与流量", "下载", []string{"speed", "traffic-quota"}},
	{"媒体合并程序", "下载", []string{"media-mux"}},
	{"分类保存目录", "下载", []string{"category-dirs"}},
	{"分类扩展名规则", "下载", []string{"category-exts"}},
	{"自定义分类", "下载", []string{"custom-categories"}},
	{"下载后扫描", "下载", []string{"post-scan", "scan-args"}},

	{"下载代理", "网络", []string{"proxy"}},
	{"站点登录", "网络", []string{"site-logins"}},
	{"系统集成认证", "网络", []string{"windows-auth"}},
	{"站点规则", "网络", []string{"site-rules"}},
	{"站点连接", "网络", []string{"site-conns"}},

	{"夜间队列", "计划", []string{"night-enabled", "night-time"}},
	{"完成后动作", "计划", []string{"finish-action", "finish-program", "finish-args"}},
	{"队列管理", "计划", []string{"custom-queues"}},
}

// settingsEntryByID 返回指定编号的设置项。
func settingsEntryByID(id string) settingsEntry {
	for _, entry := range settingsEntries {
		if entry.id == id {
			return entry
		}
	}
	return settingsEntry{id: id, title: id}
}

// settingsEntryMatches 报告设置项是否命中搜索词。
func settingsEntryMatches(entry settingsEntry, query string) bool {
	text := entry.section + " " + entry.title + " " + entry.description + " " + entry.keywords +
		" " + tr(entry.section) + " " + tr(entry.title) + " " + tr(entry.description) + " " + tr(entry.keywords)
	return strings.Contains(strings.ToLower(text), query)
}

// syncCategoryDirs 读取分类归档设置作为编辑草稿。
func (s *settingsPage) syncCategoryDirs() {
	for i, id := range categoryDirIDs {
		s.categoryDirDraft[i] = s.owner.preferences.CategoryDirs[id]
	}
	s.categoryDirError = ""
}

// selectedFinishDays 把完成后动作的重复日期勾选转成偏好存储；全部不勾选
// 等价于每天。
func (s *settingsPage) selectedFinishDays() []int {
	days := make([]int, 0, 7)
	for i, on := range s.finishDays {
		if on {
			days = append(days, i)
		}
	}
	if len(days) == 0 {
		return nil
	}
	return normalizeNightDays(days)
}

// syncFinishDays 读取完成后动作的重复日期草稿。
func (s *settingsPage) syncFinishDays() {
	for i := range s.finishDays {
		on := len(s.owner.preferences.FinishDays) == 0
		for _, day := range s.owner.preferences.FinishDays {
			if day == i {
				on = true
				break
			}
		}
		s.finishDays[i] = on
	}
}

// syncCustomQueues 读取自定义队列作为编辑草稿。
func (s *settingsPage) syncCustomQueues() {
	keys := make(map[string]uint64, len(s.queueDraft))
	for _, row := range s.queueDraft {
		if row.id != "" {
			keys[row.id] = row.key
		}
	}
	s.queueDraft = s.queueDraft[:0]
	for _, spec := range s.owner.preferences.CustomQueues {
		limit := ""
		if spec.Limit > 0 {
			limit = strconv.Itoa(spec.Limit)
		}
		rate := ""
		if spec.Rate > 0 {
			rate = strconv.Itoa(spec.Rate)
		}
		key := keys[spec.ID]
		if key == 0 {
			key = s.newDraftKey()
		}
		s.queueDraft = append(s.queueDraft, queueRow{id: spec.ID, name: spec.Name, limit: limit, rate: rate, key: key})
	}
	s.queueMainRate, s.queueNightRate = "", ""
	if s.owner.preferences.MainQueueRate > 0 {
		s.queueMainRate = strconv.Itoa(s.owner.preferences.MainQueueRate)
	}
	if s.owner.preferences.NightQueueRate > 0 {
		s.queueNightRate = strconv.Itoa(s.owner.preferences.NightQueueRate)
	}
	s.queueMainLimit, s.queueNightLimit = "", ""
	if s.owner.preferences.MainQueueLimit > 0 {
		s.queueMainLimit = strconv.Itoa(s.owner.preferences.MainQueueLimit)
	}
	if s.owner.preferences.NightQueueLimit > 0 {
		s.queueNightLimit = strconv.Itoa(s.owner.preferences.NightQueueLimit)
	}
	s.queueError = ""
}

// syncSiteConns 读取每站点连接上限作为编辑草稿。
func (s *settingsPage) syncSiteConns() {
	keys := make(map[string]uint64, len(s.siteConnDraft))
	for _, row := range s.siteConnDraft {
		keys[row.host] = row.key
	}
	s.siteConnDraft = s.siteConnDraft[:0]
	for _, spec := range s.owner.preferences.SiteConnections {
		key := keys[spec.Host]
		if key == 0 {
			key = s.newDraftKey()
		}
		s.siteConnDraft = append(s.siteConnDraft, siteConnRow{host: spec.Host, limit: strconv.Itoa(spec.Limit), key: key, committed: true, last: spec})
	}
	s.siteConnError = ""
}

// syncCustomCategories 读取自定义分类作为编辑草稿。
func (s *settingsPage) syncCustomCategories() {
	keys := make(map[string][]uint64, len(s.customCatDraft))
	for _, row := range s.customCatDraft {
		if row.committed {
			keys[row.label] = append(keys[row.label], row.key)
		}
	}
	s.customCatDraft = s.customCatDraft[:0]
	for _, spec := range s.owner.preferences.CustomCategories {
		var key uint64
		if candidates := keys[spec.Label]; len(candidates) > 0 {
			key = candidates[0]
			keys[spec.Label] = candidates[1:]
		}
		if key == 0 {
			key = s.newDraftKey()
		}
		s.customCatDraft = append(s.customCatDraft, customCatRow{label: spec.Label, exts: fileTypesText(spec.Exts), dir: spec.Dir, committed: true, key: key, last: spec})
	}
	s.customCatError = ""
}

// 未自定义的分类留空，避免编辑另一项时把内置规则意外保存为覆盖值。
func (s *settingsPage) syncCategoryExts() {
	for i, id := range categoryExtIDs {
		exts := s.owner.preferences.CategoryExts[id]
		s.categoryExtDraft[i] = fileTypesText(exts)
	}
	s.categoryExtError = ""
}

func (pg *mainPage) openSettings() {
	if pg.settingsWindow != nil && pg.settingsWindow.win != nil {
		pg.settingsWindow.win.Show()
		pg.settingsWindow.win.Focus()
		return
	}
	if pg.win == nil || !mygo.App.IsReady() {
		pg.flash(tr("设置窗口需要桌面应用"))
		return
	}
	s := &settingsPage{owner: pg, section: "常规", directory: pg.saveDir, rate: "0", schedule: pg.nightAt}
	s.scheduleTime, _ = time.Parse("15:04", pg.nightAt)
	s.scheduleHour, s.scheduleMinute = s.scheduleTime.Format("15"), s.scheduleTime.Format("04")
	if pg.limitOn {
		s.rate = pg.limitValue
	}
	s.quotaPeriod = pg.preferences.QuotaPeriod
	s.quotaMB = strconv.Itoa(pg.preferences.QuotaMB)
	s.syncCategoryDirs()
	s.syncCategoryExts()
	s.syncCustomCategories()
	s.syncCustomQueues()
	s.syncSiteConns()
	s.syncFinishDays()
	s.theme = pg.selectedThemeMode()
	s.language = i18n.Locale()
	s.syncUpdateDraft()
	s.mediaMuxDraft = pg.preferences.MediaMuxProgram
	s.siteRulesDraft, s.siteRulesError = pg.preferences.SiteRulesURL, ""
	s.connections = strconv.Itoa(pg.preferences.Connections)
	s.maxconn = strconv.Itoa(pg.preferences.MaxConnections)
	s.autoStart = autoStartEnabled()
	s.detail = pg.detailPlacement
	s.syncProxy()
	s.syncSiteLogins()
	pg.settingsWindow = s
	s.win = mygo.NewWindow(mygo.WindowOptions{
		Title: tr("设置"), Width: 840, Height: 620, MinWidth: 680, MinHeight: 500,
		Parent: pg.win, StateKey: "settings", Content: ui.View(s.view),
		TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 38,
	})
	s.win.OnClosed(func() {
		s.loginPassword = ""
		if !s.proxySaving {
			s.proxyPassword = ""
		}
		if pg.settingsWindow == s {
			pg.settingsWindow = nil
		}
		s.win = nil
	})
}

// Both windows build on the UI thread. Draft fields are not overwritten while
// the user is editing; selections and untouched fields follow the main page.
func (pg *mainPage) refreshSettingsWindow() {
	s := pg.settingsWindow
	if s == nil {
		return
	}
	s.theme = pg.selectedThemeMode()
	s.language = i18n.Locale()
	s.syncUpdateDraft()
	s.connections = strconv.Itoa(pg.preferences.Connections)
	s.maxconn = strconv.Itoa(pg.preferences.MaxConnections)
	s.autoStart = autoStartEnabled()
	s.detail = pg.detailPlacement
	if !s.proxyDirty && !s.proxySaving {
		s.syncProxy()
	}
	if !s.directoryDirty {
		s.directory = pg.saveDir
	}
	if !s.rateDirty {
		s.rate = "0"
		if pg.limitOn {
			s.rate = pg.limitValue
		}
	}
	if !s.quotaDirty {
		s.quotaPeriod = pg.preferences.QuotaPeriod
		s.quotaMB = strconv.Itoa(pg.preferences.QuotaMB)
	}
	if !s.categoryDirDirty {
		s.syncCategoryDirs()
	}
	if !s.categoryExtDirty {
		s.syncCategoryExts()
	}
	if !s.customCatDirty {
		s.syncCustomCategories()
	}
	if !s.queueDirty {
		s.syncCustomQueues()
	}
	if !s.siteConnDirty {
		s.syncSiteConns()
	}
	s.syncFinishDays()
	if !s.scheduleDirty {
		s.schedule = pg.nightAt
		if parsed, err := time.Parse("15:04", pg.nightAt); err == nil {
			s.scheduleTime = parsed
			s.scheduleHour, s.scheduleMinute = parsed.Format("15"), parsed.Format("04")
		}
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

func (s *settingsPage) changed() {
	if s.owner.win != nil {
		s.owner.win.Invalidate()
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

func (s *settingsPage) applySiteRules() {
	s.siteRulesRevision++
	revision := s.siteRulesRevision
	value := strings.TrimSpace(s.siteRulesDraft)
	if value != "" {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			s.siteRulesError = tr("规则源必须使用不含登录凭据的 HTTPS URL")
			s.changed()
			return
		}
	}
	s.owner.preferences.SiteRulesURL = value
	s.owner.saveAppPreferences()
	setSiteRulesSource(value)
	s.siteRulesError = ""
	s.changed()
	if value == "" || s.owner.win == nil {
		return
	}
	win, owner := s.owner.win, s.owner
	time.AfterFunc(time.Second, func() {
		win.Update(func() {
			select {
			case <-owner.stop:
				return
			default:
			}
			if revision != s.siteRulesRevision || currentSiteRulesSource() != value {
				return
			}
			go func() {
				err := syncSiteRulesRemote()
				if err == nil {
					return
				}
				win.Update(func() {
					select {
					case <-owner.stop:
						return
					default:
					}
					if revision == s.siteRulesRevision {
						s.siteRulesError = err.Error()
						s.changed()
					}
				})
			}()
		})
	})
}

// Invalid drafts stay visible while the last valid setting continues to apply.
func (s *settingsPage) applySiteConnections() {
	s.siteConnDirty = true
	specs := make([]siteConnSpec, 0, len(s.siteConnDraft))
	seen := map[string]bool{}
	reserved := map[string]uint64{}
	for _, row := range s.siteConnDraft {
		if row.committed {
			reserved[row.last.Host] = row.key
		}
	}
	for index, row := range s.siteConnDraft {
		s.siteConnDraft[index].err = ""
		host := strings.ToLower(strings.TrimSpace(row.host))
		if host == "" && strings.TrimSpace(row.limit) == "" {
			if !row.committed {
				continue
			}
		}
		rowError := ""
		if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\:@ ?") || seen[host] || (reserved[host] != 0 && reserved[host] != row.key) {
			rowError = tr("请填写有效站点")
		}
		value, err := strconv.Atoi(strings.TrimSpace(row.limit))
		if err != nil || value < 1 || value > 64 {
			if rowError == "" {
				rowError = tr("连接数需要在 1–64 之间：") + host
			}
		}
		if rowError != "" {
			s.siteConnDraft[index].err = rowError
			if row.committed {
				specs = append(specs, row.last)
				seen[row.last.Host] = true
			}
			continue
		}
		seen[host] = true
		spec := siteConnSpec{Host: host, Limit: value}
		specs = append(specs, spec)
		s.siteConnDraft[index].committed = true
		s.siteConnDraft[index].last = spec
	}
	s.owner.preferences.SiteConnections = specs
	if s.owner.mgr != nil {
		s.owner.mgr.SetSiteConnectionLimits(siteConnectionTable(specs))
	}
	s.siteConnError = ""
	s.owner.saveAppPreferences()
	s.changed()
}

func (s *settingsPage) applyBaseQueueSetting(text, queue string, rate bool) {
	s.queueDirty = true
	if s.queueBaseErrors == nil {
		s.queueBaseErrors = make(map[string]string)
	}
	field := queue + "-limit"
	if rate {
		field = queue + "-rate"
	}
	value := 0
	if input := strings.TrimSpace(text); input != "" {
		parsed, err := strconv.Atoi(input)
		maxValue, message := 64, "并发上限需要在 1–64 之间"
		if rate {
			maxValue, message = 1048576, "限速需要在 1–1048576 KiB/s 之间"
		}
		if err != nil || parsed < 1 || parsed > maxValue {
			s.queueBaseErrors[field] = tr(message)
			s.changed()
			return
		}
		value = parsed
	}
	if queue == "main" {
		if rate {
			s.owner.preferences.MainQueueRate = value
		} else {
			s.owner.preferences.MainQueueLimit = value
		}
	} else {
		if rate {
			s.owner.preferences.NightQueueRate = value
		} else {
			s.owner.preferences.NightQueueLimit = value
		}
	}
	if s.owner.mgr != nil {
		s.owner.mgr.SetQueueLimits(queueLimitTable(s.owner.preferences, s.owner.preferences.CustomQueues))
		s.owner.mgr.SetQueueRates(queueRateTable(s.owner.preferences, s.owner.preferences.CustomQueues))
	}
	delete(s.queueBaseErrors, field)
	s.owner.saveAppPreferences()
	s.changed()
}

func (s *settingsPage) applyQueuesDraft() {
	s.queueDirty = true
	specs := make([]customQueueSpec, 0, len(s.queueDraft))
	previous := make(map[string]customQueueSpec, len(s.owner.preferences.CustomQueues))
	reservedIDs := make(map[string]bool, len(s.owner.preferences.CustomQueues))
	for _, spec := range s.owner.preferences.CustomQueues {
		previous[spec.ID] = spec
		reservedIDs[spec.ID] = true
	}
	// Imported settings may contain an older sequence; never reuse an ID still
	// attached to a task, including one whose queue was removed from settings.
	if s.owner.mgr != nil {
		for _, task := range s.owner.mgr.Snapshots() {
			reservedIDs[task.Queue] = true
		}
	}
	sequence := s.owner.preferences.QueueSeq
	for index, row := range s.queueDraft {
		s.queueDraft[index].err = ""
		name := strings.TrimSpace(row.name)
		if name == "" && strings.TrimSpace(row.limit) == "" && strings.TrimSpace(row.rate) == "" && row.id == "" {
			continue
		}
		rowError := ""
		if name == "" || len([]rune(name)) > 24 {
			rowError = tr("请填写不超过 24 字的队列名称")
		}
		limit := 0
		if text := strings.TrimSpace(row.limit); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || value < 1 || value > 64 {
				if rowError == "" {
					rowError = tr("并发上限需要在 1–64 之间：") + name
				}
			} else {
				limit = value
			}
		}
		rate := 0
		if text := strings.TrimSpace(row.rate); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || value < 1 || value > 1048576 {
				if rowError == "" {
					rowError = tr("限速需要在 1–1048576 KiB/s 之间：") + name
				}
			} else {
				rate = value
			}
		}
		if rowError != "" {
			s.queueDraft[index].err = rowError
			if old, ok := previous[row.id]; ok {
				specs = append(specs, old)
			}
			continue
		}
		id := row.id
		if id == "" {
			for {
				sequence++
				id = "q" + strconv.Itoa(sequence)
				if !reservedIDs[id] {
					break
				}
			}
			reservedIDs[id] = true
		}
		specs = append(specs, customQueueSpec{ID: id, Name: name, Limit: limit, Rate: rate})
		s.queueDraft[index].id = id
	}
	s.owner.preferences.QueueSeq = sequence
	s.owner.preferences.CustomQueues = specs
	applyQueues(s.owner.preferences)
	if s.owner.mgr != nil {
		s.owner.mgr.SetQueueLimits(queueLimitTable(s.owner.preferences, specs))
		s.owner.mgr.SetQueueRates(queueRateTable(s.owner.preferences, specs))
	}
	s.queueError = ""
	s.owner.saveAppPreferences()
	s.changed()
}

func (s *settingsPage) applyRate() {
	s.rateDirty = true
	value, err := strconv.ParseFloat(strings.TrimSpace(s.rate), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 10240 || (value > 0 && value*float64(1<<20) < 1) {
		s.rateError = tr("请输入 0 或不超过 10240 的有效速度")
		s.changed()
		return
	}
	s.rateError = ""
	s.owner.setLimitValue(strconv.FormatFloat(value, 'f', -1, 64), false)
	s.rateDirty = false
	s.changed()
}

func (s *settingsPage) applyQuota() {
	s.quotaDirty = true
	mb := 0
	if s.quotaPeriod != "" {
		var err error
		mb, err = strconv.Atoi(strings.TrimSpace(s.quotaMB))
		if err != nil {
			s.quotaError = tr("额度需要填写整数 MB")
			s.changed()
			return
		}
	}
	if err := s.owner.applyTrafficQuota(s.quotaPeriod, mb); err != nil {
		s.quotaError = tr("无法应用流量配额：") + err.Error()
		s.changed()
		return
	}
	s.quotaError = ""
	s.quotaDirty = false
	s.changed()
}

func (s *settingsPage) applySchedule() {
	s.scheduleDirty = true
	parsed, err := time.Parse("15:04", strings.TrimSpace(s.schedule))
	if err != nil {
		s.scheduleError = tr("请输入有效时间，例如 01:00")
		s.changed()
		return
	}
	s.owner.nightAt = parsed.Format("15:04")
	s.scheduleError = ""
	s.owner.saveAppPreferences()
	s.scheduleDirty = false
	s.changed()
}

// 下载设置填入直接生效：输入变化即校验并应用，不需要「应用」按钮；
// 无效输入保留上一次生效值并提示原因，修正后立即恢复生效。

// applyDirectory 立即应用默认保存位置；非绝对路径不生效。
func (s *settingsPage) applyDirectory() {
	s.directoryDirty = true
	if !s.owner.setDefaultDirectory(s.directory) {
		s.directoryError = tr("请输入绝对路径")
		s.changed()
		return
	}
	s.directoryError = ""
	s.directoryDirty = false
	s.changed()
}

// applyMediaMux 立即应用媒体合并程序路径；语法不合法时保留原配置。
func (s *settingsPage) applyMediaMux() {
	value, err := download.NormalizeMediaMuxProgram(s.mediaMuxDraft)
	if err != nil {
		s.mediaMuxError = err.Error()
		s.changed()
		return
	}
	s.mediaMuxError = ""
	s.owner.preferences.MediaMuxProgram = value
	if s.owner.mgr != nil {
		s.owner.mgr.SetMediaMuxProgram(value)
	}
	s.owner.saveAppPreferences()
	s.changed()
}

// applyCategoryDir 立即应用有效的分类归档子目录，其他行保持不变。
func (s *settingsPage) applyCategoryDir(index int) {
	s.categoryDirDirty = true
	if s.categoryDirErrors == nil {
		s.categoryDirErrors = make(map[string]string)
	}
	id := categoryDirIDs[index]
	value := strings.TrimSpace(s.categoryDirDraft[index])
	if value != "" && download.CleanCategoryDir(value) == "" {
		s.categoryDirErrors[id] = tr("子目录需要是默认目录下的相对路径：") + categoryTitle(id)
		s.changed()
		return
	}
	dirs := make(map[string]string, len(s.owner.preferences.CategoryDirs))
	for key, item := range s.owner.preferences.CategoryDirs {
		dirs[key] = item
	}
	if value == "" {
		delete(dirs, id)
	} else {
		dirs[id] = value
	}
	s.owner.preferences.CategoryDirs = dirs
	byExt, fallback := categoryDirTable(dirs)
	if s.owner.mgr != nil {
		s.owner.mgr.SetCategoryDirs(byExt, fallback)
	}
	delete(s.categoryDirErrors, id)
	s.owner.saveAppPreferences()
	s.changed()
}

// applyCategoryExt 立即应用有效的分类扩展名规则，其他行保持不变。
func (s *settingsPage) applyCategoryExt(index int) {
	s.categoryExtDirty = true
	if s.categoryExtErrors == nil {
		s.categoryExtErrors = make(map[string]string)
	}
	id := categoryExtIDs[index]
	text := strings.TrimSpace(s.categoryExtDraft[index])
	overrides := make(map[string][]string, len(s.owner.preferences.CategoryExts))
	for key, item := range s.owner.preferences.CategoryExts {
		overrides[key] = item
	}
	if text == "" {
		delete(overrides, id)
	} else {
		parsed, err := parseFileTypes(text)
		if err != nil {
			s.categoryExtErrors[id] = tr("扩展名格式无效：") + categoryTitle(id)
			s.changed()
			return
		}
		if len(parsed) == 0 {
			s.categoryExtErrors[id] = tr("请至少填写一个扩展名：") + categoryTitle(id)
			s.changed()
			return
		}
		exts := make([]string, 0, len(parsed))
		for _, name := range parsed {
			exts = append(exts, "."+name)
		}
		overrides[id] = exts
	}
	s.owner.preferences.CategoryExts = overrides
	applyCategories(s.owner.preferences)
	byExt, fallback := categoryDirTable(s.owner.preferences.CategoryDirs)
	if s.owner.mgr != nil {
		s.owner.mgr.SetCategoryDirs(byExt, fallback)
	}
	delete(s.categoryExtErrors, id)
	s.owner.saveAppPreferences()
	s.changed()
}

// 每行独立生效；未完成的新行不提交，已有行无效时保留上次有效配置。
func (s *settingsPage) applyCustomCategories() {
	s.customCatDirty = true
	specs := make([]customCategorySpec, 0, len(s.customCatDraft))
	for index, row := range s.customCatDraft {
		s.customCatDraft[index].err = ""
		label := strings.TrimSpace(row.label)
		exts := strings.TrimSpace(row.exts)
		if label == "" || exts == "" {
			if row.committed || label != "" || exts != "" || strings.TrimSpace(row.dir) != "" {
				s.customCatDraft[index].err = tr("请填写分类名称和扩展名")
			}
			if row.committed {
				specs = append(specs, row.last)
			}
			continue
		}
		if len([]rune(label)) > 24 {
			s.customCatDraft[index].err = tr("请填写不超过 24 字的分类名称")
			if row.committed {
				specs = append(specs, row.last)
			}
			continue
		}
		parsed, err := parseFileTypes(exts)
		if err != nil || len(parsed) == 0 {
			s.customCatDraft[index].err = tr("扩展名格式无效：") + label
			if row.committed {
				specs = append(specs, row.last)
			}
			continue
		}
		extList := make([]string, 0, len(parsed))
		for _, name := range parsed {
			extList = append(extList, "."+name)
		}
		dir := download.CleanCategoryDir(row.dir)
		if strings.TrimSpace(row.dir) != "" && dir == "" {
			s.customCatDraft[index].err = tr("子目录需要是默认目录下的相对路径：") + label
			if row.committed {
				specs = append(specs, row.last)
			}
			continue
		}
		spec := customCategorySpec{Label: label, Exts: extList, Dir: dir}
		specs = append(specs, spec)
		s.customCatDraft[index].committed = true
		s.customCatDraft[index].last = spec
	}
	s.owner.preferences.CustomCategories = specs
	applyCategories(s.owner.preferences)
	byExt, fallback := categoryDirTable(s.owner.preferences.CategoryDirs)
	if s.owner.mgr != nil {
		s.owner.mgr.SetCategoryDirs(byExt, fallback)
	}
	s.customCatError = ""
	s.owner.saveAppPreferences()
	s.changed()
}

func (s *settingsPage) view(c *ui.Context) {
	p := d1AccentPalette(s.owner.dark, s.owner.preferences.AccentColor)
	c.SetTheme(p.theme())
	query := strings.ToLower(strings.TrimSpace(s.query))
	if query != s.previousQuery {
		s.searchSection = ""
		s.previousQuery = query
	}
	matchingSections := map[string]bool{}
	if query != "" {
		for _, group := range settingsGroups {
			for _, id := range group.ids {
				if settingsEntryMatches(settingsEntryByID(id), query) {
					matchingSections[group.section] = true
				}
			}
		}
	}
	preferredWidth := s.owner.preferences.SettingsSidebarWidth
	if preferredWidth < settingsSidebarMin || preferredWidth > settingsSidebarMax || math.IsNaN(float64(preferredWidth)) {
		preferredWidth = settingsSidebarDefault
	}
	width, _ := c.Size()
	upper := min(settingsSidebarMax, width-settingsContentMin-1)
	s.sidebarWidth = clampPanel(preferredWidth, settingsSidebarMin, upper)
	ui.Column(c).Fill().Font(style.FontFamily).FontSize(style.TextBase).FontWeight(style.WeightRegular).
		LineHeight(style.LineHeight).Background(p.bg).Children(func() {
		s.titleBar(c, p)
		panelSplit(c, p, "settings-sidebar-split", &s.sidebarWidth, settingsSidebarMin, upper, func() {
			ui.Column(c).Fill().Padding(16, 12).Gap(14).Background(p.card).Children(func() {
				settingsSearchFocused := false
				settingsSearchBox := ui.Row(c).Height(32).Padding(0, 8).Gap(7).AlignItems(ui.Center).Radius(style.RadiusSM).Border(1, p.bd2).Background(p.bg)
				settingsSearchBox.Children(func() {
					d1Icon(c, "search", 16, p.mu)
					c.Key("settings-search")
					settingsSearch := ui.TextInputBase(c, &s.query).Grow(1).Shrink(1).Basis(0).MinWidth(0).
						Label(tr("搜索设置")).Placeholder(tr("搜索设置")).FontSize(style.TextSM).TextColor(p.fg)
					settingsSearchFocused = settingsSearch.Focused()
				})
				if settingsSearchFocused {
					settingsSearchBox.BorderColor(p.pri)
				}
				sections := []struct{ name, icon string }{{"常规", "cog"}, {"界面", "panel-bottom"}, {"下载", "down"}, {"网络", "globe"}, {"计划", "clock"}}
				c.Key("settings-nav-list")
				list := ui.Column(c).Gap(style.NavItemGap).FocusGroup(ui.Vertical).Bind(&s.navList)
				var selected *ui.Handle
				if query == "" {
					for i, section := range sections {
						if section.name == s.section {
							selected = &s.navRows[i]
							break
						}
					}
				}
				slide := navSlidePosition(c, list, &s.navList, selected)
				navSurfaces(c, list, &s.navList, s.navRows[:], []navGroup{{0, len(sections) - 1}}, p, slide)
				list.Children(func() {
					for i, section := range sections {
						if query != "" && !matchingSections[section.name] {
							continue
						}
						c.Key(section.name)
						on := (query == "" && s.section == section.name) || (query != "" && s.searchSection == section.name)
						b := ui.ButtonBase(c).FillWidth().Height(32).Padding(0, 8).Gap(8).AlignItems(ui.Center).Justify(ui.Start).
							Radius(style.RadiusSM).Label(tr(section.name)).Selected(on).Background(ui.Transparent).Cursor(ui.CursorPointer).Bind(&s.navRows[i])
						navMotion(b, p, on, slide)
						b.Children(func() {
							color := p.mu
							weight := style.WeightRegular
							if on {
								color, weight = p.fg, style.WeightMedium
							}
							d1Icon(c, section.icon, 16, color)
							ui.Text(c, tr(section.name)).FontSize(style.TextBase).FontWeight(weight).TextColor(color).SingleLine()
						})
						name := section.name
						b.OnClick(func() {
							if query != "" {
								s.searchSection = name
							} else {
								s.section = name
							}
						})
					}
				})
			})
		}, func() {
			ui.Column(c).Fill().Children(func() {
				if s.owner.settingsSaveError != "" {
					ui.Box(c).FillWidth().Padding(10, 28).Children(func() {
						settingsError(c, p, s.owner.settingsSaveError)
					})
				}
				ui.Scroll(c).FillWidth().Grow(1).Shrink(1).Basis(0).Children(func() {
					ui.Column(c).FillWidth().Padding(24, 28).Gap(14).Children(func() {
						title := s.section
						if query != "" {
							title = "搜索结果"
						}
						ui.Text(c, tr(title)).FontSize(style.TextLG).FontWeight(style.WeightMedium).TextColor(p.fg)
						count := 0
						lastSection := ""
						for _, group := range settingsGroups {
							if query != "" && s.searchSection != "" && group.section != s.searchSection {
								continue
							}
							if query == "" && group.section != s.section {
								continue
							}
							ids := group.ids
							if query != "" {
								matched := make([]string, 0, len(group.ids))
								for _, id := range group.ids {
									if settingsEntryMatches(settingsEntryByID(id), query) {
										matched = append(matched, id)
									}
								}
								if len(matched) == 0 {
									continue
								}
								ids = matched
							}
							if query != "" && group.section != lastSection {
								ui.Text(c, tr(group.section)).FontSize(style.TextSM).FontWeight(style.WeightMedium).TextColor(p.mu).SingleLine()
								lastSection = group.section
							}
							count++
							c.Key(group.ids[0])
							s.settingGroup(c, p, settingsGroup{title: group.title, section: group.section, ids: ids}, len(group.ids) == 1)
						}
						if count == 0 {
							ui.Text(c, tr("没有找到相关设置")).FontSize(style.TextBase).TextColor(p.mu)
						}
					})
				})
			})
		}, func() {
			s.owner.preferences.SettingsSidebarWidth = s.sidebarWidth
			s.owner.saveAppPreferences()
		}).Grow(1).Shrink(1).Basis(0).FillWidth()
	})
}

func (s *settingsPage) titleBar(c *ui.Context, p *d1Palette) {
	tb := c.TitleBar()
	height := tb.Height
	if height <= 0 {
		height = style.HeightTitleBar
	}
	ui.Row(c).Height(height).FillWidth().PaddingStart(12).Gap(8).AlignItems(ui.Center).
		Background(p.card).BorderWidth(0, 0, 1, 0).BorderColor(p.bd).DragWindow().Children(func() {
		if tb.Left > 0 {
			ui.Box(c).Width(tb.Left).Height(height)
		}
		d1Icon(c, "cog", 16, p.fg)
		ui.Text(c, tr("设置")).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg)
		ui.Box(c).Grow(1).Shrink(1).Basis(0)
		ui.Box(c).Width(tb.Right).Height(height)
	})
}

// settingGroup 渲染一组设置卡片：组标题在卡片外，卡片内每项一行。
func (s *settingsPage) settingGroup(c *ui.Context, p *d1Palette, group settingsGroup, singleEntry bool) {
	advanced := false
	switch group.title {
	case "分类保存目录", "分类扩展名规则", "自定义分类", "下载后扫描", "站点登录", "系统集成认证", "站点规则", "站点连接", "队列管理":
		advanced = true
	}
	if advanced && s.advancedOpen == nil {
		s.advancedOpen = make(map[string]bool)
	}
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		if !singleEntry && !advanced && group.title != group.section {
			ui.Text(c, tr(group.title)).FontSize(style.TextSM).FontWeight(style.WeightMedium).TextColor(p.mu).SingleLine()
		}
		rows := func() {
			shown := false
			for _, id := range group.ids {
				if (id == "finish-program" || id == "finish-args") && s.owner.preferences.FinishAction != "run" && strings.TrimSpace(s.query) == "" {
					continue
				}
				if shown {
					ui.Box(c).FillWidth().Height(1).Background(p.bd)
				}
				shown = true
				c.Key(id)
				s.settingRow(c, p, settingsEntryByID(id), advanced && singleEntry)
			}
		}
		if !advanced {
			ui.Column(c).FillWidth().Padding(14, 16).Gap(12).Radius(style.RadiusMD).Border(1, p.bd).Background(p.bg).Children(rows)
			return
		}
		// 折叠卡片的卡头按钮自带卡片内边距，让整个卡头都是点击区域，
		// 而不是只有中间一行文字；折叠时整张卡片即按钮。
		ui.Column(c).FillWidth().Gap(12).Radius(style.RadiusMD).Border(1, p.bd).Background(p.bg).Children(func() {
			id := group.title
			searching := strings.TrimSpace(s.query) != ""
			open := s.advancedOpen[id] || searching
			if searching {
				ui.Text(c, tr(group.title)).FillWidth().Padding(14, 16).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg).SingleLine()
			} else {
				c.Key("advanced-toggle")
				button := ui.ButtonBase(c).FillWidth().Padding(14, 16).Gap(8).AlignItems(ui.Center).Justify(ui.Start).Label(tr(group.title)).Expanded(open)
				rotation := button.Animate("settings-disclosure", 180*motionTarget(open), selectMotion)
				button.Children(func() {
					ui.Text(c, tr(group.title)).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg).SingleLine()
					d1Icon(c, "chevron-down", style.SizeIcon, p.mu).Rotate(rotation)
				})
				button.OnClick(func() { s.advancedOpen[id] = !open })
			}
			if !open {
				return
			}
			ui.Column(c).FillWidth().Padding(0, 16, 14).Gap(12).Children(func() {
				if singleEntry {
					description := settingsEntryByID(group.ids[0]).description
					if description != "" {
						ui.Text(c, tr(description)).FillWidth().FontSize(style.TextSM).TextColor(p.mu)
					}
				}
				rows()
			})
		})
	})
}

// settingRow 是卡片内的一行：设置名称与控件，必要时在下方追加字段。
func (s *settingsPage) settingRow(c *ui.Context, p *d1Palette, entry settingsEntry, hideTitle bool) {
	if (entry.id == "finish-program" || entry.id == "finish-args") && s.owner.preferences.FinishAction != "run" && strings.TrimSpace(s.query) == "" {
		return
	}
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		if !hideTitle {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				tip := tr(entry.title)
				if entry.description != "" {
					tip = tr(entry.description)
				}
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, tr(entry.title)).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg).SingleLine().Tooltip(tip)
					if entry.description != "" {
						ui.Text(c, tr(entry.description)).FillWidth().FontSize(style.TextSM).TextColor(p.mu)
					}
				})
				switch entry.id {
				case "appearance":
					s.themeChoices(c, p)
				case "accent":
					s.accentChoices(c, p)
				case "speed":
					field := ui.Row(c).Width(settingsControlWidth).Height(32).Shrink(0).Padding(0, 10).Gap(8).AlignItems(ui.Center).Radius(style.RadiusSM).Border(1, p.bd2).Background(p.bg)
					field.Children(func() {
						c.Key("settings-rate")
						input := ui.TextInputBase(c, &s.rate).Grow(1).Shrink(1).Basis(0).MinWidth(0).TextColor(p.fg).FontSize(style.TextSM).Label(tr("下载速度限制（MB/s）"))
						if input.Focused() {
							field.BorderColor(p.pri)
						}
						input.OnChange(s.applyRate)
						ui.Text(c, tr("MB/s")).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
					})
				case "traffic-quota":
					d1Choice(c, p, "流量配额", &s.quotaPeriod, []d1Option{{value: "", label: "无限制"}, {value: "day", label: "每日"}, {value: "week", label: "每周"}, {value: "month", label: "每月"}}).Width(settingsControlWidth).Shrink(0).OnChange(s.applyQuota)
				case "night-time":
					s.schedulePicker(c, p)
				case "language":
					d1Choice(c, p, "语言", &s.language, []d1Option{{value: i18n.Chinese, label: "简体中文"}, {value: i18n.English, label: "English"}}).
						Width(settingsControlWidth).Shrink(0).OnChange(func() { s.owner.setLanguage(s.language); s.changed() })
				case "proxy":
					d1Choice(c, p, "代理模式", &s.proxyMode, []d1Option{{value: "environment", label: "环境代理"}, {value: "system", label: "系统代理"}, {value: "direct", label: "不使用代理"}, {value: "manual", label: "手动代理"}}).
						Width(settingsControlWidth).Shrink(0).Tooltip(tr("代理设置用于后续网络请求，已传输的响应继续使用原连接")).OnChange(s.proxyEdited)
				case "clipboard":
					settingsSwitch(c, p, "剪贴板监听", &s.owner.clipOn).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "unverified-resume":
					settingsSwitch(c, p, "允许未校验续传", &s.owner.preferences.AllowUnverifiedResume).OnChange(func() {
						s.owner.mgr.SetAllowUnverifiedResume(s.owner.preferences.AllowUnverifiedResume)
						s.owner.saveAppPreferences()
						s.changed()
					})
				case "notify-system":
					settingsSwitch(c, p, "系统通知", &s.owner.preferences.NotifyComplete).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "notify-app":
					settingsSwitch(c, p, "应用通知", &s.owner.preferences.NotifyApp).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "autostart":
					settingsSwitch(c, p, "开机自动启动", &s.autoStart).OnChange(func() {
						s.autostartError = ""
						if err := s.owner.setAutoStart(s.autoStart); err != nil {
							s.autoStart = !s.autoStart
							s.autostartError = tr("无法设置开机自动启动：") + err.Error()
						}
						s.changed()
					})
				case "min-tray-start":
					settingsSwitch(c, p, "启动时最小化到托盘", &s.owner.preferences.MinTrayStart).OnChange(func() {
						s.trayStartError = ""
						if err := s.owner.applyTrayPrefs(); err != nil {
							s.trayStartError = tr("无法设置托盘：") + err.Error()
						}
						s.changed()
					})
				case "min-tray-close":
					settingsSwitch(c, p, "关闭时最小化到托盘", &s.owner.preferences.MinTrayClose).OnChange(func() {
						s.trayCloseError = ""
						if err := s.owner.applyTrayPrefs(); err != nil {
							s.trayCloseError = tr("无法设置托盘：") + err.Error()
						}
						s.changed()
					})
				case "finish-action":
					d1Choice(c, p, "全部完成后", &s.owner.preferences.FinishAction, []d1Option{{value: "", label: "无"}, {value: "shutdown", label: "关闭计算机"}, {value: "sleep", label: "睡眠"}, {value: "run", label: "运行程序"}}).
						Width(settingsControlWidth).Shrink(0).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "drag-file-action":
					d1Choice(c, p, "拖出文件时", &s.owner.preferences.DragFileAction, []d1Option{{value: "", label: "移动"}, {value: "copy", label: "复制"}}).
						Width(settingsControlWidth).Shrink(0).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "file-exists":
					d1Choice(c, p, "文件已存在时", &s.owner.preferences.FileExistsAction, []d1Option{{value: "", label: "覆盖原文件"}, {value: "copy", label: "创建副本"}}).
						Width(settingsControlWidth).Shrink(0).OnChange(func() {
							s.owner.mgr.SetOverwriteExisting(s.owner.preferences.FileExistsAction != "copy")
							s.owner.saveAppPreferences()
							s.changed()
						})
				case "connections":
					d1Choice(c, p, "默认连接数", &s.connections, []d1Option{{value: "-1", label: "自动"}, {value: "1", label: "1"}, {value: "4", label: "4"}, {value: "8", label: "8"}, {value: "16", label: "16"}, {value: "32", label: "32"}}).Width(settingsControlWidth).Shrink(0).
						OnChange(func() {
							value, _ := strconv.Atoi(s.connections)
							s.owner.setDefaultConnections(value)
							s.changed()
						})
				case "max-connections":
					d1Choice(c, p, "最大连接数", &s.maxconn, []d1Option{{value: "4", label: "4"}, {value: "8", label: "8"}, {value: "16", label: "16"}, {value: "32", label: "32"}, {value: "64", label: "64"}, {value: "128", label: "128"}, {value: "256", label: "256"}, {value: "0", label: "无限制"}}).Width(settingsControlWidth).Shrink(0).
						OnChange(func() {
							value, _ := strconv.Atoi(s.maxconn)
							s.owner.setMaxConnections(value)
							s.changed()
						})
				case "night-enabled":
					settingsSwitch(c, p, "启用夜间队列", &s.owner.nightOn).OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
				case "detail":
					d1Choice(c, p, "详情面板位置", &s.detail, []d1Option{{value: "bottom", label: "底部显示"}, {value: "right", label: "右侧显示"}}).Width(settingsControlWidth).Shrink(0).
						OnChange(func() { s.owner.setDetailPlacement(s.detail); s.changed() })
				case "reset-layout":
					d1Button(c, p, "恢复默认", "").OnClick(func() { s.owner.resetPanelLayout(); s.changed() })
				}
			})
		}
		switch entry.id {
		case "shortcuts":
			s.shortcutFields(c, p)
		case "appearance":
			if hideTitle {
				s.themeChoices(c, p)
			}
		case "accent":
			if hideTitle {
				s.accentChoices(c, p)
			}
		case "updates":
			s.updateFields(c, p)
		case "proxy":
			s.proxyFields(c, p)
		case "site-logins":
			s.siteLoginsFields(c, p)
		case "windows-auth":
			s.windowsAuthFields(c, p)
		case "media-mux":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "FFmpeg 程序路径", "settings-media-mux", &s.mediaMuxDraft, "留空自动查找").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(s.applyMediaMux)
			})
			settingsError(c, p, s.mediaMuxError)
		case "site-rules":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "规则源地址", "settings-site-rules", &s.siteRulesDraft, "https://example.com/site-rules.json").Grow(1).Shrink(1).Basis(0).MinWidth(0).OnChange(s.applySiteRules)
			})
			settingsError(c, p, s.siteRulesError)
		case "site-conns":
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				if len(s.siteConnDraft) > 0 {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, tr("站点")).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
						ui.Text(c, tr("连接数")).Width(110).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
						ui.Box(c).Width(style.SizeIconButton).Shrink(0)
					})
				}
				for i := range s.siteConnDraft {
					index := i
					c.Key(s.siteConnDraft[index].key)
					ui.Column(c).FillWidth().Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							settingsInput(c, p, "站点", "settings-site-conn-host", &s.siteConnDraft[index].host, "example.com").Grow(1).Shrink(1).Basis(0).MinWidth(0).
								OnChange(s.applySiteConnections)
							settingsInput(c, p, "连接数", "settings-site-conn-limit", &s.siteConnDraft[index].limit, "1–64").Width(110).Shrink(0).
								OnChange(s.applySiteConnections)
							d1SelectionAction(c, p, "删除", "trash", p.bad, true).OnClick(func() {
								s.siteConnDraft = append(s.siteConnDraft[:index], s.siteConnDraft[index+1:]...)
								s.applySiteConnections()
							})
						})
						settingsError(c, p, s.siteConnDraft[index].err)
					})
				}
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					d1Button(c, p, "添加", "plus").OnClick(func() {
						if len(s.siteConnDraft) >= 32 {
							s.siteConnError = tr("站点连接配置最多 32 条")
							s.changed()
							return
						}
						s.siteConnDraft = append(s.siteConnDraft, siteConnRow{key: s.newDraftKey()})
						s.siteConnDirty = true
						s.changed()
					})

				})
				settingsError(c, p, s.siteConnError)
			})
		case "autostart":
			settingsError(c, p, s.autostartError)
		case "min-tray-start":
			settingsError(c, p, s.trayStartError)
		case "min-tray-close":
			settingsError(c, p, s.trayCloseError)
		case "directory":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "默认保存位置", "settings-directory", &s.directory, "").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(s.applyDirectory)
				d1Button(c, p, "浏览", "").OnClick(s.browseDirectory)
			})
			settingsError(c, p, s.directoryError)
		case "category-dirs":
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				for i, id := range categoryDirIDs {
					index := i
					ui.Column(c).FillWidth().Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, tr(categoryTitle(id))).Width(104).Shrink(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine().Tooltip(tr(categoryTitle(id)))
							settingsInput(c, p, categoryTitle(id)+tr("子目录"), "settings-category-dir-"+id, &s.categoryDirDraft[index], "留空不归档").Grow(1).Shrink(1).Basis(0).MinWidth(0).
								OnChange(func() { s.applyCategoryDir(index) })
						})
						settingsError(c, p, s.categoryDirErrors[id])
					})
				}
				settingsError(c, p, s.categoryDirError)
			})
		case "category-exts":
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				for i, id := range categoryExtIDs {
					index := i
					ui.Column(c).FillWidth().Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, tr(categoryTitle(id))).Width(104).Shrink(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine().Tooltip(tr(categoryTitle(id)))
							settingsInput(c, p, categoryTitle(id)+tr("扩展名"), "settings-category-exts-"+id, &s.categoryExtDraft[index], "空格或逗号分隔，例如 mp4 mkv").Grow(1).Shrink(1).Basis(0).MinWidth(0).
								OnChange(func() { s.applyCategoryExt(index) })
						})
						settingsError(c, p, s.categoryExtErrors[id])
					})
				}
				settingsError(c, p, s.categoryExtError)
			})
		case "finish-action":
			if s.owner.preferences.FinishAction == "" {
				break
			}
			ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr("重复日期")).FontSize(style.TextSM).TextColor(p.mu).Shrink(0)
				labels := []string{"日", "一", "二", "三", "四", "五", "六"}
				for i := range s.finishDays {
					index := i
					c.Key("settings-finish-day-" + strconv.Itoa(index))
					ui.CheckboxBase(c, &s.finishDays[index]).Gap(6).Justify(ui.Start).AlignItems(ui.Center).
						Label(tr("重复日期 ") + tr(labels[index])).Children(func() {
						d1CheckMark(c, p, s.finishDays[index])
						ui.Text(c, tr(labels[index])).FontSize(style.TextSM).TextColor(p.fg)
					}).OnChange(func() {
						anySelected := false
						for _, selected := range s.finishDays {
							anySelected = anySelected || selected
						}
						if !anySelected {
							s.finishDays[index] = true
							s.finishDaysError = tr("请至少选择一个重复日期")
							s.changed()
							return
						}
						s.finishDaysError = ""
						s.owner.preferences.FinishDays = s.selectedFinishDays()
						s.owner.saveAppPreferences()
					})
				}
			})
			settingsError(c, p, s.finishDaysError)
		case "custom-queues":
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("队列")).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
					ui.Text(c, tr("并发上限")).Width(86).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine().Tooltip(tr("并发上限"))
					ui.Text(c, tr("限速 KiB/s")).Width(96).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine().Tooltip(tr("限速 KiB/s"))
					if len(s.queueDraft) > 0 {
						ui.Box(c).Width(100).Shrink(0)
					}
				})
				ui.Column(c).FillWidth().Gap(4).Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, tr("主队列")).FontSize(style.TextSM).TextColor(p.fg).Grow(1).Shrink(1).Basis(0).MinWidth(0).SingleLine()
						settingsInput(c, p, "主队列并发上限", "settings-queue-main-limit", &s.queueMainLimit, "留空不限").Width(86).Shrink(0).
							OnChange(func() { s.applyBaseQueueSetting(s.queueMainLimit, "main", false) })
						settingsInput(c, p, "主队列限速 KiB/s", "settings-queue-main-rate", &s.queueMainRate, "留空不限").Width(96).Shrink(0).
							OnChange(func() { s.applyBaseQueueSetting(s.queueMainRate, "main", true) })
						if len(s.queueDraft) > 0 {
							ui.Box(c).Width(100).Shrink(0)
						}
					})
					settingsError(c, p, s.queueBaseErrors["main-limit"])
					settingsError(c, p, s.queueBaseErrors["main-rate"])
				})
				ui.Column(c).FillWidth().Gap(4).Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, tr("夜间队列")).FontSize(style.TextSM).TextColor(p.fg).Grow(1).Shrink(1).Basis(0).MinWidth(0).SingleLine()
						settingsInput(c, p, "夜间队列并发上限", "settings-queue-night-limit", &s.queueNightLimit, "留空不限").Width(86).Shrink(0).
							OnChange(func() { s.applyBaseQueueSetting(s.queueNightLimit, "night", false) })
						settingsInput(c, p, "夜间队列限速 KiB/s", "settings-queue-night-rate", &s.queueNightRate, "留空不限").Width(96).Shrink(0).
							OnChange(func() { s.applyBaseQueueSetting(s.queueNightRate, "night", true) })
						if len(s.queueDraft) > 0 {
							ui.Box(c).Width(100).Shrink(0)
						}
					})
					settingsError(c, p, s.queueBaseErrors["night-limit"])
					settingsError(c, p, s.queueBaseErrors["night-rate"])
				})
				for i := range s.queueDraft {
					index := i
					c.Key(s.queueDraft[index].key)
					ui.Column(c).FillWidth().Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							settingsInput(c, p, "队列名称", "settings-queue-name", &s.queueDraft[index].name, "名称").Grow(1).Shrink(1).Basis(0).MinWidth(0).
								OnChange(s.applyQueuesDraft)
							settingsInput(c, p, "并发上限", "settings-queue-limit", &s.queueDraft[index].limit, "留空不限").Width(86).Shrink(0).
								OnChange(s.applyQueuesDraft)
							settingsInput(c, p, "限速 KiB/s", "settings-queue-rate", &s.queueDraft[index].rate, "留空不限").Width(96).Shrink(0).
								OnChange(s.applyQueuesDraft)
							d1SelectionAction(c, p, "上移", "arrow-up", p.mu, index > 0).OnClick(func() {
								if index > 0 {
									s.queueDraft[index], s.queueDraft[index-1] = s.queueDraft[index-1], s.queueDraft[index]
									s.queueDirty = true
									s.applyQueuesDraft()
								}
							})
							d1SelectionAction(c, p, "下移", "arrow-down", p.mu, index < len(s.queueDraft)-1).OnClick(func() {
								if index < len(s.queueDraft)-1 {
									s.queueDraft[index], s.queueDraft[index+1] = s.queueDraft[index+1], s.queueDraft[index]
									s.queueDirty = true
									s.applyQueuesDraft()
								}
							})
							d1SelectionAction(c, p, "删除", "trash", p.bad, true).OnClick(func() {
								s.queueDraft = append(s.queueDraft[:index], s.queueDraft[index+1:]...)
								s.queueDirty = true
								s.applyQueuesDraft()
							})
						})
						settingsError(c, p, s.queueDraft[index].err)
					})
				}
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					d1Button(c, p, "添加队列", "plus").OnClick(func() {
						if len(s.queueDraft) >= 16 {
							s.queueError = tr("自定义队列最多 16 个")
							s.changed()
							return
						}
						s.queueDraft = append(s.queueDraft, queueRow{key: s.newDraftKey()})
						s.queueDirty = true
						s.changed()
					})

				})
				settingsError(c, p, s.queueError)
			})
		case "custom-categories":
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				if len(s.customCatDraft) > 0 {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, tr("名称")).Width(90).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
						ui.Text(c, tr("扩展名")).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
						ui.Text(c, tr("子目录")).Width(100).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
						ui.Box(c).Width(style.SizeIconButton).Shrink(0)
					})
				}
				for i := range s.customCatDraft {
					index := i
					c.Key(s.customCatDraft[index].key)
					ui.Column(c).FillWidth().Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							settingsInput(c, p, "分类名称", "settings-custom-cat-label", &s.customCatDraft[index].label, "名称").Width(90).Shrink(0).
								OnChange(s.applyCustomCategories)
							settingsInput(c, p, "扩展名", "settings-custom-cat-exts", &s.customCatDraft[index].exts, "例如 zst tar").Grow(1).Shrink(1).Basis(0).MinWidth(0).
								OnChange(s.applyCustomCategories)
							settingsInput(c, p, "子目录", "settings-custom-cat-dir", &s.customCatDraft[index].dir, "留空不归档").Width(100).Shrink(0).
								OnChange(s.applyCustomCategories)
							d1SelectionAction(c, p, "删除", "trash", p.bad, true).OnClick(func() {
								s.customCatDraft = append(s.customCatDraft[:index], s.customCatDraft[index+1:]...)
								s.applyCustomCategories()
							})
						})
						settingsError(c, p, s.customCatDraft[index].err)
					})
				}
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					d1Button(c, p, "添加分类", "plus").OnClick(func() {
						if len(s.customCatDraft) >= 16 {
							s.customCatError = tr("自定义分类最多 16 个")
							s.changed()
							return
						}
						s.customCatDraft = append(s.customCatDraft, customCatRow{key: s.newDraftKey()})
						s.applyCustomCategories()
					})
				})
				settingsError(c, p, s.customCatError)
			})
		case "post-scan":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "扫描程序", "settings-scan-program", &s.owner.preferences.ScanProgram, "留空不启用").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
			})
		case "scan-args":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "扫描参数", "settings-scan-args", &s.owner.preferences.ScanArgs, "空格分隔，{file} 表示文件路径").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
			})
		case "finish-program":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "完成后运行程序", "settings-finish-program", &s.owner.preferences.FinishProgram, "留空不执行").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
			})
		case "finish-args":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				settingsInput(c, p, "运行参数", "settings-finish-args", &s.owner.preferences.FinishArgs, "空格分隔").Grow(1).Shrink(1).Basis(0).MinWidth(0).
					OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
			})
		case "traffic-quota":
			if s.quotaPeriod != "" {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("额度（MB）")).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
					settingsInput(c, p, "额度（MB）", "settings-quota-mb", &s.quotaMB, "1–4194304").Width(settingsControlWidth).Shrink(0).OnChange(s.applyQuota)
				})
			}
			settingsError(c, p, s.quotaError)
		case "speed":
			settingsError(c, p, s.rateError)
		case "night-time":
			settingsError(c, p, s.scheduleError)
		}
	})
}

// schedulePicker keeps both time segments in one popup and applies each selection.
func (s *settingsPage) schedulePicker(c *ui.Context, p *d1Palette) {
	c.Key("schedule-picker")
	trigger := ui.ButtonBase(c).Width(settingsControlWidth).Height(style.HeightButton).Shrink(0).Padding(0, 10).Gap(6).
		Radius(style.RadiusSM).Border(1, p.bd).Background(p.bg).AlignItems(ui.Center).
		Role(ui.RoleMenuButton).Label(tr("开始时间")).Expanded(s.schedulePickerOpen)
	hoverMotion(trigger, p.bg, p.hover)
	trigger.Children(func() {
		ui.Text(c, s.scheduleHour+":"+s.scheduleMinute).Grow(1).Shrink(1).Basis(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
		d1Icon(c, "chev", 16, p.mu).Shrink(0)
	})
	trigger.OnClick(func() { s.schedulePickerOpen = !s.schedulePickerOpen }).OnShortcut(0, ui.KeyDown, func() { s.schedulePickerOpen = true })
	ui.PopoverBase(c, trigger, &s.schedulePickerOpen, func(panel ui.Element) {
		d1DropdownPanel(panel, p).Width(settingsControlWidth).Padding(8).AttachTo(trigger, ui.AnchorBottomStart, ui.AnchorTopStart).Children(func() {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				for column := 0; column < 2; column++ {
					index := column
					c.Key(index)
					ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
						label, count := "小时", 24
						if index == 1 {
							label, count = "分钟", 60
						}
						ui.Row(c).FillWidth().Height(26).Center().Children(func() {
							ui.Text(c, tr(label)).FontSize(style.TextSM).FontWeight(style.WeightMedium).TextColor(p.mu).SingleLine()
						})
						ui.Scroll(c).Height(180).FillWidth().Children(func() {
							ui.Column(c).FillWidth().Gap(2).FocusGroup(ui.Vertical).Children(func() {
								for i := 0; i < count; i++ {
									value := strconv.Itoa(i/10) + strconv.Itoa(i%10)
									selected := (index == 0 && s.scheduleHour == value) || (index == 1 && s.scheduleMinute == value)
									c.Key(value)
									button := ui.ButtonBase(c).FillWidth().Height(28).Center().Radius(style.RadiusSM).Label(value).Selected(selected).Cursor(ui.CursorPointer)
									color := p.fg
									if selected {
										button.Background(p.pri)
										color = p.onpri
									} else if button.Hovered() || button.FocusVisible() {
										button.Background(p.hover)
									}
									button.Children(func() { ui.Text(c, value).FontSize(style.TextSM).TextColor(color).SingleLine() })
									button.OnClick(func() {
										if index == 0 {
											s.scheduleHour = value
										} else {
											s.scheduleMinute = value
										}
										s.schedule = s.scheduleHour + ":" + s.scheduleMinute
										s.applySchedule()
									})
								}
							})
						})
					})
				}
			})
		})
	})
}

// accentChoices 使用带颜色标识的单选菜单，和其他设置选择器保持一致。
func (s *settingsPage) accentChoices(c *ui.Context, p *d1Palette) {
	type colorOption struct{ id, label string }
	options := []colorOption{{"gray", "灰色"}}
	for _, scale := range style.AccentScales {
		options = append(options, colorOption{scale.ID, scale.Label})
	}
	accent := s.owner.preferences.AccentColor
	if accent == "" {
		accent = "blue"
	}
	label := "灰色"
	if scale, ok := style.Accent(accent); ok {
		label = scale.Label
	}
	c.Key("accent-picker")
	choice := ui.SelectBase(c, &s.owner.preferences.AccentColor)
	trigger := choice.Trigger.Width(settingsControlWidth).Height(style.HeightButton).Shrink(0).
		Padding(0, 10).Gap(8).AlignItems(ui.Center).Radius(style.RadiusSM).Border(1, p.bd).
		Background(p.bg).Label(tr("主题色"))
	hoverMotion(trigger, p.bg, p.hover)
	trigger.Children(func() {
		ui.Box(c).Size(16, 16).Radius(style.RadiusSM).Background(p.pri).Border(1, p.bd)
		ui.Text(c, tr(label)).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
		d1Icon(c, "chev", 16, p.mu).Shrink(0)
	})
	trigger.OnChange(func() { s.owner.saveAppPreferences(); s.changed() })
	choice.Popup(func(panel ui.Element) {
		d1DropdownPanel(panel, p).Width(settingsControlWidth).MaxHeight(260).AttachTo(trigger, ui.AnchorBottomEnd, ui.AnchorTopEnd).Children(func() {
			ui.Scroll(c).MaxHeight(248).FillWidth().Children(func() {
				for _, option := range options {
					c.Key(option.id)
					colors := style.NewAccentRules(p.dark, option.id).Colors
					item := choice.Item(option.id).FillWidth().Padding(6, 8).Gap(8).AlignItems(ui.Center).Radius(style.RadiusSM).Label(tr(option.label)).Cursor(ui.CursorPointer)
					if item.Highlighted() {
						item.Background(p.hover)
					}
					item.Children(func() {
						ui.Box(c).Size(16, 16).Radius(style.RadiusSM).Background(colors.Primary).Border(1, p.bd).Shrink(0)
						ui.Text(c, tr(option.label)).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
						if accent == option.id {
							style.Icon(c, style.IconCheck, 16, p.fg)
						}
					})
				}
			})
		})
	})
}

// Theme previews use the same palettes as the real windows, so choices
// remain recognisable regardless of the currently active theme.
func (s *settingsPage) themeChoices(c *ui.Context, p *d1Palette) {
	ui.RadioGroup(c, func() {
		for _, option := range []struct {
			value, label string
			palette      *d1Palette
		}{{"system", "跟随系统", nil}, {"light", "浅色", d1AccentPalette(false, s.owner.preferences.AccentColor)}, {"dark", "深色", d1AccentPalette(true, s.owner.preferences.AccentColor)}} {
			c.Key("theme-" + option.value)
			selected := s.theme == option.value
			b := ui.RadioBase(c, &s.theme, option.value).Column().Size(88, 57).Padding(4).
				AlignItems(ui.Center).Radius(style.RadiusMD).Border(2, p.bd).Label(tr(option.label)).Cursor(ui.CursorPointer)
			b.Tooltip(tr(option.label))
			selectedAmount := b.Animate("theme-selection", motionTarget(selected || b.FocusVisible()), selectMotion)
			b.Border(2, p.bd.Mix(p.pri, selectedAmount))
			hoverMotion(b, ui.Transparent, p.hover)
			b.Children(func() {
				if option.value == "system" {
					systemThemePreview(c, s.owner.preferences.AccentColor)
				} else {
					themePreview(c, option.palette)
				}
			})
			b.OnChange(func() { s.owner.setThemeMode(s.theme); s.changed() })
		}
	}).Row().Gap(8).Shrink(0).Label(tr("主题"))
}

func systemThemePreview(c *ui.Context, accent string) {
	ui.Row(c).Size(74, 45).Radius(5).Border(1, d1Light().bd).Children(func() {
		for _, p := range []*d1Palette{d1AccentPalette(false, accent), d1AccentPalette(true, accent)} {
			ui.Row(c).Width(36).FillHeight().Background(p.bg).Children(func() {
				ui.Column(c).Width(9).FillHeight().Padding(4, 2).Background(p.card).Children(func() {
					ui.Box(c).Size(4, 3).Radius(1).Background(p.mu2)
				})
				ui.Column(c).Grow(1).Shrink(1).Basis(0).Padding(5, 3).Gap(4).Children(func() {
					ui.Box(c).Size(16, 3).Radius(1.5).Background(p.pri)
					ui.Box(c).Size(20, 2).Radius(1).Background(p.bd2)
					ui.Box(c).Size(15, 2).Radius(1).Background(p.bd2)
					ui.Box(c).Size(18, 3).Radius(1.5).Background(p.pri)
				})
			})
		}
	})
}

func themePreview(c *ui.Context, p *d1Palette) {
	ui.Row(c).Size(74, 45).Radius(5).Border(1, p.bd).Background(p.bg).Children(func() {
		ui.Column(c).Width(17).FillHeight().Padding(4, 3).Gap(3).Background(p.card).
			BorderWidth(0, 1, 0, 0).BorderColor(p.bd).Children(func() {
			ui.Box(c).Size(8, 3).Radius(1.5).Background(p.mu2)
			ui.Box(c).Size(9, 2).Radius(1).Background(p.bd2)
			ui.Box(c).Size(7, 2).Radius(1).Background(p.bd2)
		})
		ui.Column(c).Grow(1).Shrink(1).Basis(0).Padding(5, 5).Gap(4).Children(func() {
			ui.Box(c).Size(24, 3).Radius(1.5).Background(p.pri)
			ui.Box(c).Size(35, 2).Radius(1).Background(p.bd2)
			ui.Box(c).Size(28, 2).Radius(1).Background(p.bd2)
			ui.Box(c).Size(32, 3).Radius(1.5).Background(p.pri)
		})
	})
}

// shortcutDefinitions 是全局快捷键的稳定身份、默认键位与展示顺序。
var shortcutDefinitions = []struct{ id, title, keys string }{
	{"search", "搜索任务", "Ctrl F"},
	{"add", "添加下载", "Ctrl N"},
	{"open-folder", "打开所在文件夹", "Ctrl O"},
	{"copy-url", "复制下载链接", "Ctrl C"},
	{"select-all", "全选可见任务", "Ctrl A"},
	{"delete", "删除所选任务", "Delete"},
	{"toggle", "打开文件或继续/暂停", "Enter"},
}

func shortcutLabel(id string) string {
	for _, definition := range shortcutDefinitions {
		if definition.id == id {
			return tr(definition.title)
		}
	}
	return tr(id)
}

// shortcutText 返回持久化的键位，缺失或空白时回退默认值。
func shortcutText(prefs appPreferences, id string) string {
	for _, definition := range shortcutDefinitions {
		if definition.id != id {
			continue
		}
		if value := strings.TrimSpace(prefs.Shortcuts[id]); value != "" {
			return value
		}
		return definition.keys
	}
	return ""
}

// shortcutKeyLabel 是英文界面中的快捷键名称；源码键位仍是标准组合。
func shortcutKeyLabel(keys string) string {
	if i18n.Locale() != i18n.English {
		return strings.ReplaceAll(keys, " ", " ")
	}
	switch keys {
	case "Ctrl K", "Ctrl N", "Ctrl O", "Ctrl C", "Ctrl A":
		return strings.ReplaceAll(keys, "Ctrl", "Ctrl")
	}
	return keys
}

// shortcutFields 渲染快捷键设置卡片的完整行列表，保持任务表快捷键语义不变。
func (s *settingsPage) shortcutFields(c *ui.Context, p *d1Palette) {
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		ui.Text(c, tr("点击快捷键后按下新的组合；Esc 取消。系统保留组合可能无法被应用接收。")).FontSize(style.TextSM).TextColor(p.mu)
		settingsError(c, p, s.shortcutError)
		for i, definition := range shortcutDefinitions {
			id, title := definition.id, definition.title
			c.Key(id)
			if i > 0 {
				ui.Box(c).FillWidth().Height(1).Background(p.bd2)
			}
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr(title)).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
				label := shortcutKeyLabel(shortcutText(s.owner.preferences, id))
				if s.shortcutEditing == id {
					label = tr("请按快捷键…")
				}
				handle := &s.shortcutHandles[i]
				button := ui.ButtonBase(c).Bind(handle).Height(28).MinWidth(112).Padding(0, 8).Radius(style.RadiusSM).Border(1, p.bd2).Center().Label(tr(title) + " " + label)
				button.Children(func() { ui.Text(c, label).FontSize(style.TextXS).TextColor(p.fg).SingleLine() })
				button.OnClick(func() { s.shortcutEditing = id; s.shortcutError = ""; handle.Focus(c) })
				button.HandleInput(func(ev ui.InputEvent) bool {
					if s.shortcutEditing != id || ev.Kind != ui.InputKeyDown {
						return false
					}
					if ev.Key == ui.KeyEscape && ev.Mods == 0 {
						s.shortcutEditing = ""
						return true
					}
					if ev.Repeat {
						return true
					}
					value, err := shortcutFromKey(ev.Mods, ev.Key)
					if err == "" {
						err = shortcutConflict(s.owner.preferences, id, value)
					}
					if err != "" {
						s.shortcutError = err
						return true
					}
					if s.owner.preferences.Shortcuts == nil {
						s.owner.preferences.Shortcuts = map[string]string{}
					}
					s.owner.preferences.Shortcuts[id] = value
					s.shortcutEditing, s.shortcutError = "", ""
					s.owner.saveAppPreferences()
					s.changed()
					return true
				})
				d1Button(c, p, "恢复默认", "").OnClick(func() {
					value := ""
					for _, item := range shortcutDefinitions {
						if item.id == id {
							value = item.keys
							break
						}
					}
					if err := shortcutConflict(s.owner.preferences, id, value); err != "" {
						s.shortcutError = err
						return
					}
					delete(s.owner.preferences.Shortcuts, id)
					s.shortcutEditing, s.shortcutError = "", ""
					s.owner.saveAppPreferences()
					s.changed()
				})
			})
		}
	})
}

func settingsInput(c *ui.Context, p *d1Palette, label, key string, value *string, hint string) ui.Element {
	c.Key(key)
	input := ui.TextInputBase(c, value).Height(32).Padding(0, 10).Radius(style.RadiusSM).Border(1, p.bd2).
		Background(p.bg).TextColor(p.fg).FontSize(style.TextSM).Label(tr(label)).Tooltip(*value)
	if hint != "" {
		input.Placeholder(tr(hint))
	}
	if input.Focused() {
		input.BorderColor(p.pri)
	}
	return input
}

func settingsError(c *ui.Context, p *d1Palette, message string) {
	if message != "" {
		ui.Text(c, tr(message)).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
	}
}

func settingsSwitch(c *ui.Context, p *d1Palette, label string, value *bool) ui.Element {
	c.Key(label)
	toggle := ui.SwitchBase(c, value).Size(style.SizeSwitchW, style.SizeSwitchH).Shrink(0).Radius(8).Label(tr(label))
	left, track, thumb := switchMotion(toggle, p, *value)
	toggle.Children(func() {
		ui.Box(c).Size(style.SizeSwitchW, style.SizeSwitchH).Radius(8).Background(track).Children(func() {
			ui.Box(c).Absolute().Left(left).Top(2).Size(12, 12).Radius(6).Background(thumb)
		})
	})
	return toggle
}

func (s *settingsPage) browseDirectory() {
	path, cancel := chooseFolder(s.win, tr("选择默认保存文件夹"), s.directory)
	if cancel {
		return
	}
	s.directory = path
	s.applyDirectory()
}

// chooseFolder 弹出系统文件夹选择器；parent 可为 nil，cancel 为真表示用户
// 取消或对话框失败，此时 path 为空。
func chooseFolder(parent *mygo.Window, title, defaultPath string) (path string, cancel bool) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: title, DefaultPath: defaultPath, Directory: true, CreateDirectories: true})
	if err != nil || len(paths) == 0 {
		return "", true
	}
	return paths[0], false
}
