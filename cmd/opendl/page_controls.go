package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"openDL/download"
	"openDL/pathrec"
	"openDL/style"

	"github.com/egoist/mygo/ui"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// The compact component sizes follow design.html; all states use shared tokens.
type d1Option struct{ value, label, icon string }

func optionLabel(option d1Option) string {
	if strings.HasPrefix(option.value, "cat:") {
		return option.label
	}
	return tr(option.label)
}

func d1Choice(c *ui.Context, p *d1Palette, label string, value *string, options []d1Option) ui.Element {
	c.Key(label)
	choice := ui.SelectBase(c, value)
	text := tr(label)
	for _, o := range options {
		if o.value == *value {
			text = optionLabel(o)
		}
	}
	width, _ := c.Size()
	maxWidth := float32(200)
	if width < 1300 {
		maxWidth = 140
	}
	choice.Trigger.MaxWidth(maxWidth).Height(style.HeightButton).Padding(0, 10).Gap(6).Radius(style.RadiusSM).Justify(ui.Start).
		Border(1, p.bd).Label(tr(label))
	hoverMotion(choice.Trigger, p.bg, p.hover)
	arrow := choice.Trigger.Animate("choice-arrow", 180*motionTarget(choice.Open()), selectMotion)
	choice.Trigger.Children(func() {
		if icon := map[string]string{"类型": "list-filter", "日期": "calendar"}[label]; icon != "" {
			d1Icon(c, icon, 16, p.mu)
		}
		ui.Text(c, text).Grow(1).Shrink(1).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(text)
		d1Icon(c, "chev", 16, p.mu).Shrink(0).Rotate(arrow)
	})
	choice.Popup(func(panel ui.Element) {
		d1DropdownPanel(panel, p).Width(choice.Trigger.Bounds().W).MaxHeight(260).Children(func() {
			ui.Scroll(c).MaxHeight(248).FillWidth().Children(func() {
				for _, o := range options {
					c.Key(o.value)
					item := choice.Item(o.value).FillWidth().Padding(6, 8).Gap(8).Radius(5).Justify(ui.Start).Cursor(ui.CursorPointer)
					if item.Highlighted() {
						item.Background(p.hover)
					}
					item.Children(func() {
						if o.icon != "" {
							d1Icon(c, o.icon, 16, p.mu)
						}
						ui.Text(c, optionLabel(o)).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(optionLabel(o))
					})
				}
			})
		})
	})
	return choice.Trigger
}

func d1DropdownPanel(panel ui.Element, p *d1Palette) ui.Element {
	return panel.Padding(4).Radius(8).Border(1, p.bd2).Background(p.bg).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, .14))
}

// d1MenuRow 是弹层行的共用外壳：配色、圆角、间距与聚焦语义统一。
func d1MenuRow(c *ui.Context, p *d1Palette, label string, enabled bool, keys ...string) ui.Element {
	key := label
	if len(keys) > 0 {
		key = keys[0]
	}
	c.Key(key)
	item := ui.ButtonBase(c).FillWidth().Padding(6, 8).Gap(8).Radius(5).
		Role(ui.RoleMenuItem).Label(tr(label)).Disabled(!enabled).Justify(ui.Start)
	hoverMotion(item, ui.Transparent, p.hover)
	return item
}

