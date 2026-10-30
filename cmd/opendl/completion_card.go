package main

import (
	"path/filepath"
	"time"

	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type completionCardItem struct {
	name string
	path string
}

// showCompletionCard is called on the main thread; queued completions are
// shown one at a time so simultaneous downloads remain individually actionable.
func (pg *mainPage) showCompletionCard(name, path string) {
	if path == "" || !mygo.App.IsReady() {
		return
	}
	pg.completionQueue = append(pg.completionQueue, completionCardItem{name: name, path: path})
	pg.showNextCompletionCard()
}

func (pg *mainPage) showNextCompletionCard() {
	if pg.completionWindow != nil || len(pg.completionQueue) == 0 {
		return
	}
	if pg.quitting {
		pg.completionQueue = nil
		return
	}
	select {
	case <-pg.stop:
		pg.completionQueue = nil
		return
	default:
	}
	item := pg.completionQueue[0]
	pg.completionQueue = pg.completionQueue[1:]
	if item.name == "" {
		item.name = filepath.Base(item.path)
	}
	const cardWidth, cardHeight, inset = 280, 128, 16
	area := mygo.Screen.PrimaryDisplay().WorkArea
	width, height := cardWidth, cardHeight
	if area.Width > 0 { width = min(width, max(1, area.Width-2*inset)) }
	if area.Height > 0 { height = min(height, max(1, area.Height-2*inset)) }
	x, y := area.X+area.Width-width-inset, area.Y+area.Height-height-inset
	var win *mygo.Window
	view := func(c *ui.Context) {
		p := d1AccentPalette(pg.dark, pg.preferences.AccentColor)
		ui.Column(c).Fill().Padding(16).Gap(12).Background(p.bg).Border(1, p.bd).Radius(style.RadiusLG).Font(style.FontFamily).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				style.Icon(c, style.IconCheckC, style.SizeIcon, p.pri)
				ui.Text(c, tr("下载完成")).Grow(1).Shrink(1).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
				d1IconButton(c, p, "x").Size(24, 24).Label(tr("关闭")).OnClick(func() { win.Close() })
			})
			ui.Text(c, item.name).FillWidth().Shrink(1).MinWidth(0).FontSize(style.TextBase).TextColor(p.mu).SingleLine().Tooltip(item.name)
			ui.Box(c).FillWidth().Grow(1)
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				d1Button(c, p, "打开文件夹", "folder").Grow(1).Shrink(1).Basis(0).OnClick(func() { win.Close(); pg.openLocalPath(filepath.Dir(item.path)) })
				d1PrimaryButton(c, p, "打开文件", "file").Grow(1).Shrink(1).Basis(0).Center().OnClick(func() { win.Close(); pg.openLocalPath(item.path) })
			})
		})
	}
	win = mygo.NewWindow(mygo.WindowOptions{Title: tr("下载完成"), Width: width, Height: height, X: x, Y: y, Hidden: true, Frameless: true, DisableResize: true, SkipTaskbar: true, AlwaysOnTop: true, Content: ui.View(view)})
	pg.completionWindow = win
	win.OnClosed(func() {
		if pg.completionWindow == win {
			pg.completionWindow = nil
			pg.showNextCompletionCard()
		}
	})
	win.ShowInactive()
	time.AfterFunc(12*time.Second, func() {
		win.Update(func() {
			if pg.completionWindow == win { win.Close() }
		})
	})
}
