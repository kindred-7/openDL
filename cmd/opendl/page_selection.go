package main

import (
	"fmt"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

// Selection actions replace column labels in the same 30px header, so the task
// rows stay still when entering or leaving selection mode.
func (pg *mainPage) d1SelectionHeader(c *ui.Context, p *d1Palette, rows []d1Row, allOn bool, count, hidden int) {
	selected := pg.selection()
	single := len(selected) == 1
	canOpen := single && !selected[0].pending &&
		selected[0].snap.State == download.Done && selected[0].snap.FilePath != ""
	canFinishLive := single && !selected[0].pending && selected[0].snap.LiveRecording
	gap, pad, buttonSize, fontSize := float32(10), style.TaskTable.PadX, style.SizeIconButton, style.TextBase
	dense := pg.taskViewportWidth(c) < 440
	if dense {
		gap, pad, buttonSize, fontSize = 4, 8, 24, style.TextSM
	}
	separator := func(beforeCancel bool) {
		if dense {
			ui.Box(c).Width(1).Height(16).Background(p.bd)
		} else if beforeCancel {
			ui.Box(c).Width(1).Height(18).Background(p.bd).MarginStart(6)
		} else {
			d1Sep(c, p)
		}
	}
	c.Key("table-selection-head")
	ui.Row(c).FillWidth().Height(style.HeightTableHead).Padding(0, pad).PaddingEnd(style.TaskTable.PadX+8).
		Gap(gap).AlignItems(ui.Center).Background(p.card).
		BorderWidth(1, 0, 1, 0).BorderColor(p.bd).Role(ui.RoleToolbar).Label(tr("已选任务操作")).Children(func() {
		c.Key("select-visible")
		ui.Box(c).Size(buttonSize, 28).Center().Children(func() {
			cb := d1Checkbox(c, p, allOn, true, !allOn).
				Label(tr("选择全部任务")).Tooltip(tr("全选或取消选择当前列表")).Disabled(len(rows) == 0)
			cb.OnClick(func() { pg.selectVisibleRows(rows, allOn) })
		})
		label := fmt.Sprintf(tr("已选 %d 项"), count)
		hint := label
		if hidden > 0 {
			hint += fmt.Sprintf(tr("，其中 %d 项在其他筛选中"), hidden)
		}
		ui.Text(c, tr(label)).FontSize(fontSize).TextColor(p.fg).SingleLine().Tooltip(tr(hint))
		separator(false)
		d1SelectionAction(c, p, "打开文件", "external-link", p.mu, canOpen).Size(buttonSize, buttonSize).OnClick(func() {
			if canOpen {
				pg.openRowFile(selected[0])
			}
		})
		d1SelectionAction(c, p, "打开所在文件夹", style.IconFolderOpen, p.mu, single).Size(buttonSize, buttonSize).OnClick(func() {
			if single {
				pg.openRowFolder(selected[0])
			}
		})
		if canFinishLive {
			d1SelectionAction(c, p, "结束录制并保存", "stop", p.mu, true).Size(buttonSize, buttonSize).OnClick(func() {
				if pg.mgr != nil && pg.mgr.FinishLive(selected[0].id) {
					pg.flash(tr("正在结束录制并保存"))
				}
			})
		}
		d1SelectionAction(c, p, "删除所选任务", "trash", p.bad, true).Size(buttonSize, buttonSize).OnClick(pg.deleteSelection)
		separator(true)
		d1SelectionAction(c, p, "取消选择", "close", p.mu, true).Size(buttonSize, buttonSize).OnClick(pg.clearTableSelection)
		ui.Box(c).Grow(1).Shrink(1).Basis(0)
	})
}

func d1SelectionAction(c *ui.Context, p *d1Palette, label, icon string, color ui.Color, enabled bool) ui.Element {
	return d1SelectionActionBase(c, p, label, icon, color, enabled, true)
}

func d1SelectionActionBase(c *ui.Context, p *d1Palette, label, icon string, color ui.Color, enabled, tooltip bool) ui.Element {
	c.Key(label)
	e := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).
		Center().Label(tr(label)).Disabled(!enabled)
	if tooltip {
		e.Tooltip(tr(label))
	}
	if enabled {
		hoverMotion(e, ui.Transparent, p.hover)
	}
	e.Children(func() { d1Icon(c, icon, style.SizeIcon, color) })
	return e
}