func d1DropdownItem(c *ui.Context, p *d1Palette, label, icon string, enabled bool, shortcut ...string) ui.Element {
	item := d1MenuRow(c, p, label, enabled)
	item.Children(func() {
		if icon != "" {
			d1Icon(c, icon, 16, p.mu)
		}
		ui.Text(c, tr(label)).Grow(1).Shrink(1).Basis(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine().Tooltip(tr(label))
		if len(shortcut) > 0 && shortcut[0] != "" {
			ui.Text(c, shortcut[0]).Shrink(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine()
		}
	})
	return item
}

// d1MultiChoice 是多选下拉：触发器与单选下拉一致，弹层行带复选标记，
// 点选不关闭弹层，可连续勾选；当前值与选项均靠左，箭头在触发器右侧。
func d1MultiChoice(c *ui.Context, p *d1Palette, label, icon string, open *bool, selected map[string]bool, options []d1Option) ui.Element {
	c.Key(label)
	text := tr(label)
	var picked []string
	for _, o := range options {
		if selected[o.value] {
			picked = append(picked, optionLabel(o))
		}
	}
	if len(picked) > 0 {
		text = strings.Join(picked, tr("、"))
	}
	width, _ := c.Size()
	maxWidth := float32(200)
	if width < 1300 {
		maxWidth = 140
	}
	trigger := ui.ButtonBase(c).MaxWidth(maxWidth).Height(style.HeightButton).Padding(0, 10).Gap(6).
		Radius(style.RadiusSM).Border(1, p.bd).Background(p.bg).Role(ui.RoleMenuButton).Label(tr(label)).
		Expanded(*open).Justify(ui.Start)
	hoverMotion(trigger, p.bg, p.hover)
	arrow := trigger.Animate("choice-arrow", 180*motionTarget(*open), selectMotion)
	trigger.Children(func() {
		if icon != "" {
			d1Icon(c, icon, 16, p.mu)
		}
		ui.Text(c, text).Grow(1).Shrink(1).Basis(0).FontSize(style.TextBase).TextColor(p.fg).
			SingleLine().Tooltip(text)
		d1Icon(c, "chev", 16, p.mu).Shrink(0).Rotate(arrow)
	})
	trigger.OnClick(func() { *open = !*open }).OnShortcut(0, ui.KeyDown, func() { *open = true })
	ui.PopoverBase(c, trigger, open, func(panel ui.Element) {
		d1DropdownPanel(panel, p).Width(trigger.Bounds().W).MaxHeight(260).
			AttachTo(trigger, ui.AnchorBottomStart, ui.AnchorTopStart).
			Role(ui.RoleMenu).Label(tr(label)).FocusGroup(ui.Vertical).Children(func() {
			ui.Scroll(c).MaxHeight(248).FillWidth().Children(func() {
				for _, o := range options {
					on := selected[o.value]
					item := d1MenuRow(c, p, o.label, true, o.value).Label(optionLabel(o))
					item.Children(func() {
						d1CheckMark(c, p, on)
						if o.icon != "" {
							d1Icon(c, o.icon, 16, p.mu)
						}
						ui.Text(c, optionLabel(o)).Grow(1).Shrink(1).Basis(0).
							FontSize(style.TextBase).TextColor(p.fg).SingleLine()
					})
					item.OnClick(func() {
						if on {
							delete(selected, o.value)
						} else {
							selected[o.value] = true
						}
					})
				}
			})
		})
	})
	return trigger
}

func d1Switch(c *ui.Context, p *d1Palette, label, icon string, on *bool) ui.Element {
	c.Key(label)
	toggle := ui.SwitchBase(c, on).FillWidth().Gap(8).AlignItems(ui.Center).Radius(6).Label(tr(label))
	left, track, thumb := switchMotion(toggle, p, *on)
	toggle.Children(func() {
		if icon != "" {
			d1Icon(c, icon, 16, p.mu)
		}
		ui.Text(c, tr(label)).Grow(1).Shrink(1).Basis(0).FontSize(style.TextSM).TextColor(p.fg)
		ui.Box(c).Size(28, 16).Shrink(0).Radius(8).Background(track).Children(func() {
			ui.Box(c).Absolute().Left(left).Top(2).Size(12, 12).Radius(6).Background(thumb)
		})
	})
	return toggle
}

func d1Input(c *ui.Context, p *d1Palette, label, key string, value *string, password bool) {
	ui.Column(c).FillWidth().Shrink(1).Gap(5).Children(func() {
		ui.Text(c, tr(label)).FontSize(style.TextSM).TextColor(p.mu)
		c.Key(key)
		input := ui.TextInputBase(c, value).FillWidth().Height(30).Padding(0, 10).Radius(6).
			Border(1, p.bd2).Background(p.bg).TextColor(p.fg).FontSize(style.TextSM).Label(tr(label))
		if password {
			input.Password()
		}
		if input.Focused() {
			input.BorderColor(p.pri)
		}
	})
}

// d1PrimaryButton 是普通实心主按钮：主色面 + 主色上文字，悬停/按下沿用
// 中性色阶过渡。enabled 为假时内容弱化并禁用交互。
func d1PrimaryButton(c *ui.Context, p *d1Palette, label, icon string, enabled ...bool) ui.Element {
	c.Key(label)
	ok := len(enabled) == 0 || enabled[0]
	b := ui.ButtonBase(c).Height(style.HeightButton).Padding(0, 10).Gap(6).Radius(style.RadiusSM).Border(1, p.pri).Label(tr(label)).Disabled(!ok)
	colors := style.NewAccentRules(p.dark, p.accent).Colors
	icoCol, textCol := p.onpri, p.onpri
	if !ok {
		b.Background(p.hover).BorderColor(p.bd)
		icoCol, textCol = p.mu, p.mu
	} else {
		amount := b.Animate("primary-hover", motionTarget(b.Hovered() || b.Pressed()), controlMotion)
		b.Background(p.pri.Mix(colors.PrimaryHover, amount))
		if b.Pressed() {
			b.Background(colors.PrimaryPressed)
		}
	}
	b.Children(func() {
		if icon != "" {
			d1Icon(c, icon, 16, icoCol)
		}
		ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(textCol)
	})
	return b
}

func d1Radio(c *ui.Context, p *d1Palette, value *int, option int, label string) {
	c.Key(label)
	b := ui.RadioBase(c, value, option).Height(28).Padding(0, 10).Radius(6).Border(1, p.bd).Label(tr(label))
	if *value == option {
		b.Background(p.sel)
	} else if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() { ui.Text(c, tr(label)).FontSize(style.TextBase).TextColor(p.fg) })
}

func (pg *mainPage) emptyTitle() string {
	if len(pg.allRows()) > 0 {
		return tr("没有符合条件的任务")
	}
	return tr("还没有下载任务")
}
func (pg *mainPage) emptyHint() string {
	if len(pg.allRows()) > 0 {
		return tr("可清除筛选、修改搜索或切换左侧视图")
	}
	return tr("点「添加 URL」粘贴地址，或从浏览器扩展嗅探发送")
}

func (pg *mainPage) limitLabel() string {
	if !pg.limitOn {
		return tr("无限制")
	}
	return tr("限速 ") + pg.limitValue + " MB/s"
}
func (pg *mainPage) setLimit(value string) {
	pg.setLimitValue(value, true)
}

// Settings edits are applied on every change and do not need repeated toasts.
func (pg *mainPage) setLimitValue(value string, notify bool) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 10240 {
		return
	}
	pg.limitOn = v > 0
	if v > 0 {
		pg.limitValue = strconv.FormatFloat(v, 'f', -1, 64)
	}
	if pg.mgr != nil {
		pg.mgr.SetRateLimit(int64(v * float64(1<<20)))
	}
	pg.saveAppPreferences()
	if notify {
		pg.flash(pg.limitLabel())
	}
}

