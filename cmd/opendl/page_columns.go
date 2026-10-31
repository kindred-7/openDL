package main

import (
	"fmt"
	"math"

	"openDL/style"

	"github.com/egoist/mygo/ui"
)

type taskColumnSetting struct {
	ID     string  `json:"id"`
	Width  float32 `json:"width"`
	Hidden bool    `json:"hidden,omitempty"`
}

type taskColumnSpec struct {
	id, title      string
	width, minimum float32
	priority       int
	defaultHidden  bool
}

// taskColumnsVersion 2 起默认只显示名称、大小、进度、速度、剩余时间；
// 3 起默认列增加状态；4 起移除速度列，速度并入状态列（下载中显示实时速度）。
const taskColumnsVersion = 5

var taskColumns = []taskColumnSpec{
	{"name", "名称", 280, 140, 100, false},
	{"size", "大小", 76, 64, 50, false},
	{"progress", "进度", 210, 140, 90, false},
	{"queue", "队列", 92, 70, 20, true},
	{"status", "状态", 84, 64, 70, false},
	{"added", "添加", 70, 58, 10, true},
	{"remain", "剩余时间", 84, 64, 30, false},
}

type taskColumnView struct {
	spec  taskColumnSpec
	width float32
}

const rowActionsWidth = 4 * style.SizeIconButton

func taskColumnByID(id string) (taskColumnSpec, bool) {
	for _, spec := range taskColumns {
		if spec.id == id {
			return spec, true
		}
	}
	return taskColumnSpec{}, false
}

func (p *panelLayout) ensureColumns() {
	settings := make([]taskColumnSetting, 0, len(taskColumns))
	seen := make(map[string]bool, len(taskColumns))
	for _, old := range p.Columns {
		spec, ok := taskColumnByID(old.ID)
		if !ok || seen[old.ID] {
			continue
		}
		seen[old.ID] = true
		old.Hidden = old.Hidden && old.ID != "name"
		if old.Width <= 0 || math.IsNaN(float64(old.Width)) || math.IsInf(float64(old.Width), 0) {
			old.Width = spec.width
		}
		old.Width = max(spec.minimum, old.Width)
		settings = append(settings, old)
	}
	for _, spec := range taskColumns {
		if !seen[spec.id] {
			settings = append(settings, taskColumnSetting{ID: spec.id, Width: spec.width, Hidden: spec.defaultHidden})
		}
	}
	// Migrate older preferences that allowed the name column to move.
	for i := range settings {
		if settings[i].ID == "name" {
			name := settings[i]
			copy(settings[1:i+1], settings[:i])
			settings[0] = name
			break
		}
	}
	// 旧配置一次性跟随新默认：版本 2 起默认隐藏次要列，版本 3 起把状态
	// 加回默认显示；之后“显示列”里的显隐选择原样保留。
	if p.Version < taskColumnsVersion {
		for i := range settings {
			spec, ok := taskColumnByID(settings[i].ID)
			if !ok {
				continue
			}
			if p.Version < 2 && spec.defaultHidden {
				settings[i].Hidden = true
			}
			if p.Version < 3 && spec.id == "status" {
				settings[i].Hidden = false
			}
			if p.Version < 5 && spec.id == "progress" && settings[i].Width == 150 {
				settings[i].Width = spec.width
			}
		}
		p.Version = taskColumnsVersion
	}
	p.Columns = settings
}

func (pg *mainPage) columnSetting(id string) *taskColumnSetting {
	for i := range pg.panes.Columns {
		if pg.panes.Columns[i].ID == id {
			return &pg.panes.Columns[i]
		}
	}
	return nil
}

