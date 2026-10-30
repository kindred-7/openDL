package main

import (
	"os"
	"strconv"
	"time"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func (pg *mainPage) table(c *ui.Context, p *d1Palette, rows []d1Row) {
	columns := pg.visibleColumns(c)
	ui.Column(c).Grow(1).Shrink(1).Basis(0).FillWidth().Clip().Role(ui.RoleTable).Label(tr("下载任务")).Bind(&pg.tableHandle).Children(func() {
		pg.tableHead(c, p, rows, columns)
		if len(rows) == 0 {
			ui.Column(c).Grow(1).Shrink(1).Basis(0).Center().Gap(8).Children(func() {
				ui.Text(c, tr(pg.emptyTitle())).FontSize(style.TextBase).TextColor(p.fg)
				ui.Text(c, tr(pg.emptyHint())).FontSize(style.TextSM).TextColor(p.mu)
				d1Button(c, p, "添加 URL", "plus").OnClick(func() { pg.openAdd(false) })
			})
			return
		}
		// The scroll surface receives clicks only where no row or row action
		// handles them, so blank space clears the existing selection.
		c.Key("task-table-scroll")
		ui.Scroll(c).Grow(1).Shrink(1).Basis(0).FillWidth().OnClick(pg.clearTableSelection).Children(func() {
			for _, r := range rows {
				pg.tableRow(c, p, r, columns)
			}
		})
	})
	if pg.showMenu {
		pg.contextMenu(c, p)
	}
}

func (pg *mainPage) tableHead(c *ui.Context, p *d1Palette, rows []d1Row, columns []taskColumnView) {
	n, hidden := pg.selectionCounts(rows)
	allOn := len(rows) > 0
	for _, r := range rows {
		if !pg.rowChecked(r.id) {
			allOn = false
		}
	}
	if n+hidden > 0 {
		pg.d1SelectionHeader(c, p, rows, allOn, n+hidden, hidden)
		return
	}
	c.Key("table-column-head")
	head := taskGrid(c, columns).Height(style.HeightTableHead).Background(p.card).BorderWidth(1, 0, 1, 0).BorderColor(p.bd)
	var pending columnMove
	hasDrop := false
	head.Children(func() {
		for i, column := range columns {
			c.Key(column.spec.id)
			cell := ui.Row(c).Width(column.width).Height(style.HeightTableHead - 2).AlignItems(ui.Center).
				Role(ui.RoleColumnHeader).Label(tr(column.spec.title))
			id := column.spec.id
			if id != "name" {
				cell.Focusable().Description(tr("拖动调整列顺序，或使用 Alt+Shift+方向键")).Cursor(ui.CursorGrab).Drag(columnDrag{id: id})
				if cell.Dragging() {
					cell.Cursor(ui.CursorGrabbing)
				}
				cell.OnShortcut(ui.Alt|ui.Shift, ui.KeyLeft, func() { pg.moveTaskColumnAdjacent(id, -1) }).
					OnShortcut(ui.Alt|ui.Shift, ui.KeyRight, func() { pg.moveTaskColumnAdjacent(id, 1) })
			}
			if move, dropped := columnDropTarget(p, cell, id, false); dropped {
				pending, hasDrop = move, true
			}
			cell.Children(func() {
				if column.spec.id == "name" {
					ui.Box(c).Size(28, 28).Center().Children(func() {
						cb := d1Checkbox(c, p, false, len(rows) > 0 && head.Hovered(), false).Label(tr("选择全部任务")).Disabled(len(rows) == 0)
						cb.OnClick(func() { pg.selectVisibleRows(rows, false) })
					})
					ui.Box(c).Width(10)
					b := ui.ButtonBase(c).Gap(4).AlignItems(ui.Center).Label(tr("按名称排序"))
					b.Children(func() {
						ui.Text(c, tr("名称")).FontSize(style.TextXS).TextColor(p.mu).SingleLine()
						icon := style.IconArrowUp
						if pg.sortDesc {
							icon = style.IconArrowDown
						}
						style.Icon(c, icon, 12, p.mu)
					})
					b.OnClick(func() { pg.sortDesc = !pg.sortDesc; pg.saveAppPreferences() })
				} else {
					ui.Text(c, tr(column.spec.title)).FontSize(style.TextXS).TextColor(p.mu).SingleLine()
				}
				pg.columnResizeHandle(c, p, columns, i)
			})
		}
	})
	if hasDrop {
		pg.moveTaskColumn(pending)
		c.AnimationFrame()
	}
}

// dragFilePaths 返回拖出的文件集合：勾选多个已完成任务时一起拖出。
func (pg *mainPage) dragFilePaths(r d1Row) []string {
	if !pg.checked[r.id] {
		return []string{r.snap.FilePath}
	}
	paths := []string{}
	for _, row := range pg.allRows() {
		if pg.checked[row.id] && row.snap.State == download.Done && row.snap.FilePath != "" {
			paths = append(paths, row.snap.FilePath)
		}
	}
	if len(paths) == 0 {
		return []string{r.snap.FilePath}
	}
	return paths
}

func (pg *mainPage) tableRow(c *ui.Context, p *d1Palette, r d1Row, columns []taskColumnView) {
	childClicked := false
	nameClicked, nameRightClicked := false, false
	var nameClickMods ui.Modifiers
	status := pg.rowStatus(r)
	missing := !r.pending && r.snap.State == download.Done && pg.fileMissing[r.id]
	nameColor, detailColor := p.fg, p.mu
	if missing {
		background := p.card
		if pg.sel == r.id || pg.checked[r.id] {
			background = p.sel
		}
		nameColor = p.mu2.Mix(background, 0.35)
		detailColor = nameColor
	}
	on := pg.sel == r.id || pg.checked[r.id]
	c.Key(r.id)
	row := taskGrid(c, columns).Height(style.HeightRow).Focusable().Role(ui.RoleRow).Selected(on).Label(r.rowName()).
		BorderWidth(0, 0, 1, 0).BorderColor(p.bd).Cursor(ui.CursorPointer)
	if !r.pending && r.snap.State == download.Done && r.snap.FilePath != "" {
		operation := transfer.Move
		if pg.preferences.DragFileAction == "copy" {
			operation = transfer.Copy
		}
		var originals map[string]os.FileInfo
		row.DragDataFrom(func() transfer.Data {
			paths := pg.dragFilePaths(r)
			originals = make(map[string]os.FileInfo, len(paths))
			if operation == transfer.Move {
				for _, path := range paths {
					if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
						originals[path] = info
					}
				}
			}
			data, err := transfer.FileData(paths...)
			if err != nil {
				return transfer.Data{}
			}
			return data
		}, transfer.DragOptions{Operations: operation, Done: func(result transfer.Result) {
			if result.Operation != transfer.Move || result.Canceled || result.Err != nil {
				return
			}
			files := originals
			go func() {
				failed := false
				for path, original := range files {
					current, err := os.Lstat(path)
					if err == nil && os.SameFile(original, current) {
						if err := os.Remove(path); err != nil {
							failed = true
						}
					}
				}
				if failed && pg.win != nil {
					pg.win.Update(func() { pg.flash(tr("移动后无法移除原文件")) })
				}
			}()
		}})
	}
	if on {
		row.Background(p.sel)
	} else if row.Hovered() {
		row.Background(p.hover)
	}
	row.Children(func() {
		for i, column := range columns {
			c.Key(column.spec.id)
			last := i == len(columns)-1
			reveal := row.Hovered() || row.FocusWithin()
			ui.Row(c).Width(column.width).Height(style.HeightRow - 1).MinWidth(0).Clip().AlignItems(ui.Center).Children(func() {
				value := ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center)
				if last && reveal && column.spec.id != "name" {
					value.Opacity(0)
				}
				value.Children(func() {
					switch column.spec.id {
					case "name":
						checked := pg.rowChecked(r.id)
						cb := d1FileSlot(c, p, r, pg.taskFileIcon(r), checked, row.Hovered() || row.FocusVisible(), false, missing).Shrink(0).Label(tr("选择任务 ") + r.rowName())
						if cb.Clicked() {
							childClicked = true
							pg.toggleTableCheckbox(r.id)
						}
						ui.Box(c).Width(10).Shrink(0)
						c.Key("task-name-hit")
						nameValue := ui.Row(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(6).AlignItems(ui.Center)
						if last && reveal {
							nameValue.Opacity(0)
						}
						nameClicked = nameValue.Clicked()
						nameClickMods = nameValue.ClickModifiers()
						nameRightClicked = nameValue.RightClicked()
						nameValue.Children(func() {
							ui.Text(c, r.rowName()).Grow(1).Shrink(1).Basis(0).MinWidth(0).Tooltip(r.rowName()).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(nameColor).SingleLine()
						})
					case "progress":
						pct := pg.rowPct(r)
						// 尾部留白加大，与后一列（默认速度）拉开间隔。
						ui.Row(c).FillWidth().PaddingEnd(16).Gap(6).AlignItems(ui.Center).Tooltip(pg.rowDone(r) + " / " + pg.rowSize(r)).Children(func() {
							bar := ui.Box(c).Grow(1).Shrink(1).Basis(0).Height(4)
							count := r.snap.Connections
							if count == -1 {
								count = 0
								for _, segment := range r.snap.Segments {
									if segment.Active {
										count++
									}
								}
								if count == 0 && len(r.snap.Segments) > 0 {
									count = len(r.snap.Segments)
								}
							}
							if (!r.pending && r.snap.State == download.Done) || r.snap.Total <= 0 || len(r.snap.Segments) <= 1 || count <= 1 {
								count = 1
							}
							count = min(count, 32)
							fractions := make([]float32, count)
							for i := range fractions {
								fraction := float32(pct) / 100
								if count > 1 {
									total := r.snap.Total
									start := total/int64(count)*int64(i) + total%int64(count)*int64(i)/int64(count)
									end := total/int64(count)*int64(i+1) + total%int64(count)*int64(i+1)/int64(count)
									fraction = sliceCoverage(total, r.snap.Segments, start, end)
								}
								fractions[i] = bar.Animate(i, fraction, 700*time.Millisecond)
							}
							bar.Draw(func(painter *ui.Painter, bounds ui.Rect) {
								gap := min(float32(1), bounds.W/float32(count*2))
								width := max(float32(0), (bounds.W-gap*float32(count-1))/float32(count))
								for i, fraction := range fractions {
									cell := ui.Rect{X: bounds.X + float32(i)*(width+gap), Y: bounds.Y, W: width, H: bounds.H}
									painter.Fill(cell, p.bd, min(width/2, float32(2)))
									if fraction > 0 {
										cell.W *= fraction
										painter.Fill(cell, p.progressFill(status), min(cell.W/2, float32(2)))
									}
								}
							})
							ui.Text(c, strconv.Itoa(pct)+"%").Shrink(0).FontSize(style.TextBase).TextColor(detailColor).SingleLine()
						})
					case "status":
						fg, border := p.badgeColors(status)
						if missing {
							fg = detailColor
						}
						text := tr(status)
						if r.snap.State == download.Running && !r.snap.Verifying && !r.snap.Reencoding && !r.snap.FinishingLive {
							if speed := pg.rowSpeed(r); speed != "-" {
								text = speed
							}
						}
						tooltip := text
						if r.snap.Err != "" {
							tooltip = r.snap.Err
						}
						ui.Row(c).Padding(style.BadgePadY, style.BadgePadX).Radius(style.BadgeRadius).Border(1, border).Children(func() {
							ui.Text(c, text).FontSize(style.TextXS).TextColor(fg).SingleLine().Tooltip(tooltip)
						})
					default:
						value := ""
						switch column.spec.id {
						case "size":
							value = pg.rowSize(r)
						case "remain":
							value = pg.rowRemain(r)
						case "queue":
							value = pg.rowQueue(r)
						case "added":
							value = pg.rowAdded(r)
						}
						ui.Text(c, value).FillWidth().Tooltip(value).FontSize(style.TextBase).TextColor(detailColor).SingleLine()
					}
				})
				if last {
					left := float32(0)
					if column.spec.id == "name" {
						left = 38
					} // Keep the file/selection slot available.
					overlay := ui.Box(c).Absolute().Left(left).Top((style.HeightRow - 1 - style.SizeIconButton) / 2).
						Width(rowActionsWidth).Height(style.SizeIconButton)
					overlay.Children(func() {
						if pg.rowActions(c, p, r, reveal) {
							childClicked = true
						}
					})
				}
			})
		}
	})
	if row.Focused() {
		mods, key := pg.shortcut("toggle")
		row.OnShortcut(mods, key, func() { pg.sel = r.id; pg.toggleRow(r) })
	}
	rowClicked := row.Clicked()
	if !childClicked && (nameClicked || rowClicked) {
		mods := row.ClickModifiers()
		if nameClicked {
			mods = nameClickMods
		}
		now := time.Now()
		// Selection can rebuild the table between presses. Track the stable
		// task identity, rather than the transient hit element, across them.
		if mods == 0 && pg.lastRowClickID == r.id && now.Sub(pg.lastRowClickAt) <= 500*time.Millisecond {
			pg.lastRowClickID = ""
			pg.lastRowClickAt = time.Time{}
			pg.toggleRow(r)
		} else {
			if mods == 0 {
				pg.lastRowClickID, pg.lastRowClickAt = r.id, now
			} else {
				pg.lastRowClickID = ""
			}
			pg.selectTableRow(r.id, mods)
			row.Focus()
		}
	}
	if nameRightClicked || row.RightClicked() {
		pg.sel = r.id
		pg.menuRow = r.id
		row.Focus()
		bounds := row.Bounds()
		x, y, _ := row.PointerPosition()
		pg.menuX, pg.menuY = bounds.X+x, bounds.Y+y
		pg.showMenu = true
	}
}

