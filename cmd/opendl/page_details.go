package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"openDL/style"
)

func (pg *mainPage) rightDetailOpen() bool {
	return pg.detailPlacement == "right" && !pg.detailCollapsed
}

func (pg *mainPage) showDetails() {
	if pg.detailPlacement != "right" {
		pg.detailPlacement = "bottom"
	}
	pg.detailCollapsed = false
	pg.refreshSettingsWindow()
}

func (pg *mainPage) hideDetails() {
	pg.detailCollapsed = true
	pg.rememberDetailPlacement()
	pg.refreshSettingsWindow()
}

func detailPlacementPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "detail-position"), nil
}

func loadDetailPlacement() string {
	path, err := detailPlacementPath()
	if err != nil {
		return "bottom"
	}
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) == "right" {
		return "right"
	}
	return "bottom"
}

func (pg *mainPage) rememberDetailPlacement() {
	// Headless views keep their state in memory without touching preferences.
	if pg.win == nil {
		return
	}
	path, err := detailPlacementPath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = os.WriteFile(path, []byte(pg.detailPlacement), 0o600)
	}
	if err != nil {
		pg.flash(tr("无法保存详情位置：") + err.Error())
	}
}

func (pg *mainPage) setDetailPlacement(position string) {
	if position != "right" {
		position = "bottom"
	}
	pg.detailPlacement = position
	pg.showDetails()
	pg.rememberDetailPlacement()
}

// d1TooltipBelow 与主题提示同貌，但显示在控件下方：详情面板右上角的控件紧贴
// 标题栏，向上的提示会与原生窗口按钮相叠。
func d1TooltipBelow(c *ui.Context, p *d1Palette, anchor ui.Element, s string) {
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.MaxWidth(320).Padding(5, 8).Radius(5).Background(p.fg).TextColor(p.bg).
			FontSize(style.TextSM).Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.2))
		tip.Children(func() { ui.Text(c, tr(s)) })
		tip.AttachTo(anchor, ui.AnchorBottom, ui.AnchorTop).Margin(6, 0, 0, 0)
	})
}

// detailPositionControl 是停靠位置分段控件：两段均为图标——当前停靠段为选中态，
// 点击收起面板（取代原关闭按钮）；另一段切换到对应停靠位置。
func (pg *mainPage) detailPositionControl(c *ui.Context, p *d1Palette) {
	currentIcon, closeTip := "panel-bottom", "收起底部详情面板"
	targetIcon, targetTip, target := "panel-right", "切换到右侧显示", "right"
	if pg.detailPlacement == "right" {
		currentIcon, closeTip = "panel-right", "收起右侧详情栏"
		targetIcon, targetTip, target = "panel-bottom", "切换到底部显示", "bottom"
	}
	// 左段切换到另一停靠位置，右段固定为收起面板（最右侧始终是收起）。
	ui.Row(c).Padding(2).Gap(2).Radius(6).Border(1, p.bd).Background(p.card).Children(func() {
		other := ui.ButtonBase(c).Height(24).Padding(0, 7).Radius(5).Center().
			Role(ui.RoleToggleButton).Label(tr(targetTip))
		hoverMotion(other, ui.Transparent, p.hover)
		other.Children(func() { d1Icon(c, targetIcon, 16, p.mu) })
		other.OnClick(func() { pg.setDetailPlacement(target) })
		d1TooltipBelow(c, p, other, targetTip)

		current := ui.ButtonBase(c).Height(24).Padding(0, 7).Radius(5).Center().
			Role(ui.RoleToggleButton).Checked(true).Label(tr(closeTip))
		current.Background(p.bg).Border(1, p.bd)
		hoverMotion(current, p.bg, p.hover)
		current.Children(func() { d1Icon(c, currentIcon, 16, p.fg) })
		current.OnClick(pg.hideDetails)
		d1TooltipBelow(c, p, current, closeTip)
	})
}