// limitStatusControl 是状态栏左侧的限速入口：默认显示“无限制”，点击切换档位或自定义。
func (pg *mainPage) limitStatusControl(c *ui.Context, p *d1Palette) {
	c.Key("limit-status")
	trigger := ui.ButtonBase(c).Height(24).Padding(0, 6).Gap(6).Radius(4).Shrink(0).Cursor(ui.CursorPointer).
		Role(ui.RoleMenuButton).Label(tr("速度限制")).Expanded(pg.showLimitMenu)
	hoverMotion(trigger, ui.Transparent, p.hover)
	arrow := trigger.Animate("limit-arrow", 180*motionTarget(pg.showLimitMenu), selectMotion)
	color := p.mu
	if trigger.Hovered() || trigger.Pressed() || trigger.FocusVisible() {
		color = p.fg
	}
	trigger.Children(func() {
		d1Icon(c, "gauge", 14, color)
		ui.Text(c, tr(pg.limitLabel())).FontSize(style.TextSM).TextColor(color).SingleLine()
		d1Icon(c, "chev", 12, color).Rotate(arrow)
	})
	trigger.OnClick(func() { pg.showLimitMenu = !pg.showLimitMenu }).
		OnShortcut(0, ui.KeyDown, func() { pg.showLimitMenu = true })
	ui.PopoverBase(c, trigger, &pg.showLimitMenu, func(panel ui.Element) {
		d1DropdownPanel(panel, p).MinWidth(150).AttachTo(trigger, ui.AnchorTopStart, ui.AnchorBottomStart).
			Role(ui.RoleMenu).Label(tr("速度限制")).FocusGroup(ui.Vertical).Children(func() {
			d1DropdownItem(c, p, "无限制", "gauge", true).Role(ui.RoleMenuItemRadio).Checked(!pg.limitOn).AutoFocus().OnClick(func() { pg.showLimitMenu = false; pg.setLimit("0") })
			for _, value := range []string{"1", "5", "10", "20"} {
				d1DropdownItem(c, p, value+" MB/s", "", true).Role(ui.RoleMenuItemRadio).Checked(pg.limitOn && pg.limitValue == value).OnClick(func() { pg.showLimitMenu = false; pg.setLimit(value) })
			}
			d1MenuSep(c, p)
			d1DropdownItem(c, p, "自定义速度…", "cog", true).OnClick(func() {
				pg.showLimitMenu = false
				pg.limitDraft = pg.limitValue
				pg.limitError = ""
				pg.showLimit = true
			})
		})
	})
	trigger.Tooltip(tr("速度限制：") + pg.limitLabel())
}
func (pg *mainPage) limitDialog(c *ui.Context, p *d1Palette) {
	ui.DialogBase(c, &pg.showLimit, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, .42))
		panel.Width(360).Padding(18).Radius(8).Border(1, p.bd2).Background(p.bg).Label(tr("自定义限速"))
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, tr("自定义限速")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
			d1Input(c, p, "速度上限（MB/s）", "limit-value", &pg.limitDraft, false)
			if pg.limitError != "" {
				ui.Text(c, tr(pg.limitError)).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				d1Button(c, p, "取消", "").OnClick(func() { pg.showLimit = false })
				d1PrimaryButton(c, p, "应用", "").OnClick(func() {
					v, e := strconv.ParseFloat(strings.TrimSpace(pg.limitDraft), 64)
					if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 10240 {
						pg.limitError = tr("请输入大于 0 且不超过 10240 的速度")
						return
					}
					pg.setLimit(strconv.FormatFloat(v, 'f', -1, 64))
					pg.showLimit = false
				})
			})
		})
	})
}

// applyTrafficQuota 应用流量配额设置并持久化；周期为空表示不限制。
func (pg *mainPage) applyTrafficQuota(period string, mb int) error {
	if period == "" {
		mb = 0
	} else if mb < 1 || mb > 4194304 {
		return fmt.Errorf(tr("额度需要在 1–4194304 MB 之间"))
	}
	if pg.mgr == nil {
		return fmt.Errorf(tr("下载引擎尚未就绪"))
	}
	if err := pg.mgr.SetTrafficQuota(period, int64(mb)<<20); err != nil {
		return err
	}
	pg.preferences.QuotaPeriod, pg.preferences.QuotaMB = period, mb
	pg.saveAppPreferences()
	return nil
}

func (pg *mainPage) parallelism() int {
	if pg.mgr != nil {
		return pg.mgr.Concurrency()
	}
	return 3
}

func (pg *mainPage) setChecked(id string, on bool) {
	if on {
		pg.checked[id] = true
	} else {
		delete(pg.checked, id)
	}
}

// Match selection()'s active-row fallback until an explicit checkbox set exists.
func (pg *mainPage) rowChecked(id string) bool {
	for _, on := range pg.checked {
		if on {
			return pg.checked[id]
		}
	}
	return pg.sel == id
}

func (pg *mainPage) materializeSelection() {
	for _, on := range pg.checked {
		if on {
			return
		}
	}
	if pg.sel != "" {
		pg.checked[pg.sel] = true
	}
}

func (pg *mainPage) selectTableRow(id string, mods ui.Modifiers) {
	if mods&ui.Shift != 0 {
		// Shift 从锚点到目标行按当前可见顺序整段选择；Ctrl+Shift 追加到既有勾选。
		pg.selectRowRange(id, mods&ui.Ctrl != 0)
		pg.sel = id
		return
	}
	if mods&ui.Ctrl != 0 {
		pg.toggleTableCheckbox(id)
		return
	}
	if pg.sel == id && pg.rowChecked(id) && len(pg.checked) <= 1 {
		pg.clearTableSelection()
		pg.hideDetails()
		return
	}
	clear(pg.checked)
	pg.setChecked(id, true)
	pg.sel = id
	pg.selAnchor = id
	pg.detailPlacement = "right"
	pg.showDetails()
}

// selectRowRange 从锚点行到目标行整段选择（含两端，按可见顺序）；
// add 为真时保留既有勾选。锚点或目标不在可见列表时退化为单选。
func (pg *mainPage) selectRowRange(id string, add bool) {
	rows := pg.visibleRows()
	from, to := -1, -1
	for i, r := range rows {
		if r.id == pg.selAnchor {
			from = i
		}
		if r.id == id {
			to = i
		}
	}
	if !add {
		clear(pg.checked)
	}
	if from < 0 || to < 0 {
		pg.setChecked(id, true)
		return
	}
	if from > to {
		from, to = to, from
	}
	for i := from; i <= to; i++ {
		pg.setChecked(rows[i].id, true)
	}
}

func (pg *mainPage) toggleTableCheckbox(id string) {
	checked := pg.rowChecked(id)
	pg.materializeSelection()
	pg.setChecked(id, !checked)
	if !checked {
		pg.sel = id
	} else if pg.sel == id || len(pg.checked) == 0 {
		pg.sel = ""
	}
	pg.selAnchor = id
	if pg.sel == "" {
		pg.hideDetails()
	} else {
		pg.detailPlacement = "right"
		pg.showDetails()
	}
}

func (pg *mainPage) clearTableSelection() {
	clear(pg.checked)
	pg.sel = ""
}

