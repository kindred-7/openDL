package main

import (
	"runtime"

	"openDL/i18n"
)

// tr is used only at presentation boundaries; source labels remain stable keys.
func tr(source string) string { return i18n.T(source) }

func (pg *mainPage) setLanguage(locale string) {
	locale = i18n.Normalize(locale)
	pg.preferences.Language = locale
	i18n.Set(locale)
	pg.saveAppPreferences()
	// Native menus and titles are retained outside the immediate-mode UI.
	if pg.tray != nil && runtime.GOOS == "linux" {
		pg.tray.SetMenu(pg.trayMenu())
	}
	if s := pg.settingsWindow; s != nil && s.win != nil {
		s.win.SetTitle(tr("设置"))
		s.win.Invalidate()
	}
	if pg.win != nil {
		pg.win.Invalidate()
	}
}
