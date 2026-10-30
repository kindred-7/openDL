package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (pg *mainPage) openAddWindow() {
	if pg.win == nil {
		return
	}
	if pg.addWindow != nil {
		pg.addWindow.Show()
		pg.addWindow.Focus()
		pg.addWindow.Invalidate()
		return
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: tr("添加下载"), Width: 580, Height: 560, MinWidth: 480, MinHeight: 400, Content: ui.View(func(c *ui.Context) {
		p := d1AccentPalette(pg.dark, pg.preferences.AccentColor)
		c.SetTheme(p.theme())
		pg.addDialog(c, p)
	})})
	pg.addWindow = w
	_ = w.SetIcon(appIconPNG)
	w.OnClosed(func() {
		if pg.addWindow != w {
			return
		}
		pg.addWindow = nil
		pg.showAdd = false
		pg.addWasOpen = false
		pg.dlgPassword = ""
		pg.dlgName = ""
		pg.browserDraft = nil
		pg.extractJob++
		pg.extracting = false
		if pg.win != nil {
			pg.win.Invalidate()
		}
	})
}

func (pg *mainPage) closeAddWindow() {
	pg.showAdd = false
	if pg.addWindow != nil {
		pg.addWindow.Close()
	}
}