func (pg *mainPage) selectVisibleRows(rows []d1Row, allOn bool) {
	for _, r := range rows {
		pg.setChecked(r.id, !allOn)
	}
	if allOn {
		// Do not let the active-row fallback revive a cleared selection.
		if !pg.checked[pg.sel] {
			pg.sel = ""
		}
	} else if len(rows) > 0 && !pg.checked[pg.sel] {
		pg.sel = rows[0].id
	}
	if !allOn && len(rows) > 0 {
		// 全选后以首行作为 Shift 连续选择的锚点。
		pg.selAnchor = rows[0].id
	}
}

func (pg *mainPage) selectionCounts(rows []d1Row) (visible, hidden int) {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.id] = true
		if pg.checked[r.id] {
			visible++
		}
	}
	valid := map[string]bool{}
	for _, r := range pg.allRows() {
		valid[r.id] = true
		if pg.checked[r.id] && !seen[r.id] {
			hidden++
		}
	}
	for id, on := range pg.checked {
		if !on || !valid[id] {
			delete(pg.checked, id)
		}
	}
	if visible+hidden == 0 {
		for _, r := range rows {
			if r.id == pg.sel {
				visible = 1
				break
			}
		}
	}
	return
}

// customFilterKey 是类型筛选中自定义分类的键。
func customFilterKey(idx int) string { return "cat:" + strconv.Itoa(idx) }

// matchTypeFilters 报告行是否命中任一勾选的类型；自定义分类按扩展名匹配。
func (pg *mainPage) matchTypeFilters(r d1Row) bool {
	if pg.typeFilters[r.rowKind()] {
		return true
	}
	for i, spec := range activeCustomCategories {
		if pg.typeFilters[customFilterKey(i)] && customCategoryMatch(spec, r.rowName()) {
			return true
		}
	}
	return false
}