// Fit chosen columns to the viewport. Low-priority columns yield before text
// becomes unreadable; user visibility and requested widths stay remembered.
func (pg *mainPage) visibleColumns(c *ui.Context) []taskColumnView {
	views := make([]taskColumnView, 0, len(taskColumns))
	for _, setting := range pg.panes.Columns {
		spec, ok := taskColumnByID(setting.ID)
		if ok && !setting.Hidden {
			views = append(views, taskColumnView{spec: spec, width: max(spec.minimum, setting.Width)})
		}
	}
	// Keep an eight-pixel gutter for the vertical scroll thumb; header and
	// body use the same tracks, whether or not the thumb is currently visible.
	budget := max(float32(1), pg.taskViewportWidth(c)-8-2*style.TaskTable.PadX)
	for len(views) > 1 {
		minimum := float32(len(views)-1) * style.TaskTable.Gap
		lowest := -1
		for i, column := range views {
			minimumWidth := column.spec.minimum
			if i == len(views)-1 {
				required := float32(rowActionsWidth)
				if column.spec.id == "name" {
					required += 38
				}
				minimumWidth = max(minimumWidth, required)
			}
			minimum += minimumWidth
			if column.spec.id != "name" && (lowest < 0 || column.spec.priority < views[lowest].spec.priority) {
				lowest = i
			}
		}
		if minimum <= budget || lowest < 0 {
			break
		}
		views = append(views[:lowest], views[lowest+1:]...)
	}
	if len(views) > 0 {
		last := &views[len(views)-1]
		required := float32(rowActionsWidth)
		if last.spec.id == "name" {
			required += 38
		}
		last.spec.minimum = max(last.spec.minimum, required)
		last.width = max(last.width, last.spec.minimum)
	}
	available := max(float32(1), budget-float32(max(0, len(views)-1))*style.TaskTable.Gap)
	var preferred, minimum float32
	for _, column := range views {
		preferred += column.width
		minimum += column.spec.minimum
	}
	for i := range views {
		if preferred > available && preferred > minimum {
			views[i].width = views[i].spec.minimum + (views[i].width-views[i].spec.minimum)*max(float32(0), available-minimum)/(preferred-minimum)
		}
	}
	if len(views) > 0 {
		var sum float32
		for _, column := range views {
			sum += column.width
		}
		// Give spare space and rounding residuals to the filename.
		for i := range views {
			if views[i].spec.id == "name" {
				views[i].width = max(float32(1), views[i].width+available-sum)
				break
			}
		}
	}
	return views
}

type columnDrag struct{ id string }

type columnMove struct {
	source, target string
	after          bool
}

// Reorder the complete preference list so hidden columns retain their place.
func (pg *mainPage) moveTaskColumn(move columnMove) {
	if move.source == "name" || move.source == move.target || (move.target == "name" && !move.after) {
		return
	}
	from, target := -1, -1
	for i, column := range pg.panes.Columns {
		if column.ID == move.source {
			from = i
		}
		if column.ID == move.target {
			target = i
		}
	}
	if from < 0 || target < 0 {
		return
	}
	insert := target
	if move.after {
		insert++
	}
	if from < insert {
		insert--
	}
	if insert < 1 || insert == from {
		return
	}
	columns := pg.panes.Columns
	moving := columns[from]
	columns = append(columns[:from], columns[from+1:]...)
	columns = append(columns, taskColumnSetting{})
	copy(columns[insert+1:], columns[insert:])
	columns[insert] = moving
	pg.panes.Columns = columns
	pg.panelLayoutChanged()
}

func (pg *mainPage) moveTaskColumnAdjacent(id string, direction int) {
	for i, column := range pg.panes.Columns {
		if column.ID == id {
			other := i + direction
			if other >= 0 && other < len(pg.panes.Columns) {
				pg.moveTaskColumn(columnMove{source: id, target: pg.panes.Columns[other].ID, after: direction > 0})
			}
			return
		}
	}
}