func (pg *mainPage) rowActions(c *ui.Context, p *d1Palette, r d1Row, reveal bool) bool {
	clicked := false
	group := ui.Row(c).Width(rowActionsWidth).Height(style.SizeIconButton).AlignItems(ui.Center)
	if !reveal && !group.FocusWithin() {
		group.Opacity(0)
	}
	group.Children(func() {
		icon, label := "play", "开始任务"
		if !r.pending && !r.snap.Held && (r.snap.State == download.Running || r.snap.State == download.Queued) {
			icon, label = "pause", "暂停任务"
		}
		done := !r.pending && r.snap.State == download.Done
		if done {
			icon, label = "redo", "重新下载"
		}
		button := rowActionButton(c, p, "切换下载状态", label, icon, true)
		if button.Clicked() {
			clicked = true
		}
		button.OnClick(func() {
			if done {
				pg.redownload(r)
			} else if r.pending || r.snap.State != download.Done {
				pg.toggleRow(r)
			}
		})
		button = rowActionButton(c, p, "打开文件位置", "打开文件位置", style.IconFolderOpen, true)
		if button.Clicked() {
			clicked = true
		}
		button.OnClick(func() { pg.openRowFolder(r) })
		icon = "panel-bottom"
		if pg.detailPlacement == "right" {
			icon = "panel-right"
		}
		label = "打开详情面板"
		if !pg.detailCollapsed && pg.sel == r.id {
			label = "收起详情"
		}
		button = rowActionButton(c, p, "详情面板", label, icon, true)
		if button.Clicked() {
			clicked = true
		}
		button.OnClick(func() {
			if !pg.detailCollapsed && pg.sel == r.id {
				pg.hideDetails()
			} else {
				pg.sel = r.id
				pg.showDetails()
			}
		})
	})
	return clicked
}

// Row actions are transparent in every pointer state. Icon color and the
// framework's keyboard focus ring provide feedback without a button surface.
func rowActionButton(c *ui.Context, p *d1Palette, key, label, icon string, enabled bool) ui.Element {
	c.Key(key)
	button := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Radius(style.RadiusSM).Center().Label(tr(label)).Tooltip(tr(label)).Disabled(!enabled)
	hovered := enabled && button.Hovered()
	pressed := enabled && button.Pressed()
	color := p.mu
	if hovered || pressed || (enabled && button.FocusVisible()) {
		color = p.fg
	}
	targetSize := style.SizeIcon
	if hovered && !pressed {
		targetSize += 2
	}
	iconSize := button.Animate("row-action-icon-size", targetSize, 120*time.Millisecond)
	button.Children(func() { d1Icon(c, icon, iconSize, color) })
	return button
}