func (pg *mainPage) matchesFilters(r d1Row) bool {
	if len(pg.typeFilters) > 0 && !pg.matchTypeFilters(r) {
		return false
	}
	if pg.dateFilter == "" {
		return true
	}
	created := r.snap.Created
	if r.pending && r.snap.ID == "" {
		created = pg.pendingCreated(r.id)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch pg.dateFilter {
	case "week":
		start = start.AddDate(0, 0, -6)
	case "month":
		start = start.AddDate(0, 0, -29)
	}
	return !created.Before(start)
}

func taskTypeOptions() []d1Option {
	options := []d1Option{
		{value: "film", label: "视频", icon: "film"},
		{value: "music", label: "音乐", icon: "music"},
		{value: "zip", label: "压缩包", icon: "zip"},
		{value: "box", label: "程序", icon: "box"},
		{value: "file", label: "文档", icon: "file"},
	}
	for i, spec := range activeCustomCategories {
		options = append(options, d1Option{value: customFilterKey(i), label: spec.Label, icon: "file"})
	}
	return options
}

func taskDateOptions() []d1Option {
	return []d1Option{
		{value: "today", label: "今天"},
		{value: "week", label: "最近 7 天"},
		{value: "month", label: "最近 30 天"},
	}
}

// d1ClearButton keeps the reset action visually secondary to task commands.
func d1ClearButton(c *ui.Context, p *d1Palette, label string) ui.Element {
	c.Key(label)
	e := ui.ButtonBase(c).Height(style.HeightButton).Padding(0, 8).Gap(4).Radius(style.RadiusSM).
		Role(ui.RoleButton).Label(tr(label)).Justify(ui.Start).Cursor(ui.CursorPointer)
	hoverMotion(e, ui.Transparent, p.hover)
	color := p.mu
	if e.Hovered() || e.Pressed() || e.FocusVisible() {
		color = p.fg
	}
	e.Children(func() {
		d1Icon(c, "x", 14, color)
		ui.Text(c, tr(label)).FontSize(style.TextBase).FontWeight(style.WeightRegular).TextColor(color).SingleLine()
	})
	return e
}

func (pg *mainPage) filterControls(c *ui.Context, p *d1Palette) {
	d1Choice(c, p, "日期", &pg.dateFilter, taskDateOptions())
	if len(pg.typeFilters) > 0 || pg.dateFilter != "" {
		d1ClearButton(c, p, "清除筛选").OnClick(func() {
			clear(pg.typeFilters)
			pg.dateFilter = ""
		})
	}
	ui.Box(c).Grow(1).Shrink(1).Basis(0)
}

// Narrow task areas expose the same filters directly in the application menu.
func (pg *mainPage) compactFilterMenu(c *ui.Context, p *d1Palette) {
	ui.Scroll(c).MaxHeight(360).Children(func() {
		ui.Column(c).FillWidth().Children(func() {
			ui.Text(c, tr("类型")).Padding(6, 8).FontSize(style.TextXS).TextColor(p.mu)
			for _, option := range taskTypeOptions() {
				on := pg.typeFilters[option.value]
				item := d1MenuRow(c, p, option.label, true, "type:"+option.value).Label(optionLabel(option)).Role(ui.RoleMenuItemCheckBox).Checked(on)
				item.Children(func() {
					d1CheckMark(c, p, on)
					d1Icon(c, option.icon, 16, p.mu)
					ui.Text(c, optionLabel(option)).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
				})
				value := option.value
				item.OnClick(func() {
					if pg.typeFilters[value] {
						delete(pg.typeFilters, value)
					} else {
						pg.typeFilters[value] = true
					}
				})
			}
			d1MenuSep(c, p)
			ui.Text(c, tr("日期")).Padding(6, 8).FontSize(style.TextXS).TextColor(p.mu)
			dates := append([]d1Option{{value: "", label: "不限日期"}}, taskDateOptions()...)
			for _, option := range dates {
				on := pg.dateFilter == option.value
				item := d1MenuRow(c, p, option.label, true).Role(ui.RoleMenuItemRadio).Checked(on)
				item.Children(func() {
					d1CheckMark(c, p, on)
					ui.Text(c, tr(option.label)).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
				})
				value := option.value
				item.OnClick(func() { pg.dateFilter = value })
			}
			if len(pg.typeFilters) > 0 || pg.dateFilter != "" {
				d1MenuSep(c, p)
				d1ClearButton(c, p, "清除筛选").FillWidth().OnClick(func() { clear(pg.typeFilters); pg.dateFilter = "" })
			}
		})
	})
}

func (pg *mainPage) canResume() bool {
	for _, r := range pg.selection() {
		if r.pending || r.snap.Held || r.snap.State == download.Canceled || r.snap.State == download.ErrorS {
			return true
		}
	}
	return false
}
func (pg *mainPage) canPause() bool {
	for _, r := range pg.selection() {
		if !r.pending && !r.snap.Held && (r.snap.State == download.Running || r.snap.State == download.Queued) {
			return true
		}
	}
	return false
}
func (pg *mainPage) hasActive() bool {
	for _, r := range pg.allRows() {
		if !r.pending && !r.snap.Held && (r.snap.State == download.Running || r.snap.State == download.Queued) {
			return true
		}
	}
	return false
}

func (pg *mainPage) taskShortcuts(c *ui.Context) {
	pg.taskShortcut(c, "delete", pg.deleteSelection)
	pg.taskShortcut(c, "select-all", func() {
		pg.selectVisibleRows(pg.visibleRows(), false)
	})
	if pg.tableHandle.Focused(c) {
		pg.taskShortcut(c, "toggle", func() {
			if r, ok := pg.selectedRow(); ok {
				pg.toggleRow(r)
			}
		})
	}
	c.OnShortcut(ui.Alt, ui.KeyEnter, func() { pg.tab = 0; pg.showDetails() })
	pg.taskShortcut(c, "open-folder", func() {
		if selected := pg.selection(); len(selected) == 1 {
			pg.openRowFolder(selected[0])
		}
	})
	pg.taskShortcut(c, "copy-url", func() {
		if selected := pg.selection(); len(selected) == 1 {
			pg.copyDownloadURL(selected[0].rowURL())
		}
	})
	c.OnShortcut(ui.Shift, ui.KeyF10, func() {
		if r, ok := pg.selectedRow(); ok {
			bounds := pg.tableHandle.Bounds(c)
			pg.menuRow = r.id
			pg.menuX = bounds.X + 48
			pg.menuY = bounds.Y + 32
			pg.showMenu = true
		}
	})
}

func (pg *mainPage) primaryRowAction(r d1Row) (icon, label string) {
	if r.pending || r.snap.Held {
		return "play", "继续"
	}
	switch r.snap.State {
	case download.Running, download.Queued:
		return "pause", "暂停"
	case download.Done:
		return "file", "打开文件"
	}
	return "play", "继续"
}

// Stable pending IDs do not depend on list indexes after deletion or transfer.
// Call while nightMu is held; the UI thread owns creation timestamps.
func (pg *mainPage) ensureNightIDs() {
	if pg.nightCreated == nil {
		pg.nightCreated = map[string]time.Time{}
	}
	for len(pg.nightIDs) < len(pg.night) {
		pg.nextNightID++
		id := fmt.Sprintf("pending-%d", pg.nextNightID)
		pg.nightIDs = append(pg.nightIDs, id)
		pg.nightCreated[id] = time.Now()
	}
}
func (pg *mainPage) pendingCreated(id string) time.Time {
	pg.nightMu.Lock()
	defer pg.nightMu.Unlock()
	return pg.nightCreated[id]
}
func (pg *mainPage) enqueueNight(req download.Request) string {
	if pg.mgr != nil {
		return pg.mgr.AddNight(req)
	}
	pg.nightMu.Lock()
	defer pg.nightMu.Unlock()
	pg.night = append(pg.night, req)
	pg.ensureNightIDs()
	return pg.nightIDs[len(pg.nightIDs)-1]
}

func (pg *mainPage) takeNight(id string) (download.Request, bool) {
	pg.nightMu.Lock()
	defer pg.nightMu.Unlock()
	pg.ensureNightIDs()
	for i, key := range pg.nightIDs {
		if key == id {
			req := pg.night[i]
			pg.night = append(pg.night[:i], pg.night[i+1:]...)
			pg.nightIDs = append(pg.nightIDs[:i], pg.nightIDs[i+1:]...)
			delete(pg.nightCreated, id)
			return req, true
		}
	}
	return download.Request{}, false
}

// moveRowToQueue 把任务移入指定的自定义队列。
func (pg *mainPage) moveRowToQueue(r d1Row, queue string) {
	if pg.mgr == nil {
		return
	}
	if pg.mgr.MoveToQueue(r.id, queue) {
		pg.flash(tr("已移至") + customQueueName(queue))
	}
}

func (pg *mainPage) moveQueue(r d1Row) {
	if pg.mgr == nil {
		return
	}
	if r.pending {
		if r.snap.ID != "" {
			if pg.mgr.MoveToMain(r.id, false) {
				pg.flash(tr("已移至主队列，等待开始"))
			} else {
				pg.flash(tr("任务正在暂停，请稍后重试"))
			}
			return
		}
		if req, ok := pg.takeNight(r.id); ok {
			id := pg.mgr.AddPaused(req)
			pg.sel = id
			delete(pg.checked, r.id)
			pg.flash(tr("已移至主队列，等待开始"))
		}
		return
	}
	if pg.mgr.MoveToNight(r.id) {
		pg.flash(tr("已移至夜间队列"))
	}
}

// canMoveSelection 报告所选任务里是否有可移入指定队列的任务。
func (pg *mainPage) canMoveSelection(queue string) bool {
	for _, r := range pg.selection() {
		if queue == "main" {
			if r.pending {
				return true
			}
			continue
		}
		if !r.pending && r.snap.State != download.Done {
			return true
		}
	}
	return false
}

// canRedownloadSelection 报告所选任务里是否有可重新下载的任务。
func (pg *mainPage) canRedownloadSelection() bool {
	return len(pg.selection()) > 0
}

// moveSelectionToQueue 批量迁移所选任务：main 移至主队列（等待）、night 移至
// 夜间队列、其余为自定义队列 ID。只处理可迁移的任务，结果合并为一条提示。
func (pg *mainPage) moveSelectionToQueue(queue string) {
	if pg.mgr == nil {
		return
	}
	moved := 0
	for _, r := range pg.selection() {
		switch queue {
		case "night":
			if !r.pending && r.snap.State != download.Done && pg.mgr.MoveToNight(r.id) {
				moved++
			}
		case "main":
			if r.pending {
				if r.snap.ID != "" {
					if pg.mgr.MoveToMain(r.id, false) {
						moved++
					}
				} else if req, ok := pg.takeNight(r.id); ok {
					pg.mgr.AddPaused(req)
					moved++
				}
			}
		default:
			if !r.pending && r.snap.State != download.Done && r.snap.Queue != queue && pg.mgr.MoveToQueue(r.id, queue) {
				moved++
			}
		}
	}
	if moved == 0 {
		pg.flash(tr("没有可移动的任务"))
		return
	}
	label := tr("夜间队列")
	switch queue {
	case "main":
		label = tr("主队列（等待）")
	default:
		if queue != "night" {
			label = customQueueName(queue)
		}
	}
	pg.flash(fmt.Sprintf(tr("已移动 %d 个任务到 %s"), moved, label))
}

func (pg *mainPage) openAdd(extract bool) {
	if !pg.showAdd {
		pg.dlgKeepExtension = true
		pg.dlgRememberDir = false
		pg.dlgDir = pg.saveDir
		if pg.defaultDirPending {
			pg.dlgDir = pg.saveDir
			pg.defaultDirPending = false
		}
		if pg.defaultConnsPending {
			pg.dlgSeg = connectionIndex(pg.preferences.Connections)
			pg.defaultConnsPending = false
		}
	}
	pg.showAdd = true
	pg.dlgExtract = extract
	pg.dlgError = ""
	pg.extractJob++
	pg.extracting = false
	pg.focus = "d1links"
	if pg.dlgDir == "" {
		pg.dlgDir = pg.saveDir
	}
	if pg.dlgGrabDepth == "" {
		pg.dlgGrabDepth = "0"
	}
	if extract {
		pg.grabProjectCache = loadGrabProjects()
		if _, ok := findGrabProject(pg.grabProjectCache, pg.dlgGrabProject); !ok {
			pg.dlgGrabProject = ""
		}
	}
	// 按链接后缀预选媒体下载方式；用户改选后即以选择为准。
	pg.dlgMedia = 0
	if links := d1ParseLinks(pg.dlgLinks); len(links) > 0 {
		allSuffix := func(suffix string) bool {
			for _, link := range links {
				if !strings.HasSuffix(strings.ToLower(strings.Split(link, "?")[0]), suffix) {
					return false
				}
			}
			return true
		}
		if allSuffix(".m3u8") {
			pg.dlgMedia = 1
		} else if allSuffix(".mpd") {
			pg.dlgMedia = 2
		}
	}
	pg.openAddWindow()
}
func (pg *mainPage) openSchedule() {
	pg.showSched = true
	pg.dlgScheduleAt = pg.nightAt
	pg.dlgScheduleStop = pg.nightStop
	pg.dlgScheduleOn = pg.nightOn
	for i := range pg.dlgScheduleDays {
		pg.dlgScheduleDays[i] = len(pg.nightDays) == 0 || slices.Contains(pg.nightDays, i)
	}
	pg.scheduleError = ""
}

// chooseDownloadDir 弹出系统文件夹选择器，替换添加下载对话框的保存路径。
func (pg *mainPage) chooseDownloadDir() {
	owner := pg.win
	if pg.addWindow != nil {
		owner = pg.addWindow
	}
	path, cancel := chooseFolder(owner, tr("选择保存文件夹"), pg.dlgDir)
	if cancel {
		return
	}
	pg.dlgDir = path
	if owner != nil {
		owner.Invalidate()
	}
}

func (pg *mainPage) submitDownloads(start bool) {
	links := d1ParseLinks(pg.dlgLinks)
	pg.dlgError = ""
	if pg.mgr == nil {
		pg.dlgError = tr("下载引擎尚未就绪")
		return
	}
	if len(links) == 0 {
		pg.dlgError = tr("请至少输入一个有效的下载链接（HTTP、HTTPS、FTP 或 FTPS）")
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(pg.dlgLinks), "\n") {
		if strings.TrimSpace(line) != "" && len(d1ParseLinks(line)) == 0 {
			pg.dlgError = tr("存在无效链接，请检查后重试")
			return
		}
	}
	if pg.dlgName != "" {
		if len(links) != 1 {
			pg.dlgError = tr("自定义文件名仅适用于单个链接")
			return
		}
		if pg.dlgName != pathrec.SanitizeFilename(pg.dlgName) {
			pg.dlgError = tr("文件名无效，请检查非法字符及首尾空格")
			return
		}
	}
	if !filepath.IsAbs(strings.TrimSpace(pg.dlgDir)) {
		pg.dlgError = tr("保存目录需要填写完整的绝对路径")
		return
	}
	if pg.dlgPage != "" && len(d1ParseLinks(pg.dlgPage)) != 1 {
		pg.dlgError = tr("来源页需要填写有效的 HTTP 或 HTTPS 地址")
		return
	}
	if pg.dlgLogin && strings.TrimSpace(pg.dlgUser) == "" {
		pg.dlgError = tr("请填写登录用户名")
		return
	}
	if pg.dlgVerifySHA {
		if len(links) != 1 {
			pg.dlgError = tr("SHA-256 校验仅适用于单个链接")
			return
		}
		if value, err := download.NormalizeSHA256(pg.dlgSHA256); err != nil || value == "" {
			pg.dlgError = tr("请输入 64 位十六进制 SHA-256 值")
			return
		}
	}
	pg.dlgNow = start
	pg.addLinks(links, pg.queueID())
	if pg.dlgRememberDir {
		pg.setDefaultDirectory(pg.dlgDir)
	}
	pg.dlgLinks = ""
	pg.dlgPassword = ""
	pg.dlgSHA256, pg.dlgVerifySHA = "", false
	pg.dlgError = ""
	pg.extractJob++
	pg.closeAddWindow()
	if start && !pg.queueIsNight() {
		pg.showMainWindow()
	}
}

func (pg *mainPage) downloadRequest(link string) download.Request {
	req := download.Request{URL: link, Directory: strings.TrimSpace(pg.dlgDir), PageURL: strings.TrimSpace(pg.dlgPage), Connections: connectionOptions[pg.dlgSeg], Headers: map[string]string{}, OpenFolder: pg.dlgOpen}
	if pg.browserDraft != nil {
		// Keep the origin when the user edits the link in a browser prompt.
		// Credentials and page metadata below remain bound to the original URL.
		req.BrowserOrigin = pg.browserDraft.BrowserOrigin
	}
	if draft := pg.browserDraft; draft != nil && link == draft.URL {
		req.Title = draft.Title
		for key, value := range draft.Headers {
			req.Headers[key] = value
		}
		if pg.dlgMedia != 0 {
			req.AudioLanguage = draft.AudioLanguage
		}
	}
	req.Name = pg.dlgName
	if pg.dlgName != "" && pg.dlgName == pg.dlgSuggestedName {
		req.Name = ""
	}
	req.KeepExtension = pg.dlgKeepExtension
	// 按来源页保留目录结构：镜像子目录优先于分类归档。
	if pg.dlgMirror && strings.TrimSpace(pg.dlgPage) != "" {
		if sub := mirrorSubdir(pg.dlgPage, link); sub != "" {
			req.Directory = filepath.Join(req.Directory, sub)
		}
	}
	req.MediaMode = mediaModeIDs()[pg.dlgMedia]
	if pg.dlgMedia != 0 {
		req.MediaQuality = mediaQualityIDs()[pg.dlgQuality]
	}
	req.RequireSignature = pg.dlgVerifySignature
	if pg.dlgVerifySHA {
		req.SHA256, _ = download.NormalizeSHA256(pg.dlgSHA256)
	}
	if req.PageURL != "" {
		req.Headers["Referer"] = req.PageURL
	}
	if pg.dlgLogin {
		req.Headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(pg.dlgUser+":"+pg.dlgPassword))
	}
	return req
}

