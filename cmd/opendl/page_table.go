package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// Task order is separate from the download manager: moving a row must never
// restart a transfer or replace its identity.
type taskDrag struct{ id string }
type taskDragFrame struct {
	active               bool
	source, name, target string
	after, over, dropped bool
}

func (pg *mainPage) taskOrder() []string {
	rows := pg.allRows()
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].rowName()), strings.ToLower(rows[j].rowName())
		if a == b {
			return rows[i].id < rows[j].id
		}
		if pg.sortDesc {
			return a > b
		}
		return a < b
	})
	order := pg.preferences.ManualTaskOrder
	if len(pg.taskDragPreview) > 0 {
		order = pg.taskDragPreview
	} else if !pg.preferences.ManualTaskSort {
		order = nil
	}
	rank := make(map[string]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, aok := rank[rows[i].id]
		b, bok := rank[rows[j].id]
		if aok != bok {
			return aok
		}
		return aok && a < b
	})
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	return ids
}

func moveTaskOrder(order []string, source, target string, after bool) []string {
	if source == target {
		return nil
	}
	sourceIndex, targetIndex := -1, -1
	for i, id := range order {
		if id == source {
			sourceIndex = i
		}
		if id == target {
			targetIndex = i
		}
	}
	if sourceIndex < 0 || targetIndex < 0 {
		return nil
	}
	result := make([]string, 0, len(order))
	for _, id := range order {
		if id == source {
			continue
		}
		if id == target && !after {
			result = append(result, source)
		}
		result = append(result, id)
		if id == target && after {
			result = append(result, source)
		}
	}
	return result
}

