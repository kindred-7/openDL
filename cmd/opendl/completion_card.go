package main

import (
	"path/filepath"
	"time"

	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 完成卡片的自动收起时长：6 秒足以读完文件名并把指针移到右下角；悬停或
// 键盘聚焦时自动延期，满足时间可延长的可访问性要求。
const (
	completionCardTimeout = 6 * time.Second
	completionCardDefer   = 3 * time.Second
)

type completionCardItem struct {
	id   string
	name string
	path string
}

// showCompletionCard is called on the main thread; queued completions are
// shown one at a time so simultaneous downloads remain individually actionable.
func (pg *mainPage) showCompletionCard(id, name, path string) {
	if path == "" || !mygo.App.IsReady() {
		return
	}
	pg.completionQueue = append(pg.completionQueue, completionCardItem{id: id, name: name, path: path})
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
		card := ui.Column(c).Fill().Padding(16).Gap(12).Background(p.bg).Border(1, p.bd).Radius(style.RadiusLG).Font(style.FontFamily)
		card.Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				style.Icon(c, style.IconCheckC, style.SizeIcon, p.pri)
				ui.Text(c, tr("下载完成")).Grow(1).Shrink(1).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
				d1IconButton(c, p, "x").Size(24, 24).Label(tr("关闭")).OnClick(func() { win.Close() })
			})
			ui.Text(c, item.name).FillWidth().Shrink(1).MinWidth(0).FontSize(style.TextBase).TextColor(p.mu).SingleLine().Tooltip(item.name)
			ui.Box(c).FillWidth().Grow(1)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				c.Key("completion-delete")
				del := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().Label(tr("删除"))
				hoverMotion(del, ui.Transparent, p.hover)
				del.Children(func() { d1Icon(c, "trash", 16, p.mu) })
				pg.completionDeleteMenu(c, p, del, win, item)
				d1Button(c, p, "打开文件夹", "folder").Grow(1).Shrink(1).Basis(0).OnClick(func() { win.Close(); pg.openLocalPath(filepath.Dir(item.path)) })
				d1PrimaryButton(c, p, "打开文件", "file").Grow(1).Shrink(1).Basis(0).Center().OnClick(func() { win.Close(); pg.openLocalPath(item.path) })
			})
		})
		pg.completionCardInside = card.Hovered() || card.FocusWithin()
	}
	win = mygo.NewWindow(mygo.WindowOptions{Title: tr("下载完成"), Width: width, Height: height, X: x, Y: y, Hidden: true, Frameless: true, DisableResize: true, SkipTaskbar: true, AlwaysOnTop: true, Content: ui.View(view)})
	pg.completionWindow = win
	win.OnClosed(func() {
		if pg.completionWindow == win {
			pg.completionWindow = nil
			pg.completionMenuOpen, pg.completionMenuInside = false, false
			pg.completionMenuClosePending = false
			pg.completionCardInside = false
			pg.showNextCompletionCard()
		}
	})
	win.ShowInactive()
	var armClose func(time.Duration)
	armClose = func(delay time.Duration) {
		time.AfterFunc(delay, func() {
			win.Update(func() {
				if pg.completionWindow != win {
					return
				}
				if pg.completionMenuOpen || pg.completionCardInside {
					// 菜单展开或指针仍在卡片上时推迟收起。
					armClose(completionCardDefer)
					return
				}
				win.Close()
			})
		})
	}
	armClose(completionCardTimeout)
}

// completionDeleteMenu 在删除按钮悬停或聚焦时展开两个删除选项，沿用更新
// 徽标的悬停弹层行为：进入即展开，离开后短暂延迟收起。
func (pg *mainPage) completionDeleteMenu(c *ui.Context, p *d1Palette, anchor ui.Element, win *mygo.Window, item completionCardItem) {
	anchorInside := anchor.Hovered() || anchor.FocusVisible() || anchor.Pressed()
	if anchorInside {
		pg.completionMenuOpen = true
	}
	panel := ui.PopoverBase(c, anchor, &pg.completionMenuOpen, func(tip ui.Element) {
		tip.AttachTo(anchor, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, 4, 0).
			Width(180).Padding(4).Radius(style.RadiusSM).Border(1, p.bd).Background(p.card).
			Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, .2)).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				pg.d1MenuIconItem(c, p, "trash", "删除任务", p.fg, func() {
					pg.completionMenuOpen = false
					win.Close()
					pg.deleteCompletionTask(item, false)
				})
				pg.d1MenuIconItem(c, p, "trash", "删除任务和文件", p.bad, func() {
					pg.completionMenuOpen = false
					win.Close()
					pg.deleteCompletionTask(item, true)
				})
			})
		})
	})
	panelInside := panel.Valid() && (panel.Hovered() || panel.FocusWithin())
	pg.completionMenuInside = anchorInside || panelInside
	if !pg.completionMenuInside {
		pg.completionDeleteArmClose(win)
	}
}

// completionDeleteArmClose 离开菜单后延迟收起，避免移入面板途中误关。
func (pg *mainPage) completionDeleteArmClose(win *mygo.Window) {
	if !pg.completionMenuOpen || pg.completionMenuClosePending {
		return
	}
	pg.completionMenuClosePending = true
	time.AfterFunc(300*time.Millisecond, func() {
		win.Update(func() {
			pg.completionMenuClosePending = false
			if pg.completionWindow == win && !pg.completionMenuInside {
				pg.completionMenuOpen = false
				win.Invalidate()
			}
		})
	})
}

// deleteCompletionTask 删除完成卡片对应的任务；withFiles 为真时把下载文件
// 一并移入回收站。
func (pg *mainPage) deleteCompletionTask(item completionCardItem, withFiles bool) {
	for _, r := range pg.allRows() {
		if r.id == item.id {
			pg.removeRow(r)
			break
		}
	}
	if !withFiles {
		pg.flash(tr("已删除任务，文件保留在磁盘"))
		return
	}
	if pg.win == nil {
		pg.flash(tr("已删除任务") + "；" + tr("无法删除本地文件"))
		return
	}
	win := pg.win
	go func() {
		err := trashLocalFile(item.path)
		win.Update(func() {
			if err != nil {
				pg.flash(tr("已删除任务") + "；" + tr("文件无法移至回收站"))
			} else {
				pg.flash(tr("已删除任务"))
			}
		})
	}()
}