func rangeLabel(r d1Row) string {
	if (r.pending && r.snap.ID == "") || !r.snap.RangeKnown {
		return tr("待探测")
	}
	if r.snap.SupportsRanges {
		if r.snap.ResumeSafe {
			return tr("服务器支持安全续传")
		}
		return tr("支持 Range，当前任务不可安全续传")
	}
	return tr("服务器未声明支持")
}
func requestPage(r d1Row) string {
	page := r.snap.PageURL
	if r.pending {
		page = r.req.PageURL
	}
	if page == "" {
		return tr("未提供")
	}
	return page
}

// grabFetch 为抓取与提取的页面请求附加站点登录表中的认证。
func (pg *mainPage) grabFetch(req *http.Request) (*http.Response, error) {
	if pg.mgr == nil {
		return http.DefaultClient.Do(req)
	}
	if req.Header.Get("Authorization") == "" {
		if auth := pg.mgr.SiteAuthorization(req.URL); auth != "" {
			req.Header.Set("Authorization", auth)
		}
	}
	return pg.mgr.DoResourceRequest(req)
}

// Fetch only when requested. Limit size/time; never execute a page's scripts.
func (pg *mainPage) extractPage() {
	page := strings.TrimSpace(pg.dlgPage)
	if len(d1ParseLinks(page)) != 1 {
		pg.dlgError = tr("请先填写有效的来源页地址")
		return
	}
	if pg.win == nil || pg.extracting {
		return
	}
	depth, _ := strconv.Atoi(pg.dlgGrabDepth)
	types := grabTypes(pg.dlgGrabType)
	rules := loadSiteRules()
	pg.extractJob++
	job := pg.extractJob
	pageDraft, linksDraft := pg.dlgPage, pg.dlgLinks
	pg.extracting = true
	pg.dlgError = ""
	win := pg.win
	if pg.addWindow != nil {
		win = pg.addWindow
	}
	fetch := pg.grabFetch
	go func() {
		var links []string
		var err error
		truncated := false
		if depth <= 0 {
			links, err = pageLinksWithFetch(page, fetch, rules)
		} else {
			result := grabLinks(grabRequest{start: page, depth: depth, types: types, rules: rules, fetch: fetch})
			links, truncated = result.links, result.truncated
			if len(links) == 0 && result.err != nil {
				err = result.err
			}
		}
		win.Update(func() {
			if job != pg.extractJob {
				return
			}
			pg.extracting = false
			if !pg.showAdd {
				return
			}
			if pg.dlgPage != pageDraft || pg.dlgLinks != linksDraft {
				pg.dlgError = tr("来源页或链接已修改，请重新提取")
				return
			}
			if err != nil {
				pg.dlgError = err.Error()
				return
			}
			if len(links) == 0 {
				pg.dlgError = tr("页面未发现可下载链接，可手动粘贴或使用浏览器扩展嗅探")
				return
			}
			pg.dlgLinks = strings.Join(links, "\n")
			if truncated {
				pg.dlgError = tr("抓取达到页面或时间上限，仅显示部分结果")
			}
		})
	}()
}