func (pg *mainPage) table(c *ui.Context, p *d1Palette, rows []d1Row) {
	columns := pg.visibleColumns(c)
	ui.Column(c).Grow(1).Shrink(1).Basis(0).FillWidth().Clip().Role(ui.RoleTable).Label(tr("下载任务")).Bind(&pg.tableHandle).Children(func() {
		pg.tableHead(c, p, rows, columns)
		if len(rows) == 0 {
			pg.taskDragID, pg.taskDragName = "", ""
			pg.taskDragPreview = nil
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
		drag := taskDragFrame{}
		scroll := ui.Scroll(c).Grow(1).Shrink(1).Basis(0).FillWidth().TrackScroll(&pg.taskScroll).OnClick(pg.clearTableSelection)
		_, pointerY, overScroll := scroll.PointerPosition()
		// MyGo scrolls value drags near the edge itself. Register the scroll
		// surface as a fallback target for the space after the final row.
		if value, over := ui.DragOver[taskDrag](scroll); over {
			drag.source, drag.target, drag.after, drag.over = value.id, rows[len(rows)-1].id, true, true
		}
		if value, dropped := ui.Drop[taskDrag](scroll); dropped {
			drag.source, drag.target, drag.after, drag.dropped = value.id, rows[len(rows)-1].id, true, true
		}
		if pg.taskDragID != "" && overScroll {
			for _, current := range rows {
				if current.id != pg.taskDragID {
					continue
				}
				name, size := current.rowName(), pg.rowSize(current)
				statusKey := pg.rowStatus(current)
				status := tr(statusKey)
				if !current.pending && current.snap.State == download.Running && !current.snap.Verifying && !current.snap.Reencoding && !current.snap.FinishingLive {
					if speed := pg.rowSpeed(current); speed != "-" {
						status = speed
					}
				}
				icon := pg.taskFileIcon(current)
				pct := pg.rowPct(current)
				knownProgress := current.snap.Total > 0 || (!current.pending && current.snap.State == download.Done)
				statusColor, _ := p.badgeColors(statusKey)
				progressColor := p.progressFill(statusKey)
				scroll.DrawOver(func(painter *ui.Painter, r ui.Rect) {
					y := max(r.Y, min(r.Y+r.H-style.HeightRow, r.Y+pointerY-style.HeightRow/2))
					ghost := ui.Rect{X: r.X + 8, Y: y, W: max(float32(0), r.W-16), H: style.HeightRow - 2}
					painter.Shadow(ghost, 8, 0, 3, 10, 0, ui.RGBA(0, 0, 0, .12))
					painter.Fill(ghost, p.bg, 8)
					painter.Stroke(ghost, p.bd2, 8, 1)
					x := ghost.X + style.TaskTable.PadX
					for _, column := range columns {
						textY := ghost.Y + (ghost.H-style.TextBase)/2
						// Painter.Text does not constrain its width like a UI
						// text element. Clip each cell so long names and
						// localized values cannot cover their neighbours.
						right := min(ghost.X+ghost.W-8, x+column.width-8)
						if right > x {
							cell := ui.Rect{X: x, Y: ghost.Y, W: right - x, H: ghost.H}
							painter.Clip(cell, 0, func() {
								switch column.spec.id {
								case "name":
									iconBox := ui.Rect{X: x + 3, Y: ghost.Y + (ghost.H-22)/2, W: 22, H: 22}
									if icon != nil {
										painter.Image(icon, iconBox, ui.Contain)
									} else {
										painter.Fill(iconBox, p.hover, 5)
										painter.Stroke(ui.Rect{X: iconBox.X + 6, Y: iconBox.Y + 4, W: 10, H: 14}, p.mu2, 2, 1)
										painter.Line(iconBox.X+9, iconBox.Y+9, iconBox.X+13, iconBox.Y+9, 1, p.mu2)
									}
									painter.Text(x+38, textY, name, style.TextBase, p.fg)
								case "size":
									painter.Text(x, textY, size, style.TextBase, p.mu)
								case "progress":
									if !knownProgress {
										painter.Text(x, textY, "—", style.TextBase, p.mu)
									} else if column.width < 90 {
										painter.Text(x, textY, strconv.Itoa(pct)+"%", style.TextBase, p.mu)
									} else {
										barWidth := max(float32(0), column.width-52)
										bar := ui.Rect{X: x, Y: ghost.Y + (ghost.H-4)/2, W: barWidth, H: 4}
										painter.Fill(bar, p.bd, 2)
										if pct > 0 {
											bar.W *= float32(min(pct, 100)) / 100
											painter.Fill(bar, progressColor, 2)
										}
										painter.Text(x+barWidth+8, textY, strconv.Itoa(pct)+"%", style.TextBase, p.mu)
									}
								case "status":
									painter.Text(x, textY, status, style.TextBase, statusColor)
								}
							})
						}
						x += column.width + style.TaskTable.Gap
					}
				})
				break
			}
		}
		scroll.Children(func() {
			for _, r := range rows {
				pg.tableRow(c, p, r, columns, &drag, overScroll)
			}
		})
		// Animated rows may temporarily overlap or leave gaps. Resolve the
		// insertion slot from the stable scroll layout, not whichever moving
		// row happened to win hit-testing in this frame.
		if overScroll && (drag.over || drag.dropped) {
			contentY := max(float32(0), pointerY+pg.taskScroll.Y)
			index := min(int(contentY/style.HeightRow), len(rows)-1)
			drag.target = rows[index].id
			drag.after = contentY >= float32(len(rows))*style.HeightRow ||
				contentY-float32(index)*style.HeightRow >= style.HeightRow/2
		}
		if drag.dropped {
			var order []string
			if drag.source == drag.target {
				order = append([]string(nil), pg.taskDragPreview...)
			} else {
				order = moveTaskOrder(pg.taskOrder(), drag.source, drag.target, drag.after)
			}
			if len(order) > 0 {
				pg.preferences.ManualTaskOrder = order
				pg.preferences.ManualTaskSort = true
				pg.saveAppPreferences()
			}
			pg.taskDragPreview = nil
		} else if drag.active && drag.over && drag.source != drag.target {
			if order := moveTaskOrder(pg.taskOrder(), drag.source, drag.target, drag.after); len(order) > 0 {
				changed := len(order) != len(pg.taskDragPreview)
				if !changed {
					for i := range order {
						if order[i] != pg.taskDragPreview[i] {
							changed = true
							break
						}
					}
				}
				if changed {
					pg.taskDragPreview = order
					c.AnimationFrame()
				}
			}
		} else if !drag.active {
			pg.taskDragPreview = nil
		}
		if drag.active {
			pg.taskDragID, pg.taskDragName = drag.source, drag.name
		} else {
			pg.taskDragID, pg.taskDragName = "", ""
		}
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
						if !pg.preferences.ManualTaskSort {
							icon := style.IconArrowUp
							if pg.sortDesc {
								icon = style.IconArrowDown
							}
							style.Icon(c, icon, 12, p.mu)
						}
					})
					b.OnClick(func() {
						if pg.preferences.ManualTaskSort {
							pg.preferences.ManualTaskSort = false
						} else {
							pg.sortDesc = !pg.sortDesc
						}
						pg.taskDragPreview = nil
						pg.saveAppPreferences()
					})
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

func (pg *mainPage) tableRow(c *ui.Context, p *d1Palette, r d1Row, columns []taskColumnView, drag *taskDragFrame, overScroll bool) {
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
		BorderWidth(0, 0, 1, 0).BorderColor(p.bd).Cursor(ui.CursorPointer).
		Transition(ui.ElementTransition{Position: true, Duration: 180 * time.Millisecond})
	if value, over := ui.DragOver[taskDrag](row); over {
		_, y, _ := row.PointerPosition()
		drag.source, drag.target, drag.after, drag.over = value.id, r.id, y >= style.HeightRow/2, true
		if value.id == r.id && len(pg.taskDragPreview) == 0 {
			row.Cursor(ui.CursorNotAllowed)
		}
	}
	if value, dropped := ui.Drop[taskDrag](row); dropped {
		_, y, _ := row.PointerPosition()
		drag.source, drag.target, drag.after, drag.dropped = value.id, r.id, y >= style.HeightRow/2, true
	}

	if pg.taskDragID == r.id && overScroll {
		// Hide the placeholder only while its floating copy is visible.
		// Outside the table, keep the task visible until the drag ends.
		row.Opacity(0)
	}
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
					overlay := ui.Box(c).Absolute().Right(0).Top((style.HeightRow - 1 - style.SizeIconButton) / 2).
						Width(rowActionsWidth).Height(style.SizeIconButton)
					overlay.Children(func() {
						if pg.rowActions(c, p, r, reveal, drag) {
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

func (pg *mainPage) rowActions(c *ui.Context, p *d1Palette, r d1Row, reveal bool, drag *taskDragFrame) bool {
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
		c.Key("task-reorder-handle")
		grip := ui.ButtonBase(c).Size(style.SizeIconButton, style.SizeIconButton).Shrink(0).Center().Cursor(ui.CursorGrab).
			Label(tr("拖动排序")).Tooltip(tr("拖动排序")).Drag(taskDrag{id: r.id})
		visible := reveal || grip.FocusVisible() || grip.Dragging()
		opacity := float32(0)
		if visible {
			opacity = 1
		}
		grip.Opacity(grip.Animate("grip-opacity", opacity, 140*time.Millisecond))
		if grip.Dragging() {
			grip.Cursor(ui.CursorGrabbing)
			drag.active = true
			drag.source = r.id
			drag.name = r.rowName()
		}
		if grip.Clicked() {
			clicked = true
		}
		// MyGo also paints a copy of the drag source at the pointer. The
		// row preview is the drag image, so avoid a second floating grip.
		if !grip.Dragging() {
			grip.Children(func() { d1Icon(c, "grip-vertical", 15, p.mu) })
		}
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
