package main

import (
	"bytes"
	"image/png"
	"strings"
	"time"

	"openDL/download"
	"openDL/resources"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// appIconBitmap 与应用和托盘共用同一图标，进程内解码一次。
var appIconBitmap = decodeAppIcon()

func decodeAppIcon() *ui.Bitmap {
	img, err := png.Decode(bytes.NewReader(resources.IconPNG))
	if err != nil {
		return nil
	}
	return ui.NewBitmap(img)
}

func (pg *mainPage) hasCompleted() bool {
	for _, r := range pg.allRows() {
		if !r.pending && r.snap.State == download.Done {
			return true
		}
	}
	return false
}

func (pg *mainPage) clearCompleted() {
	var rows []d1Row
	for _, r := range pg.allRows() {
		if !r.pending && r.snap.State == download.Done {
			rows = append(rows, r)
		}
	}
	pg.requestDelete(rows)
}

func resumableRow(r d1Row) bool {
	return r.pending || r.snap.Held || r.snap.State == download.Canceled || r.snap.State == download.ErrorS
}

func (pg *mainPage) hasResumableTask() bool {
	for _, r := range pg.allRows() {
		if resumableRow(r) {
			return true
		}
	}
	return false
}

func (pg *mainPage) startAllTasks() {
	pg.nightOn = true
	pg.saveAppPreferences()
	for _, r := range append([]d1Row(nil), pg.allRows()...) {
		if resumableRow(r) {
			pg.toggleRow(r)
		}
	}
	pg.flash(tr("已开始全部可开始的任务"))
}

func (pg *mainPage) pauseAllTasks() {
	pg.nightOn = false
	pg.saveAppPreferences()
	pg.pauseAll()
	pg.flash(tr("已暂停全部任务"))
}

func (pg *mainPage) menuGroup(c *ui.Context, p *d1Palette, id, label, icon string, body func()) {
	open := pg.appMenuSection == id
	c.Key("menu-group:" + id)
	button := ui.ButtonBase(c).FillWidth().Padding(6, 8).Gap(8).Radius(5).Justify(ui.Start).AlignItems(ui.Center).
		Role(ui.RoleMenuItem).Label(tr(label)).Expanded(open)
	if button.Hovered() || button.FocusVisible() {
		button.Background(p.hover)
	}
	button.Children(func() {
		d1Icon(c, icon, 16, p.mu)
		ui.Text(c, tr(label)).Grow(1).Shrink(1).Basis(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
		d1Icon(c, "chevron-right", 14, p.mu)
	})
	// 指针在行上或点击/右方向键在行右侧展开二级菜单；左方向键或 Escape 收起。
	if button.Hovered() {
		pg.appMenuSection = id
	}
	button.OnClick(func() {
		if pg.appMenuSection == id {
			pg.appMenuSection = ""
		} else {
			pg.appMenuSection = id
		}
	})
	button.OnShortcut(0, ui.KeyRight, func() { pg.appMenuSection = id }).OnShortcut(0, ui.KeyLeft, func() { pg.appMenuSection = "" })
	if open {
		// 二级菜单的指针是否悬停在行上；离开后短暂宽限再收起，避免斜向移入弹层时闪断。
		hoverState := ui.Local(button, "menu-group-hover", func() bool { return true })
		ui.Overlay(c, func() {
			panel := ui.Box(c).AttachTo(button, ui.AnchorTopEnd, ui.AnchorTopStart).Margin(-4, 0, 0, 6)
			d1DropdownPanel(panel, p).Width(220).Role(ui.RoleMenu).Label(tr(label)).FocusGroup(ui.Vertical).Children(body)
			if panel.OverlayShortcut(0, ui.KeyEscape) {
				pg.appMenuSection = ""
			}
			_, _, rowOver := button.PointerPosition()
			_, _, panelOver := panel.PointerPosition()
			if rowOver || panelOver {
				*hoverState = true
			} else if *hoverState {
				// 指针刚离开行和弹层：延后一帧再判定，给斜向移入弹层留出过渡时间。
				*hoverState = false
				c.After(180 * time.Millisecond)
			} else {
				// 宽限期内仍未移回行或弹层，收起二级菜单。
				pg.appMenuSection = ""
			}
		})
	}
}

// d1AppVersion 返回应用版本号（SemVer 2.0.0，不带 v 前缀）；mygo 打包时由构建
// 工具注入，开发运行时回退默认值。更新比较与写入都用这个值。
func d1AppVersion() string {
	v := appVersionLabel(mygo.App.Version())
	if v == "" {
		return "0.1.0"
	}
	return v
}

// appVersionLabel 返回界面展示的 SemVer 版本号，形如 1.5.0-rc.1；传入值带不带
// v 前缀都归一到同一形式。
func appVersionLabel(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func (pg *mainPage) aboutDialog(c *ui.Context, p *d1Palette) {
	c.Key("about-dialog")
	ui.DialogBase(c, &pg.showAbout, func(back, panel ui.Element) {
		back.Background(p.fg.Alpha(.15)).Center()
		panel.Width(400).Radius(style.RadiusLG).Border(1, p.bd).Background(p.bg).Children(func() {
			ui.Column(c).FillWidth().Children(func() {
				ui.Row(c).FillWidth().Padding(12, 16).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("关于")+" "+appBrand).Grow(1).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg)
					d1IconButton(c, p, "x").Size(28, 28).Label(tr("关闭")).OnClick(func() { pg.showAbout = false })
				})
				ui.Box(c).FillWidth().Height(1).Background(p.bd)
				ui.Column(c).FillWidth().Padding(32, 24).Gap(16).AlignItems(ui.Center).Children(func() {
					if appIconBitmap != nil {
						ui.Image(c, appIconBitmap).Size(72, 72)
					} else {
						style.Icon(c, style.IconDownload, 48, p.fg)
					}
					ui.Text(c, appBrand).FontSize(style.TextLG).FontWeight(style.WeightMedium).TextColor(p.fg).TextAlign(ui.Center)
					ui.Text(c, tr("版本：")+appVersionLabel(d1AppVersion())).FontSize(style.TextSM).TextColor(p.mu).TextAlign(ui.Center)
					ui.Text(c, appBrand+tr(" 开源的桌面下载管理器")).FontSize(style.TextBase).TextColor(p.fg).TextAlign(ui.Center)
					ui.Text(c, tr("多连接下载、队列管理与定时任务")).FontSize(style.TextSM).TextColor(p.mu).TextAlign(ui.Center)
				})
				ui.Box(c).FillWidth().Height(1).Background(p.bd)
				ui.Row(c).FillWidth().Padding(12, 16).Justify(ui.End).Children(func() {
					d1Button(c, p, "关闭", "").OnClick(func() { pg.showAbout = false })
				})
			})
		})
	})
}