// charset.DetermineEncoding treats a meta declaration as tentative, so
// detect it separately before applying the undeclared UTF-8 fallback.
func pageDeclaresMetaCharset(body []byte) bool {
	if len(body) > 1024 {
		body = body[:1024]
	}
	tokens := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			tag := tokens.Token()
			if tag.Data != "meta" {
				continue
			}
			var equiv, content string
			for _, attr := range tag.Attr {
				switch attr.Key {
				case "charset":
					if enc, _ := charset.Lookup(strings.TrimSpace(attr.Val)); enc != nil {
						return true
					}
				case "http-equiv":
					equiv = attr.Val
				case "content":
					content = attr.Val
				}
			}
			if strings.EqualFold(strings.TrimSpace(equiv), "content-type") {
				if _, params, err := mime.ParseMediaType(content); err == nil {
					if enc, _ := charset.Lookup(params["charset"]); enc != nil {
						return true
					}
				}
			}
		}
	}
}

func pageLinks(page string) ([]string, error) {
	return pageLinksWithFetch(page, http.DefaultClient.Do, nil)
}

// fetchPage 下载一个页面并按声明的字符集解码，返回内容与最终地址。只接受
// HTML 与纯文本文档；脚本、样式和媒体响应不是可抓取的页面。
func fetchPage(ctx context.Context, page string, fetch func(*http.Request) (*http.Response, error)) ([]byte, *url.URL, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return nil, nil, fmt.Errorf(tr("来源页地址无效"))
	}
	resp, err := fetch(request)
	if err != nil {
		return nil, nil, fmt.Errorf(tr("读取页面失败：%w"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf(tr("读取页面失败：HTTP %d"), resp.StatusCode)
	}
	mimeType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch strings.ToLower(mimeType) {
	case "", "text/html", "application/xhtml+xml", "text/plain":
	default:
		return nil, nil, fmt.Errorf(tr("不是可抓取的页面（%s）"), mimeType)
	}
	const maxPageBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf(tr("读取页面失败：%w"), err)
	}
	if len(body) > maxPageBytes {
		return nil, nil, fmt.Errorf(tr("页面超过 4 MiB，请使用浏览器扩展嗅探"))
	}
	encoding, _, certain := charset.DetermineEncoding(body, resp.Header.Get("Content-Type"))
	if certain || pageDeclaresMetaCharset(body) || !utf8.Valid(body) {
		if decoded, decodeErr := io.ReadAll(encoding.NewDecoder().Reader(bytes.NewReader(body))); decodeErr == nil {
			body = decoded
		}
	}
	return body, resp.Request.URL, nil
}

