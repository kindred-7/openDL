package main

import (
	"time"

	"openDL/style"

	"github.com/egoist/mygo/ui"
)

func (pg *mainPage) applicationMenu(c *ui.Context, p *d1Palette) {
	c.Key("application-menu")
	button := ui.ButtonBase(c).Size(28, 28).Radius(style.RadiusSM).Center().
		Role(ui.RoleMenuButton).Label(tr("应用菜单")).Tooltip(tr("应用菜单")).Expanded(pg.showAppMenu)
	if button.Hovered() || button.FocusVisible() || pg.showAppMenu {
		button.Background(p.hover)
	}
	target := float32(0)
	if pg.showAppMenu {
		target = 1
	}
	progress := button.Animate("menu-to-close", target, 180*time.Millisecond)
	button.Children(func() {
		// Morph three Lucide minus strokes rather than crossfading two glyphs.
		// Animate respects the desktop's ReduceMotion preference.
		ui.Box(c).Size(20, 20).Children(func() {
			c.Key("top-line")
			style.Icon(c, style.IconMinus, 24, p.fg).Absolute().Left(-2).Top(-2 - 5*(1-progress)).Rotate(45 * progress)
			c.Key("middle-line")
			style.Icon(c, style.IconMinus, 24, p.fg).Absolute().Left(-2).Top(-2).Opacity(1 - progress)
			c.Key("bottom-line")
			style.Icon(c, style.IconMinus, 24, p.fg).Absolute().Left(-2).Top(-2 + 5*(1-progress)).Rotate(-45 * progress)
		})
	})
	button.OnClick(func() {
		pg.showAppMenu = !pg.showAppMenu
		pg.appMenuSection = ""
		pg.showTypeMenu, pg.showLimitMenu, pg.showMenu, pg.showDetailMenu = false, false, false, false
	}).OnShortcut(0, ui.KeyDown, func() { pg.showAppMenu = true })
	ui.PopoverBase(c, button, &pg.showAppMenu, func(panel ui.Element) {
		if panel.Dismissed() {
			pg.appMenuSection = ""
			pg.showTypeMenu = false
		}
		_, height := c.Size()
		d1DropdownPanel(panel, p).Width(240).AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).
			Role(ui.RoleMenu).Label(tr("应用菜单")).FocusGroup(ui.Vertical).Children(func() {
			ui.Scroll(c).MaxHeight(max(float32(200), height-90)).Children(func() {
				ui.Column(c).FillWidth().Children(func() {
					d1DropdownItem(c, p, "添加下载", "plus", true, shortcutKeyLabel(shortcutText(pg.preferences, "add"))).AutoFocus().OnClick(func() { pg.showAppMenu = false; pg.openAdd(false) })
					d1DropdownItem(c, p, "站点抓取", "globe", true).OnClick(func() { pg.showAppMenu = false; pg.openAdd(true) })
					d1DropdownItem(c, p, "计划任务", "clock", true).OnClick(func() { pg.showAppMenu = false; pg.openSchedule() })
					d1MenuSep(c, p)
					pg.menuGroup(c, p, "tasks", "任务", "list", func() {
						d1DropdownItem(c, p, "继续所选任务", "play", pg.canResume()).OnClick(func() { pg.showAppMenu = false; pg.resumeSelection() })
						d1DropdownItem(c, p, "暂停所选任务", "pause", pg.canPause()).OnClick(func() { pg.showAppMenu = false; pg.pauseSelection() })
						d1DropdownItem(c, p, "重新下载所选任务", "redo", pg.canRedownloadSelection()).OnClick(func() { pg.showAppMenu = false; pg.redownloadSelection() })
						d1MenuSep(c, p)
						d1DropdownItem(c, p, "开始全部并启用夜间计划", "play", pg.hasResumableTask() || !pg.nightOn).OnClick(func() { pg.showAppMenu = false; pg.startAllTasks() })
						d1DropdownItem(c, p, "暂停全部并停用夜间计划", "pause", pg.hasActive() || pg.nightOn).OnClick(func() { pg.showAppMenu = false; pg.pauseAllTasks() })
					})
					pg.menuGroup(c, p, "move-queue", "移动所选到队列", "layers", func() {
						d1DropdownItem(c, p, "移至夜间队列", "orbit", pg.canMoveSelection("night")).OnClick(func() { pg.showAppMenu = false; pg.moveSelectionToQueue("night") })
						d1DropdownItem(c, p, "移至主队列（等待）", "layers", pg.canMoveSelection("main")).OnClick(func() { pg.showAppMenu = false; pg.moveSelectionToQueue("main") })
						for _, spec := range activeCustomQueues {
							queue := spec
							d1DropdownItem(c, p, tr("移至 ")+queue.Name, "layers", pg.canMoveSelection(queue.ID)).OnClick(func() { pg.showAppMenu = false; pg.moveSelectionToQueue(queue.ID) })
						}
					})
					if pg.taskViewportWidth(c) < 800 {
						pg.menuGroup(c, p, "filters", "筛选", "list-filter", func() {
							pg.compactFilterMenu(c, p)
						})
					}
					pg.menuGroup(c, p, "queues", "队列", "layers", func() {
						d1DropdownItem(c, p, "主队列", "layers", true).OnClick(func() { pg.showAppMenu = false; pg.nav = 5 })
						d1DropdownItem(c, p, "夜间队列", "orbit", true).OnClick(func() { pg.showAppMenu = false; pg.nav = 6 })
					})
					d1MenuSep(c, p)
					pg.menuGroup(c, p, "panels", "面板", "columns-3", func() {
						d1DropdownItem(c, p, "在底部显示详情", "panel-bottom", true).OnClick(func() { pg.showAppMenu = false; pg.setDetailPlacement("bottom") })
						d1DropdownItem(c, p, "在右侧显示详情", "panel-right", true).OnClick(func() { pg.showAppMenu = false; pg.setDetailPlacement("right") })
						d1DropdownItem(c, p, "重置面板尺寸", "redo", true).OnClick(func() { pg.showAppMenu = false; pg.resetPanelLayout() })
					})
					d1DropdownItem(c, p, "设置", "cog", true).OnClick(func() { pg.showAppMenu = false; pg.openSettings() })
					d1DropdownItem(c, p, "浏览器扩展安装说明", "globe", true).OnClick(func() {
						pg.showAppMenu = false
						pg.showExtensionGuide = true
					})
					d1DropdownItem(c, p, "导入设置", "upload", true).OnClick(func() { pg.showAppMenu = false; pg.importSettings() })
					d1DropdownItem(c, p, "导出设置", "download", true).OnClick(func() { pg.showAppMenu = false; pg.exportSettings() })
					d1DropdownItem(c, p, "检查更新", "redo", true).OnClick(func() {
						pg.showAppMenu = false
						pg.openUpdateSettings()
					})
					d1DropdownItem(c, p, "关于", "info", true).OnClick(func() { pg.showAppMenu = false; pg.copyFeedbackKey = ""; pg.showAbout = true })
				})
			})
		})
	})
}
