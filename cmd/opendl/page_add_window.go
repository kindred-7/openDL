package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"openDL/style"
)

func (pg *mainPage) addDownloadTitle() string {
	if pg.dlgExtract {
		return tr("从站点提取下载链接")
	}
	return tr("添加下载")
}

func (pg *mainPage) openAddWindow() {
	if pg.win == nil {
		return
	}
	if pg.addWindow != nil {
		pg.addWindow.SetTitle(pg.addDownloadTitle())
		pg.addWindow.Show()
		pg.addWindow.Focus()
		pg.addWindow.Invalidate()
		return
	}
	w := mygo.NewWindow(mygo.WindowOptions{
		Title: pg.addDownloadTitle(), Width: 580, Height: 560, MinWidth: 480, MinHeight: 400,
		TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: int(style.HeightTitleBar),
		Content: ui.View(func(c *ui.Context) {
			base := pg.baseTheme()
			c.SetTheme(base.theme)
			pg.addDialog(c, base.palette, true)
		}),
	})
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