// pageLinkScan 从页面内容收集可下载文件与可继续跟进的页面链接。站点规则
// 额外识别没有扩展名的下载路径。
func pageLinkScan(body []byte, base *url.URL, rules []pathrec.SiteRule) (files, pages []string, err error) {
	tokens := html.NewTokenizer(bytes.NewReader(body))
	type candidate struct {
		value    string
		link     bool
		media    bool
		download bool
	}
	var candidates []candidate
	baseFound := false
	for {
		typ := tokens.Next()
		if typ == html.ErrorToken {
			if tokens.Err() != io.EOF {
				return nil, nil, fmt.Errorf(tr("解析页面失败：%w"), tokens.Err())
			}
			break
		}
		if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
			continue
		}
		tag := tokens.Token()
		if tag.Data == "base" {
			if !baseFound {
				for _, attr := range tag.Attr {
					if attr.Key != "href" {
						continue
					}
					baseFound = true
					if ref, err := url.Parse(strings.TrimSpace(attr.Val)); err == nil {
						resolved := base.ResolveReference(ref)
						if (resolved.Scheme == "http" || resolved.Scheme == "https") && resolved.Hostname() != "" {
							base = resolved
						}
					}
					break
				}
			}
			continue
		}
		download := false
		if tag.Data == "a" {
			for _, attr := range tag.Attr {
				if attr.Key == "download" {
					download = true
					break
				}
			}
		}
		for _, attr := range tag.Attr {
			linkAttr := attr.Key == "href" || (tag.Data == "a" && attr.Key == "data-href")
			mediaTag := tag.Data == "video" || tag.Data == "audio" || tag.Data == "source" || tag.Data == "embed" || tag.Data == "object"
			mediaAttr := attr.Key == "src" || (mediaTag && (attr.Key == "data-src" || attr.Key == "data-original")) || (tag.Data == "object" && attr.Key == "data")
			if !linkAttr && !mediaAttr {
				continue
			}
			candidates = append(candidates, candidate{value: attr.Val, link: linkAttr, media: mediaTag, download: download && linkAttr})
		}
	}
	seenFiles := map[string]bool{}
	seenPages := map[string]bool{}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.value)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		ref, e := url.Parse(value)
		if e != nil {
			continue
		}
		u := base.ResolveReference(ref)
		u.Fragment = ""
		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(u.Path))
		resolved := u.String()
		file := candidate.media || candidate.download || pageDownloadExt[ext]
		ruleMatch := false
		if !file && len(rules) > 0 {
			_, ruleMatch = pathrec.MatchRule(rules, resolved)
			file = ruleMatch
		}
		switch {
		case file:
			if ext == "" && !candidate.media && !candidate.download && !ruleMatch {
				continue
			}
			if !seenFiles[resolved] {
				seenFiles[resolved] = true
				files = append(files, resolved)
			}
			if len(files) >= 500 {
				return files, pages, nil
			}
		case candidate.link && pageFollowExt[ext]:
			if !seenPages[resolved] {
				seenPages[resolved] = true
				pages = append(pages, resolved)
			}
			if len(pages) >= 300 {
				return files, pages, nil
			}
		}
	}
	return files, pages, nil
}

// pageDownloadExt 是页面提取认定可下载的扩展名。
var pageDownloadExt = map[string]bool{
	".zip": true, ".rar": true, ".7z": true, ".exe": true, ".msi": true, ".apk": true,
	".dmg": true, ".iso": true, ".img": true, ".pdf": true, ".doc": true, ".docx": true,
	".xls": true, ".xlsx": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true,
	".wmv": true, ".flv": true, ".mkv": true, ".webm": true, ".mp3": true, ".m4a": true,
	".aac": true, ".wav": true, ".ogg": true, ".opus": true, ".flac": true, ".tar": true,
	".gz": true,
}

// pageFollowExt 是抓取时可继续跟进的页面扩展名；无扩展名的地址也跟进。
var pageFollowExt = map[string]bool{
	"": true, ".html": true, ".htm": true, ".php": true, ".asp": true, ".aspx": true,
	".jsp": true, ".shtml": true,
}

// pageLinksWithFetch 提取单个页面的可下载链接；跟进页面由站点抓取处理。
func pageLinksWithFetch(page string, fetch func(*http.Request) (*http.Response, error), rules []pathrec.SiteRule) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body, base, err := fetchPage(ctx, page, fetch)
	if err != nil {
		return nil, err
	}
	files, _, err := pageLinkScan(body, base, rules)
	if err != nil {
		return nil, err
	}
	return files, nil
}