// A typed in-window drag provides a shared drop operation for both surfaces.
func columnDropTarget(p *d1Palette, target ui.Element, id string, vertical bool) (columnMove, bool) {
	x, y, _ := target.PointerPosition()
	bounds := target.Bounds()
	after := x >= bounds.W/2
	if vertical {
		after = y >= bounds.H/2
	}
	if drag, over := ui.DragOver[columnDrag](target); over && drag.id != "name" && drag.id != id && (id != "name" || after) {
		target.DrawOver(func(painter *ui.Painter, r ui.Rect) {
			if vertical {
				lineY := r.Y
				if after {
					lineY = r.Y + r.H - 2
				}
				painter.Fill(ui.Rect{X: r.X + 4, Y: lineY, W: max(float32(0), r.W-8), H: 2}, p.pri, 1)
			} else {
				lineX := r.X
				if after {
					lineX = r.X + r.W - 2
				}
				painter.Fill(ui.Rect{X: lineX, Y: r.Y + 3, W: 2, H: max(float32(0), r.H-6)}, p.pri, 1)
			}
		})
	}
	if drag, dropped := ui.Drop[columnDrag](target); dropped && drag.id != "name" && (id != "name" || after) {
		return columnMove{source: drag.id, target: id, after: after}, true
	}
	return columnMove{}, false
}

func taskGrid(c *ui.Context, columns []taskColumnView) ui.Element {
	tracks := make([]ui.Track, 0, len(columns))
	for _, column := range columns {
		tracks = append(tracks, ui.Fixed(column.width))
	}
	return ui.Grid(c).FillWidth().ColumnTracks(tracks...).Padding(0, style.TaskTable.PadX).
		Gap(style.TaskTable.Gap).AlignItems(ui.Center)
}

func (pg *mainPage) resizeTaskColumn(columns []taskColumnView, index int, delta float32) {
	if index+1 >= len(columns) {
		return
	}
	left, right := columns[index], columns[index+1]
	delta = max(left.spec.minimum-left.width, min(delta, right.width-right.spec.minimum))
	if delta == 0 {
		return
	}
	for _, column := range columns {
		if setting := pg.columnSetting(column.spec.id); setting != nil {
			setting.Width = column.width
		}
	}
	pg.columnSetting(left.spec.id).Width += delta
	pg.columnSetting(right.spec.id).Width -= delta
	pg.panelLayoutChanged()
}

func (pg *mainPage) columnResizeHandle(c *ui.Context, p *d1Palette, columns []taskColumnView, index int) {
	if index+1 >= len(columns) {
		return
	}
	c.Key("column-resize:" + columns[index].spec.id)
	edge := ui.Box(c).Absolute().Right(-4).Top(0).Width(8).FillHeight().Focusable().Role(ui.RoleSplitter).
		Label(tr("调整") + columns[index].spec.title + tr("列宽")).Cursor(ui.CursorResizeEW)
	dx, _, held := edge.Dragged()
	if held && dx != 0 {
		edge.Focus()
		pg.resizeTaskColumn(columns, index, dx)
	}
	edge.OnShortcut(0, ui.KeyLeft, func() { pg.resizeTaskColumn(columns, index, -8) }).
		OnShortcut(0, ui.KeyRight, func() { pg.resizeTaskColumn(columns, index, 8) })
	if edge.Hovered() || held || edge.FocusVisible() {
		edge.Draw(func(painter *ui.Painter, r ui.Rect) {
			painter.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y + 5, W: 2, H: max(float32(0), r.H-10)}, p.bd2, 1)
		})
	}
}