// Both docking positions share the selection, tabs and body. Only the host
// geometry changes, so switching positions cannot leave stale task details.
func (pg *mainPage) detailPanel(c *ui.Context, p *d1Palette) {
	if pg.detailCollapsed {
		return
	}
	right := pg.detailPlacement == "right"
	panel := ui.Column(c).Fill().Background(p.card).Label(tr("任务详情"))
	panel.Children(func() {
		row, hasRow := pg.selectedRow()
		if right {
			ui.Row(c).Height(38).Padding(0, 10).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr("任务详情")).Grow(1).Shrink(1).Basis(0).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg)
				pg.detailPositionControl(c, p)
			})
		}
		buildTabs := func() {
			c.Key("detail-tabs")
			tabs := ui.TabsBase(c, &pg.tab, len(d1TabLabels))
			list := tabs.List.FillWidth().AlignItems(ui.Center)
			if right {
				list.Padding(3).Gap(2).Radius(style.RadiusSM).Border(1, p.bd).Background(p.bg)
			} else {
				list.Padding(0, 10).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(p.bd)
			}
			position := float32(pg.tab)
			if right {
				segmented := segmentedSlide(list, pg.tab, len(d1TabLabels), 3, 2, style.RadiusSM, p.pri)
				position = segmented.x
			}
			list.Children(func() {
				for i, label := range d1TabLabels {
					c.Key(label)
					on := pg.tab == i
					tab := tabs.Tab(i).Padding(9, 12)
					if right {
						tab.Grow(1).Shrink(1).Basis(0).MinWidth(0).Padding(6, 2).Radius(style.RadiusSM).Center().Clip()
					}
					color := p.mu
					progress := tab.Animate("tab-selection", motionTarget(on), selectMotion)
					if right {
						weight := max(float32(0), 1-float32(math.Abs(float64(position-float32(i)))))
						color = p.mu.Mix(p.onpri, weight)
					} else if on {
						color = p.fg
					}
					if !right && progress > 0 {
						tab.Draw(func(painter *ui.Painter, r ui.Rect) {
							width := r.W * progress
							painter.Fill(ui.Rect{X: r.X + (r.W-width)/2, Y: r.Y + r.H - 2, W: width, H: 2}, p.pri.Alpha(progress), 0)
						})
					}
					tab.Children(func() {
						size := style.TextBase
						if right {
							size = style.TextSM
						}
						ui.Text(c, tr(label)).Shrink(1).MinWidth(0).FontSize(size).TextColor(color).SingleLine()
					})
				}
				if !right {
					ui.Box(c).Grow(1).Shrink(1).Basis(0)
					pg.detailPositionControl(c, p)
				}
			})
		}
		if right {
			ui.Column(c).FillWidth().Padding(0, 10, 8, 10).Children(buildTabs)
		} else {
			buildTabs()
		}
		if !hasRow && !right {
			ui.Text(c, tr("未选中任务")).Padding(14).FontSize(style.TextSM).TextColor(p.mu)
			return
		}
		scroll := ui.Scroll(c).FillWidth().Grow(1).Shrink(1).Basis(0)
		scroll.Children(func() {
			if !hasRow {
				ui.Text(c, tr("选择一个下载任务以查看详情")).Padding(14).FontSize(style.TextSM).TextColor(p.mu)
				return
			}
			switch pg.tab {
			case 0:
				pg.overviewTab(c, p, row)
			case 1:
				pg.segTab(c, p, row)
			case 2:
				pg.headersTab(c, p, row)
			default:
				pg.logTab(c, p, row)
			}
		})
		if hasRow {
			// 底部操作栏：主操作 + 打开文件夹 + 更多菜单。
			ui.Row(c).FillWidth().Padding(12, 14).Gap(8).AlignItems(ui.Center).
				BorderWidth(1, 0, 0, 0).BorderColor(p.bd).Children(func() {
				icon, label := pg.primaryRowAction(row)
				d1PrimaryButton(c, p, label, icon).OnClick(func() { pg.toggleRow(row) })
				d1Button(c, p, "打开文件夹", "folder").OnClick(func() { pg.openRowFolder(row) })
				more := d1IconButton(c, p, "dots").Expanded(pg.showDetailMenu)
				more.OnClick(func() { pg.showDetailMenu = !pg.showDetailMenu }).
					OnShortcut(0, ui.KeyDown, func() { pg.showDetailMenu = true })
				ui.PopoverBase(c, more, &pg.showDetailMenu, func(panel ui.Element) {
					d1DropdownPanel(panel, p).Width(230).AttachTo(more, ui.AnchorTopEnd, ui.AnchorBottomEnd).
						Role(ui.RoleMenu).Label(tr("更多操作")).FocusGroup(ui.Vertical).Children(func() {
						pg.taskMenuBody(c, p, row, false, func() { pg.showDetailMenu = false })
					})
				})
			})
		}
	})
}
