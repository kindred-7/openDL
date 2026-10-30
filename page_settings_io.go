package main

// 设置导出与导入：JSON 文件不包含加密凭据，导入保留本机已保存的密文。

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
)

// settingsExport 返回设置的可移植副本；密文凭据不随文件携带。
func settingsExport(prefs appPreferences) appPreferences {
	prefs.ProxySecret = ""
	prefs.SiteLoginsSecret = ""
	return prefs
}

// exportSettings 把当前设置导出为 JSON 文件。
func (pg *mainPage) exportSettings() {
	if !mygo.App.IsReady() {
		return
	}
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            pg.win,
		Title:             tr("导出设置"),
		DefaultPath:       "opendl-settings.json",
		Filters:           []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}},
		CreateDirectories: true,
	})
	if err != nil || path == "" {
		return
	}
	data, err := json.MarshalIndent(settingsExport(pg.preferences), "", "  ")
	if err != nil {
		pg.flash(fmt.Sprintf(tr("导出设置失败：%s"), err.Error()))
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		pg.flash(fmt.Sprintf(tr("导出设置失败：%s"), err.Error()))
		return
	}
	pg.flash(fmt.Sprintf(tr("已导出设置：%s"), path))
}

// importSettings 从 JSON 文件导入设置并立即应用；密文凭据保持本机现状。
func (pg *mainPage) importSettings() {
	if !mygo.App.IsReady() || pg.mgr == nil {
		return
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  pg.win,
		Title:   tr("导入设置"),
		Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}},
	})
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		pg.flash(fmt.Sprintf(tr("导入设置失败：%s"), err.Error()))
		return
	}
	var prefs appPreferences
	if err := json.Unmarshal(data, &prefs); err != nil {
		pg.flash(tr("设置文件格式无效"))
		return
	}
	prefs.validate()
	prefs.ProxySecret = pg.preferences.ProxySecret
	prefs.SiteLoginsSecret = pg.preferences.SiteLoginsSecret
	// A pending password-encryption job must not restore the old proxy after import.
	pg.proxyJob++
	pg.preferences = prefs
	pg.saveDir = prefs.Directory
	pg.clipOn = prefs.Clipboard
	pg.sortDesc = prefs.SortDesc
	pg.nightOn, pg.nightAt, pg.nightStop = prefs.NightOn, prefs.NightAt, prefs.NightStop
	pg.nightDays = append([]int(nil), prefs.NightDays...)
	pg.limitOn = prefs.RateMBps > 0
	if pg.limitOn {
		pg.limitValue = strconv.FormatFloat(prefs.RateMBps, 'f', -1, 64)
	}
	pg.mgr.SetDownloadDefaults(pg.saveDir, prefs.Connections)
	pg.mgr.SetMaxConnections(prefs.MaxConnections)
	pg.mgr.SetRateLimit(int64(prefs.RateMBps * float64(1<<20)))
	if pg.showAdd {
		pg.defaultDirPending, pg.defaultConnsPending = true, true
	} else {
		pg.dlgDir = pg.saveDir
		pg.dlgSeg = connectionIndex(prefs.Connections)
	}
	if s := pg.settingsWindow; s != nil {
		// 导入整体替换偏好，直接生效字段的草稿复位后跟随新值。
		s.directoryDirty = false
		s.directoryError = ""
		s.categoryDirDirty, s.categoryExtDirty, s.customCatDirty = false, false, false
		s.categoryDirErrors, s.categoryExtErrors = nil, nil
		s.rateDirty, s.quotaDirty, s.scheduleDirty = false, false, false
		s.rateError, s.quotaError, s.scheduleError = "", "", ""
		s.queueDirty, s.siteConnDirty = false, false
		s.queueBaseErrors = nil
		s.proxyRevision++
		s.mediaMuxDraft, s.mediaMuxError = prefs.MediaMuxProgram, ""
		s.siteRulesDraft, s.siteRulesError = prefs.SiteRulesURL, ""
		s.siteRulesRevision++
	}
	pg.saveAppPreferences()
	pg.setLanguage(prefs.Language)
	applyQueues(prefs)
	applyCategories(prefs)
	byExt, fallback := categoryDirTable(prefs.CategoryDirs)
	pg.mgr.SetCategoryDirs(byExt, fallback)
	pg.mgr.SetQueueLimits(queueLimitTable(prefs, prefs.CustomQueues))
	pg.mgr.SetQueueRates(queueRateTable(prefs, prefs.CustomQueues))
	pg.mgr.SetSiteConnectionLimits(siteConnectionTable(prefs.SiteConnections))
	pg.mgr.SetMediaMuxProgram(prefs.MediaMuxProgram)
	var problems []string
	if err := pg.mgr.SetWindowsAuthSites(prefs.WindowsAuth); err != nil {
		// 与启动路径一致：损坏的集成认证配置按禁用处理，不拖累其余导入项。
		pg.mgr.BlockWindowsAuth(err)
		problems = append(problems, err.Error())
	}
	if err := pg.applyTrafficQuota(prefs.QuotaPeriod, prefs.QuotaMB); err != nil {
		problems = append(problems, err.Error())
	}
	if err := pg.applyTrayPrefs(); err != nil {
		problems = append(problems, err.Error())
	}
	password, err := initializeDownloadProxy(pg.mgr, prefs)
	if err != nil {
		problems = append(problems, err.Error())
	}
	pg.proxyPassword, pg.proxyError = password, proxySettingsStatus(err)
	if s := pg.settingsWindow; s != nil {
		s.proxyDirty = false
		s.syncProxy()
	}
	setSiteRulesSource(prefs.SiteRulesURL)
	pg.refreshSettingsWindow()
	if len(problems) > 0 {
		pg.flash(tr("已导入设置，部分设置未生效：") + strings.Join(problems, "；"))
	} else {
		pg.flash(tr("已导入设置"))
	}
}