func (pg *mainPage) columnMenuButton(c *ui.Context, p *d1Palette) ui.Element {
	c.Key("column-menu")
	trigger := d1SelectionActionBase(c, p, "显示列", "funnel", p.mu, true, false).Role(ui.RoleMenuButton).Expanded(pg.showColumns).Description(tr("显示列"))
	// The native caption controls paint above the client area. Keep this
	// toolbar tip below its button so it cannot overlap the close button.
	ui.TooltipBase(c, trigger, func(tip ui.Element) {
		tip.AttachTo(trigger, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(6, 0, 0, 0).
			Padding(5, 8).Radius(5).Background(p.fg).TextColor(p.bg).FontSize(style.TextBase-1).
			Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, .2)).Children(func() {
			ui.Text(c, tr("显示列"))
		})
	})
	trigger.OnClick(func() { pg.showColumns = !pg.showColumns })
	trigger.OnShortcut(0, ui.KeyDown, func() { pg.showColumns = true })
	shown := map[string]bool{}
	for _, column := range pg.visibleColumns(c) {
		shown[column.spec.id] = true
	}
	ui.PopoverBase(c, trigger, &pg.showColumns, func(panel ui.Element) {
		d1DropdownPanel(panel, p).Width(230).AttachTo(trigger, ui.AnchorBottomEnd, ui.AnchorTopEnd).Children(func() {
			ui.Text(c, tr("显示列")).FontSize(style.TextSM).TextColor(p.mu).Padding(6, 8)
			var pending columnMove
			hasDrop := false
			for _, setting := range pg.panes.Columns {
				spec, ok := taskColumnByID(setting.ID)
				if !ok {
					continue
				}
				id, on := spec.id, !setting.Hidden
				c.Key(id)
				row := ui.Row(c).FillWidth().Height(32).AlignItems(ui.Center)
				if move, dropped := columnDropTarget(p, row, id, true); dropped {
					pending, hasDrop = move, true
				}
				row.Children(func() {
					c.Key("visibility")
					item := ui.CheckboxBase(c, &on).Grow(1).Shrink(1).Basis(0).MinWidth(0).Height(32).Padding(0, 8).Gap(8).AlignItems(ui.Center).Radius(5).
						Label(tr(spec.title)).Disabled(id == "name")
					if item.Hovered() || item.FocusVisible() {
						item.Background(p.hover)
					}
					item.Children(func() {
						d1CheckMark(c, p, on)
						ui.Text(c, tr(spec.title)).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
						if on && !shown[id] {
							ui.Text(c, tr("空间不足")).MarginStart(ui.Auto).Shrink(1).FontSize(style.TextXS).TextColor(p.mu).SingleLine()
						}
					})
					item.OnClick(func() {
						if value := pg.columnSetting(id); value != nil && id != "name" {
							value.Hidden = !value.Hidden
							pg.panelLayoutChanged()
						}
					})
					if id == "name" {
						ui.Box(c).Size(28, 28).Shrink(0)
					} else {
						c.Key("reorder")
						grip := ui.ButtonBase(c).Size(28, 28).Radius(style.RadiusSM).Center().
							Label(fmt.Sprintf(tr("调整%s列顺序"), tr(spec.title))).Description(tr("拖动排序，或使用 Alt+Shift+上、下方向键")).
							Tooltip(tr("拖动调整列顺序")).Cursor(ui.CursorGrab).Drag(columnDrag{id: id})
						if grip.Dragging() {
							grip.Cursor(ui.CursorGrabbing)
						}
						if grip.Hovered() || grip.FocusVisible() {
							grip.Background(p.hover)
						}
						grip.Children(func() { d1Icon(c, "grip-vertical", 16, p.mu) })
						grip.OnShortcut(ui.Alt|ui.Shift, ui.KeyUp, func() { pg.moveTaskColumnAdjacent(id, -1) }).
							OnShortcut(ui.Alt|ui.Shift, ui.KeyDown, func() { pg.moveTaskColumnAdjacent(id, 1) })
					}
				})
			}
			if hasDrop {
				pg.moveTaskColumn(pending)
				c.AnimationFrame()
			}
			d1MenuSep(c, p)
			d1DropdownItem(c, p, "重置列顺序、宽度和显示", "redo", true).OnClick(func() { pg.panes.Columns = nil; pg.panes.ensureColumns(); pg.panelLayoutChanged() })
		})
	})
	return trigger
}
