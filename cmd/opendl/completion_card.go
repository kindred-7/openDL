package main

import (
	"os"
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
	// Leave enough room for the three equally sized action labels and the
	// full bottom padding.
	const cardWidth, cardHeight, inset = 440, 160, 16
	area := mygo.Screen.PrimaryDisplay().WorkArea
	width, height := cardWidth, cardHeight
	if area.Width > 0 {
		width = min(width, max(1, area.Width-2*inset))
	}
	if area.Height > 0 {
		height = min(height, max(1, area.Height-2*inset))
	}
	x, y := area.X+area.Width-width-inset, area.Y+area.Height-height-inset
	var win *mygo.Window
	view := func(c *ui.Context) {
		base := pg.baseTheme()
		p := base.palette
		if isBlenderThemeMode(pg.themeMode) {
			c.SetTheme(base.theme)
		}
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
				del := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().Label(tr("删除")).Cursor(ui.CursorPointer)
				hoverMotion(del, ui.Transparent, p.hover)
				del.Children(func() { d1Icon(c, "trash", 16, p.mu) })
				pg.completionDeleteMenu(c, p, del, win, item)
				pg.completionCutControl(c, p, win, item)
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
			pg.completionDeleteMenuState = completionMenuState{}
			pg.completionCutMenuState = completionMenuState{}
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
				if pg.completionDeleteMenuState.open || pg.completionCutMenuState.open || pg.completionCardInside {
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

// completionMenuState 记录一个悬停弹层的展开状态；删除与剪切的弹层各持一份。
type completionMenuState struct {
	open, inside, closePending bool
}

// completionMenuWidth 是悬停弹层的默认宽度。
const completionMenuWidth = 180

// completionHoverMenu 沿用更新徽标的悬停弹层行为：进入即展开，离开后短暂
// 延迟收起。panelWidth 为面板宽度，非正时取默认值；openOn 判定锚点是否处于
// 激活，items 构建面板条目。
func (pg *mainPage) completionHoverMenu(c *ui.Context, p *d1Palette, anchor ui.Element, st *completionMenuState, win *mygo.Window, panelWidth float32, openOn bool, items func()) {
	if panelWidth <= 0 {
		panelWidth = completionMenuWidth
	}
	if openOn {
		st.open = true
	}
	panel := ui.PopoverBase(c, anchor, &st.open, func(tip ui.Element) {
		style.ShadowOverlay(tip.AttachTo(anchor, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, 4, 0).
			Width(panelWidth).Padding(4).Radius(style.RadiusMD).Border(1, p.bd).Background(p.card)).Children(func() {
			ui.Column(c).Gap(2).Children(items)
		})
	})
	panelInside := panel.Valid() && (panel.Hovered() || panel.FocusWithin())
	st.inside = openOn || panelInside
	if !st.inside {
		pg.completionMenuArmClose(win, st)
	}
}

// completionMenuArmClose 离开弹层后延迟收起，避免移入面板途中误关。
func (pg *mainPage) completionMenuArmClose(win *mygo.Window, st *completionMenuState) {
	if !st.open || st.closePending {
		return
	}
	st.closePending = true
	time.AfterFunc(300*time.Millisecond, func() {
		win.Update(func() {
			st.closePending = false
			if pg.completionWindow == win && !st.inside {
				st.open = false
				win.Invalidate()
			}
		})
	})
}

// completionDeleteMenu 在删除按钮悬停或聚焦时展开两个删除选项。
func (pg *mainPage) completionDeleteMenu(c *ui.Context, p *d1Palette, anchor ui.Element, win *mygo.Window, item completionCardItem) {
	st := &pg.completionDeleteMenuState
	c.Key("completion-delete-menu")
	pg.completionHoverMenu(c, p, anchor, st, win, completionMenuWidth, anchor.Hovered() || anchor.FocusVisible() || anchor.Pressed(), func() {
		pg.d1MenuIconItem(c, p, "trash", "删除任务", p.fg, func() {
			st.open = false
			win.Close()
			pg.deleteCompletionTask(item, false)
		})
		pg.d1MenuIconItem(c, p, "trash", "删除任务和文件", p.bad, func() {
			st.open = false
			win.Close()
			pg.deleteCompletionTask(item, true)
		})
	})
}

// completionCutControl 是“剪切文件”图标文字按钮：点击剪切文件，悬停或键盘
// 聚焦时展开“复制文件”选项，弹层与按钮同宽。
func (pg *mainPage) completionCutControl(c *ui.Context, p *d1Palette, win *mygo.Window, item completionCardItem) {
	cut := d1Button(c, p, "剪切文件", "scissors").Grow(1).Shrink(1).Basis(0).OnClick(func() {
		win.Close()
		pg.copyCompletionFile(item, true)
	})
	st := &pg.completionCutMenuState
	c.Key("completion-cut-menu")
	pg.completionHoverMenu(c, p, cut, st, win, cut.LayoutBounds().W, cut.Hovered() || cut.FocusVisible(), func() {
		pg.d1MenuIconItem(c, p, "copy", "复制文件", p.fg, func() {
			st.open = false
			win.Close()
			pg.copyCompletionFile(item, false)
		})
	})
}

// copyCompletionFile 把完成文件放入剪贴板供粘贴；cut 为真时按剪切语义写入，
// 粘贴时移动文件，否则粘贴复制文件。
func (pg *mainPage) copyCompletionFile(item completionCardItem, cut bool) {
	if _, err := os.Stat(item.path); err != nil {
		pg.flash("文件已失效，请重新下载")
		return
	}
	data, err := fileClipboardData(item.path, cut)
	if err == nil {
		err = mygo.Clipboard.Write(data)
	}
	if err != nil {
		if cut {
			pg.flash("无法剪切文件到剪贴板：%s", err.Error())
		} else {
			pg.flash("无法复制文件到剪贴板：%s", err.Error())
		}
		return
	}
	if cut {
		pg.flash("已剪切文件到剪贴板")
	} else {
		pg.flash("已复制文件到剪贴板")
	}
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
		pg.flash("已删除任务，文件保留在磁盘")
		return
	}
	if pg.win == nil {
		pg.flash("已删除任务；无法删除本地文件")
		return
	}
	win := pg.win
	go func() {
		err := trashLocalFile(item.path)
		win.Update(func() {
			if err != nil {
				pg.flash("已删除任务；文件无法移至回收站")
			} else {
				pg.flash("已删除任务")
			}
		})
	}()
}
