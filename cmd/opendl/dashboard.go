package main

// The dashboard layout: top bar, icon sidebar, KPI cards, filter tabs, the
// task table, the queue speed chart and the right-hand detail panel — a 1:1
// reconstruction of the reference design in MyGo's native widgets.

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

// ── shared controls ──────────────────────────────────────────────────────

func flatButton(c *ui.Context, label string, padY, padX float32) ui.Element {
	return ui.ButtonBase(c).Padding(padY, padX).Radius(8).Children(func() {
		ui.Text(c, tr(label)).FontSize(13)
	})
}

// toolButton is a secondary button with a leading icon ("暂停", "删除"...).
func toolButton(c *ui.Context, p *style.Palette, icon, label string) ui.Element {
	return ui.ButtonBase(c).Padding(6, 10).Radius(8).Border(1, p.Border).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			style.Icon(c, icon, 14, p.TextSoft)
			ui.Text(c, tr(label)).FontSize(12)
		})
	})
}

// iconButton is an icon-only button; active items get a pressed fill.
func iconButton(c *ui.Context, p *style.Palette, icon string, active bool) ui.Element {
	btn := ui.ButtonBase(c).Padding(8).Radius(8)
	if active {
		btn = btn.Background(p.Surface2)
	}
	return btn.Children(func() {
		style.Icon(c, icon, 18, p.Text)
	})
}

// smallIconButton is the 16px variant used inside table rows.
func smallIconButton(c *ui.Context, p *style.Palette, icon string) ui.Element {
	return ui.ButtonBase(c).Padding(4).Radius(6).Children(func() {
		style.Icon(c, icon, 14, p.TextSoft)
	})
}

func primaryActionButton(c *ui.Context, p *style.Palette, label string) ui.Element {
	return ui.ButtonBase(c).Padding(8, 14).Radius(8).Background(p.Primary).
		Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				style.Icon(c, style.IconPlus, 15, p.OnPrimary)
				ui.Text(c, tr(label)).FontSize(13).Bold().TextColor(p.OnPrimary)
			})
		})
}

// card wraps a content block in the dashboard's card chrome.
func card(c *ui.Context, p *style.Palette, children func()) ui.Element {
	return ui.Column(c).Background(p.Card).Radius(12).Border(1, p.Border).
		Shadow(0, 2, 8, 0, ui.Hex("#0B1220").Alpha(0.05)).Children(children)
}

// pill is the rounded status badge with a leading dot.
func pill(c *ui.Context, bg, fg ui.Color, text string) ui.Element {
	return ui.Row(c).Padding(3, 9).Gap(6).Radius(999).Background(bg).AlignItems(ui.Center).Children(func() {
		style.Icon(c, style.IconStatus, 8, fg)
		ui.Text(c, tr(text)).FontSize(11).TextColor(fg)
	})
}

// ── top bar ──────────────────────────────────────────────────────────────

func (a *app) topBar(c *ui.Context, p *style.Palette) {
	ui.Row(c).Padding(14, 20).Gap(12).AlignItems(ui.Center).Background(p.Card).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(34, 34).Radius(10).Background(p.Primary).Center().Children(func() {
				ui.Text(c, tr("N")).FontSize(16).Bold().TextColor(p.OnPrimary)
			})
			ui.Text(c, appBrand).SingleLine().Width(132).FontSize(16).Bold()
		})
		// Search pill with the shortcut chip.
		ui.Row(c).Padding(8, 12).Gap(8).Radius(999).Background(p.Surface2).
			Width(360).AlignItems(ui.Center).Children(func() {
			style.Icon(c, style.IconSearch, 15, p.TextSoft)
			c.Key("search")
			ui.TextInput(c, &a.query).FocusBind(&a.focus, "search").Width(230).
				Placeholder(tr("搜索下载内容、网址或粘贴链接..."))
			ui.Text(c, tr("Ctrl+K")).SingleLine().Width(38).FontSize(10).TextColor(p.TextFaint)
		})
		if primaryActionButton(c, p, "新建下载").Clicked() {
			a.showAdd = !a.showAdd
			a.focus = "url"
		}
		if toolButton(c, p, style.IconLink, "粘贴链接").Clicked() {
			a.command("粘贴")
		}
		if toolButton(c, p, style.IconUpload, "导入").Clicked() {
			a.status = tr("「导入」功能开发中")
		}
		ui.Box(c).Width(1).FillHeight().Background(p.Border)
		if iconButton(c, p, style.IconSun, false).Clicked() {
			a.toggleTheme()
		}
		if iconButton(c, p, style.IconBell, false).Clicked() {
			a.status = tr("暂无新通知")
		}
		ui.Box(c).Size(30, 30).Radius(999).Background(p.Surface2).Center().Children(func() {
			ui.Text(c, tr("Y")).FontSize(12).Bold().TextColor(p.TextSoft)
		})
	})
	if a.showAdd {
		ui.Row(c).Padding(12, 20).Gap(8).AlignItems(ui.Center).Background(p.Card).
			BorderWidth(0, 0, 1, 0).BorderColor(p.Border).Children(func() {
			c.Key("url")
			ui.TextInput(c, &a.input).FocusBind(&a.focus, "url").Fill().
				Placeholder(tr("粘贴链接，例如 https://example.com/file.zip"))
			if ui.PrimaryButton(c, tr("开始下载")).Clicked() {
				a.command("添加")
				if a.status == tr("已加入队列") {
					a.showAdd = false
				}
			}
			if flatButton(c, "取消", 8, 12).Clicked() {
				a.showAdd = false
			}
		})
	}
}

// ── left sidebar ─────────────────────────────────────────────────────────

type navItem struct {
	icon  string
	label string
	count func(a *app) int
}

var navItems = []navItem{
	{style.IconHome, "仪表盘", func(*app) int { return 0 }},
	{style.IconDownload, "下载中", func(a *app) int {
		n := 0
		for _, s := range a.mgr.Snapshots() {
			if s.State == download.Running || s.State == download.Queued {
				n++
			}
		}
		return n
	}},
	{style.IconCheckC, "已完成", func(a *app) int {
		n := 0
		for _, s := range a.mgr.Snapshots() {
			if s.State == download.Done {
				n++
			}
		}
		return n
	}},
	{style.IconPauseC, "已暂停", func(a *app) int {
		n := 0
		for _, s := range a.mgr.Snapshots() {
			if s.State == download.Canceled {
				n++
			}
		}
		return n
	}},
	{style.IconClock, "计划任务", func(*app) int { return 0 }},
	{style.IconGlobe, "浏览器接管", func(*app) int { return 0 }},
	{style.IconTrash, "回收站", func(*app) int { return 0 }},
	{style.IconSliders, "设置", func(*app) int { return 0 }},
}

func (a *app) sidebar(c *ui.Context, p *style.Palette) {
	ui.Column(c).Width(216).FillHeight().Padding(16).Gap(4).Children(func() {
		for i, it := range navItems {
			i, it := i, it
			active := a.nav == i
			row := ui.ButtonBase(c).Padding(9, 12).Radius(10).FillWidth()
			if active {
				row = row.Background(style.BadgeBlueBg)
			}
			row.Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					col := p.TextSoft
					if active {
						col = style.BadgeBlueFg
					}
					style.Icon(c, it.icon, 17, col)
					label := ui.Text(c, tr(it.label)).FontSize(13).Fill()
					if active {
						label = label.Bold().TextColor(style.BadgeBlueFg)
					}
					if n := it.count(a); n > 0 {
						badge := ui.Text(c, fmt.Sprint(n)).FontSize(11)
						if active {
							badge = badge.TextColor(style.BadgeBlueFg)
						} else {
							badge = badge.TextColor(p.TextFaint)
						}
					}
				})
			})
			if row.Clicked() {
				a.nav = i
				a.sel = -1
				switch it.label {
				case "浏览器接管":
					a.status = tr("浏览器扩展已就绪")
				case "设置":
					a.status = tr("「设置」面板开发中")
				case "回收站":
					a.status = tr("回收站为空")
				case "计划任务":
					a.status = tr("「计划任务」功能开发中")
				default:
					a.status = it.label
				}
			}
		}
		ui.Row(c).Fill().Children(func() {})
		a.storageCard(c, p)
	})
}

func (a *app) storageCard(c *ui.Context, p *style.Palette) {
	free, total := diskSpace(a.saveDir)
	label := "—"
	var frac float64 = -1
	if total > 0 {
		label = fmt.Sprintf("%.0f GB / %.0f GB", float64(free)/float64(1<<30), float64(total)/float64(1<<30))
		frac = 1 - float64(free)/float64(total)
	}
	card(c, p, func() {
		ui.Row(c).Padding(12).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(32, 32).Radius(8).Background(p.Surface2).Center().Children(func() {
				style.Icon(c, style.IconDisk, 16, p.TextSoft)
			})
			ui.Column(c).Gap(4).Fill().Children(func() {
				ui.Text(c, tr("存储空间")).FontSize(12).Bold()
				ui.Text(c, tr(label)).SingleLine().FontSize(11).TextColor(p.TextFaint)
				ui.Progress(c, frac).FillWidth()
			})
			style.Icon(c, style.IconChevron, 14, p.TextFaint)
		})
	})
}

// ── KPI cards ────────────────────────────────────────────────────────────

func (a *app) kpiRow(c *ui.Context, p *style.Palette, snaps []download.Snapshot) {
	var active, done, paused int
	for _, s := range snaps {
		switch s.State {
		case download.Running, download.Queued:
			active++
		case download.Done:
			done++
		case download.Canceled:
			paused++
		}
	}
	speed := a.totalSpeed(snaps)
	peak := a.peakSpeed()

	ui.Row(c).Gap(16).Children(func() {
		kpiCard(c, p, style.IconDownload, style.BadgeBlueBg, style.BadgeBlueFg,
			"活跃下载", fmt.Sprint(active), fmt.Sprintf(tr("排队 %d"), active), style.BadgeGreenFg,
			func() { sparkBars(c, style.BadgeBlueFg, a.histValues(20)) })
		kpiCard(c, p, style.IconBolt, style.BadgeYellowBg, style.BadgeYellowFg,
			"总速度", bytesText(speed)+"/s", tr("峰值 ")+bytesText(peak)+"/s", style.BadgeGreenFg,
			func() { sparkLine(c, style.BadgeBlueFg, a.histValues(20)) })
		kpiCard(c, p, style.IconCheckC, style.BadgeGreenBg, style.BadgeGreenFg,
			"今日完成", fmt.Sprint(done), fmt.Sprintf(tr("共 %d 项"), len(snaps)), style.BadgeGreenFg,
			func() { sparkBars(c, style.BadgeGreenFg, a.histValues(20)) })
		kpiCard(c, p, style.IconPauseC, style.BadgeOrangeBg, style.BadgeOrangeFg,
			"已暂停", fmt.Sprint(paused), fmt.Sprintf(tr("出错 %d"), countErr(snaps)), style.Danger,
			func() { sparkBars(c, ui.Hex("#94A3B8"), a.histValues(20)) })
	})
}

func countErr(snaps []download.Snapshot) int {
	n := 0
	for _, s := range snaps {
		if s.State == download.ErrorS {
			n++
		}
	}
	return n
}

func kpiCard(c *ui.Context, p *style.Palette, icon string, chipBg, chipFg ui.Color,
	title, value, delta string, deltaFg ui.Color, chart func()) {
	card(c, p, func() {
		ui.Row(c).Padding(16).Gap(12).Children(func() {
			ui.Column(c).Gap(10).Fill().Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(28, 28).Radius(8).Background(chipBg).Center().Children(func() {
						style.Icon(c, icon, 15, chipFg)
					})
					ui.Text(c, tr(title)).FontSize(12).TextColor(p.TextSoft)
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr(value)).FontSize(22).Bold()
					ui.Text(c, tr(delta)).FontSize(11).TextColor(deltaFg)
				})
			})
			chart()
		})
	})
}

// sparkBars draws a tiny bar histogram.
func sparkBars(c *ui.Context, color ui.Color, values []float64) ui.Element {
	return ui.Box(c).Width(84).Height(40).Draw(func(p *ui.Painter, r ui.Rect) {
		if len(values) == 0 {
			return
		}
		maxV := 1.0
		for _, v := range values {
			if v > maxV {
				maxV = v
			}
		}
		n := len(values)
		bw := r.W / float32(n)
		for i, v := range values {
			h := float32(v/maxV) * r.H * 0.9
			if h < 3 {
				h = 3
			}
			bar := ui.Rect{X: r.X + float32(i)*bw + bw*0.2, Y: r.Y + r.H - h, W: bw * 0.6, H: h}
			p.FillPath(rectPath(bar, 2), color.Alpha(0.85))
		}
	})
}

// sparkLine draws a tiny area chart.
func sparkLine(c *ui.Context, color ui.Color, values []float64) ui.Element {
	return ui.Box(c).Width(84).Height(40).Draw(func(p *ui.Painter, r ui.Rect) {
		if len(values) < 2 {
			return
		}
		maxV := 1.0
		for _, v := range values {
			if v > maxV {
				maxV = v
			}
		}
		step := r.W / float32(len(values)-1)
		var line, area ui.Path
		for i, v := range values {
			x := r.X + float32(i)*step
			y := r.Y + r.H - float32(v/maxV)*r.H*0.9
			if i == 0 {
				line.MoveTo(x, y)
				area.MoveTo(x, r.Y+r.H).LineTo(x, y)
			} else {
				line.LineTo(x, y)
				area.LineTo(x, y)
			}
		}
		area.LineTo(r.X+r.W, r.Y+r.H)
		area.Close()
		p.FillPath(&area, color.Alpha(0.12))
		p.StrokePath(&line, 1.8, color)
	})
}

// rectPath returns a rounded rectangle path.
func rectPath(r ui.Rect, radius float32) *ui.Path {
	var path ui.Path
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	rr := radius
	if rr*2 > r.W {
		rr = r.W / 2
	}
	if rr*2 > r.H {
		rr = r.H / 2
	}
	path.MoveTo(x0+rr, y0)
	path.LineTo(x1-rr, y0)
	path.QuadTo(x1, y0, x1, y0+rr)
	path.LineTo(x1, y1-rr)
	path.QuadTo(x1, y1, x1-rr, y1)
	path.LineTo(x0+rr, y1)
	path.QuadTo(x0, y1, x0, y1-rr)
	path.LineTo(x0, y0+rr)
	path.QuadTo(x0, y0, x0+rr, y0)
	path.Close()
	return &path
}

// ── filter tabs + toolbar ────────────────────────────────────────────────

var tabDefs = []struct {
	label string
	cat   string
}{
	{"全部", ""},
	{"视频", "video"},
	{"文档", "doc"},
	{"压缩包", "archive"},
	{"软件", "program"},
}

func (a *app) tabRow(c *ui.Context, p *style.Palette, snaps []download.Snapshot) {
	ui.Row(c).Gap(8).Children(func() {
		for i, t := range tabDefs {
			i, t := i, t
			n := 0
			for _, s := range snaps {
				if t.cat == "" || inCategory(t.cat, s) {
					n++
				}
			}
			active := a.tab == i
			item := ui.ButtonBase(c).Padding(8, 12).Radius(8)
			if active {
				item = item.Background(style.BadgeBlueBg)
			}
			item.Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					label := ui.Text(c, tr(t.label)).FontSize(13)
					if active {
						label = label.Bold().TextColor(style.BadgeBlueFg)
					}
					ui.Text(c, fmt.Sprint(n)).FontSize(11).TextColor(p.TextFaint)
				})
			})
			if item.Clicked() {
				a.tab = i
				a.sel = -1
			}
		}
	})
}

func (a *app) toolbarRow(c *ui.Context, p *style.Palette, snaps []download.Snapshot) {
	all := true
	for _, s := range snaps {
		if !a.checked(s.ID) {
			all = false
			break
		}
	}
	checked := all && len(snaps) > 0
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		style.Checkbox(c, p, &checked, "全选")
		if checked != all {
			for _, s := range snaps {
				*a.checkbox(s.ID) = checked
			}
		}
		ui.Box(c).Width(1).Height(22).Background(p.Border)
		if toolButton(c, p, style.IconPause, "暂停").Clicked() {
			a.command("停止全部")
		}
		if toolButton(c, p, style.IconPlay, "继续").Clicked() {
			a.command("开始队列")
		}
		if toolButton(c, p, style.IconTrash, "删除").Clicked() {
			a.command("删除所选")
		}
		if toolButton(c, p, style.IconBolt, "限速").Clicked() {
			a.limitOn = !a.limitOn
			a.status = map[bool]string{true: "已开启下载限速", false: "已关闭下载限速"}[a.limitOn]
		}
		ui.Row(c).Fill().Children(func() {})
		if iconButton(c, p, style.IconSort, false).Clicked() {
			if a.list.Sort != nil {
				a.list.Sort.Descending = !a.list.Sort.Descending
			}
		}
		ui.Row(c).Padding(6, 10).Gap(6).Radius(8).Background(p.Surface2).
			Width(190).AlignItems(ui.Center).Children(func() {
			style.Icon(c, style.IconSearch, 13, p.TextSoft)
			c.Key("tablesearch")
			ui.TextInput(c, &a.query).Fill().Placeholder(tr("搜索下载任务..."))
		})
		if iconButton(c, p, style.IconList, false).Clicked() {
			a.status = tr("列表视图")
		}
	})
}

// ── task table ───────────────────────────────────────────────────────────

var tableCols = []ui.TableColumn{
	{Title: "文件名", MinWidth: 260, Sortable: true},
	{Title: "大小", Width: 90, MinWidth: 80, Align: ui.End, Sortable: true},
	{Title: "进度", Width: 130, MinWidth: 110},
	{Title: "速度", Width: 96, MinWidth: 84, Align: ui.End, Sortable: true},
	{Title: "剩余时间", Width: 90, MinWidth: 80, Align: ui.End},
	{Title: "状态", Width: 108, MinWidth: 96, Sortable: true},
	{Title: "来源", Width: 64, MinWidth: 56},
	{Title: "操作", Width: 110, MinWidth: 96},
}

func (a *app) columnsFor(w float32) []ui.TableColumn {
	show := map[string]bool{
		"来源":   w >= 1060,
		"剩余时间": w >= 940,
		"速度":   w >= 860,
	}
	var cols []ui.TableColumn
	for _, c := range tableCols {
		if skip, known := show[c.Title]; known && !skip {
			continue
		}
		c.ID = c.Title
		c.Title = tr(c.Title)
		cols = append(cols, c)
	}
	return cols
}

func (a *app) tableCard(c *ui.Context, p *style.Palette, cols []ui.TableColumn, snaps []download.Snapshot) {
	card(c, p, func() {
		ui.Column(c).Padding(8).Children(func() {
			a.toolbarRow(c, p, snaps)
			ui.Box(c).FillWidth().Height(8)
			if len(snaps) == 0 {
				ui.Column(c).Fill().Center().Gap(8).Padding(40).Children(func() {
					ui.Text(c, tr("还没有下载任务")).FontSize(14).TextColor(p.TextSoft)
					ui.Text(c, tr("点「新建下载」粘贴链接，或从浏览器扩展嗅探发送")).FontSize(12).TextColor(p.TextFaint)
				})
				return
			}
			ui.Table(c, &a.list, cols, len(snaps), func(row, col int) {
				a.cell(c, p, cols, snaps[row], col)
			}).Fill()
		})
	})
}

func (a *app) cell(c *ui.Context, p *style.Palette, cols []ui.TableColumn, s download.Snapshot, col int) {
	columnID := cols[col].ID
	if columnID == "" {
		columnID = cols[col].Title
	}
	switch columnID {
	case "文件名":
		ui.Row(c).Gap(10).Padding(8, 4).AlignItems(ui.Center).Children(func() {
			style.Checkbox(c, p, a.checkbox(s.ID), "").Label(tr("选择任务 ") + s.Name)
			ui.Box(c).Size(30, 30).Radius(8).Background(p.Surface2).Center().Children(func() {
				style.Icon(c, categoryIcon(s.Name), 16, extColor(p, s.Name))
			})
			ui.Text(c, trunc(s.Name, 44)).SingleLine().FontSize(13)
		})
	case "大小":
		ui.Text(c, tr(sizeText(s))).SingleLine().FontSize(12).TextColor(p.TextSoft)
	case "进度":
		ui.Row(c).Gap(8).Padding(0, 0, 6, 0).AlignItems(ui.Center).Children(func() {
			ui.Progress(c, progress(s)).Fill()
			ui.Text(c, tr(pctText(s))).SingleLine().FontSize(11).TextColor(p.TextSoft).Width(40)
		})
	case "速度":
		ui.Text(c, tr(speedText(s))).SingleLine().FontSize(12).TextColor(p.TextSoft)
	case "剩余时间":
		ui.Text(c, tr(remainText(s))).SingleLine().FontSize(12).TextColor(p.TextSoft)
	case "状态":
		bg, fg, label := stateBadge(p, s)
		pill(c, bg, fg, label)
	case "来源":
		ui.Box(c).Size(24, 24).Radius(6).Background(p.Surface2).Center().Children(func() {
			ui.Text(c, tr(sourceGlyph(s))).FontSize(11).Bold().TextColor(p.TextSoft)
		})
	case "操作":
		ui.Row(c).Gap(2).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
			switch s.State {
			case download.Running:
				if smallIconButton(c, p, style.IconPause).Clicked() {
					a.mgr.Cancel(s.ID)
				}
				if smallIconButton(c, p, style.IconClose).Clicked() {
					a.mgr.Cancel(s.ID)
					a.mgr.Remove(s.ID)
				}
			case download.Queued:
				if smallIconButton(c, p, style.IconPlay).Clicked() {
					a.mgr.Cancel(s.ID)
				}
				if smallIconButton(c, p, style.IconClose).Clicked() {
					a.mgr.Cancel(s.ID)
					a.mgr.Remove(s.ID)
				}
			case download.Done:
				if smallIconButton(c, p, style.IconFolder).Clicked() {
					a.status = tr("打开文件夹：") + filepath.Dir(s.FilePath)
				}
			default:
				if smallIconButton(c, p, style.IconPlay).Clicked() {
					a.mgr.Add(download.Request{URL: s.URL})
				}
				if smallIconButton(c, p, style.IconClose).Clicked() {
					a.mgr.Remove(s.ID)
				}
			}
			if smallIconButton(c, p, style.IconDots).Clicked() {
				a.detailID = s.ID
				a.showDetail = true
			}
		})
	}
}

func sourceGlyph(s download.Snapshot) string {
	if u, err := urlParse(s.URL); err == nil && u != "" {
		return strings.ToUpper(u[:1])
	}
	return "?"
}

// stateBadge maps a task onto the mock's status pills.
func stateBadge(p *style.Palette, s download.Snapshot) (bg, fg ui.Color, label string) {
	if stateText(s) == "文件已删除" {
		return style.BadgeOrangeBg, style.BadgeOrangeFg, "文件已删除"
	}
	switch s.State {
	case download.Running:
		return style.BadgeBlueBg, style.BadgeBlueFg, "下载中"
	case download.Done:
		return style.BadgeGreenBg, style.BadgeGreenFg, "已完成"
	case download.Queued:
		return style.BadgeOrangeBg, style.BadgeOrangeFg, "排队中"
	case download.Canceled:
		return style.BadgeYellowBg, style.BadgeYellowFg, "已暂停"
	default:
		return ui.Hex("#FDECEC"), ui.Hex("#DC2626"), "出错"
	}
}

// ── queue speed chart ────────────────────────────────────────────────────

func (a *app) queueCard(c *ui.Context, p *style.Palette, snaps []download.Snapshot) {
	var running, done, paused int
	for _, s := range snaps {
		switch s.State {
		case download.Running, download.Queued:
			running++
		case download.Done:
			done++
		case download.Canceled:
			paused++
		}
	}
	card(c, p, func() {
		ui.Column(c).Padding(16).Gap(12).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("任务队列")).FontSize(14).Bold()
					ui.Text(c, fmt.Sprintf(tr("%d 个任务（%d 个下载中，%d 个已完成，%d 个已暂停）"),
						len(snaps), running, done, paused)).FontSize(12).TextColor(p.TextFaint)
				})
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("总速度: ")+bytesText(a.totalSpeed(snaps))+"/s").FontSize(12).TextColor(p.TextSoft)
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						style.Icon(c, style.IconArrowDown, 14, style.BadgeBlueFg)
						ui.Text(c, bytesText(a.peakSpeed())+"/s").FontSize(12).TextColor(style.BadgeBlueFg)
					})
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						style.Icon(c, style.IconArrowUp, 14, style.BadgeGreenFg)
						ui.Text(c, tr("0 B/s")).FontSize(12).TextColor(style.BadgeGreenFg)
					})
				})
			})
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				speedChart(c, p, a.histValues(40)).Fill()
				ui.Column(c).Gap(8).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						style.Icon(c, style.IconStatus, 10, style.ChartLine)
						ui.Text(c, tr("下载速度 ")+bytesText(a.totalSpeed(snaps))+"/s").SingleLine().Width(160).FontSize(12).TextColor(p.TextSoft)
					})
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						style.Icon(c, style.IconStatus, 10, style.ChartUp)
						ui.Text(c, tr("上传速度 0 B/s")).SingleLine().Width(160).FontSize(12).TextColor(p.TextSoft)
					})
				})
			})
		})
	})
}

// speedChart draws the download history: grid, axis labels, area and line.
func speedChart(c *ui.Context, p *style.Palette, values []float64) ui.Element {
	return ui.Box(c).Height(150).Draw(func(p2 *ui.Painter, r ui.Rect) {
		top := float64(10 << 20) // at least 10 MB/s on the top grid line
		for _, v := range values {
			if v > top {
				top = v * 1.25
			}
		}
		// Grid lines and labels at top, half and zero.
		for i := 0; i < 3; i++ {
			y := r.Y + r.H*float32(i)/2
			var grid ui.Path
			grid.MoveTo(r.X+48, y).LineTo(r.X+r.W, y)
			p2.StrokePath(&grid, 1, p.Border)
		}
		if len(values) < 2 {
			return
		}
		step := (r.W - 48) / float32(len(values)-1)
		var line, area ui.Path
		for i, v := range values {
			x := r.X + 48 + float32(i)*step
			y := r.Y + r.H - float32(v/top)*r.H
			if i == 0 {
				line.MoveTo(x, y)
				area.MoveTo(x, r.Y+r.H).LineTo(x, y)
			} else {
				line.LineTo(x, y)
				area.LineTo(x, y)
			}
		}
		area.LineTo(r.X+48+float32(len(values)-1)*step, r.Y+r.H)
		area.Close()
		p2.FillPath(&area, style.ChartLine.Alpha(0.12))
		p2.StrokePath(&line, 2, style.ChartLine)
	})
}

// ── right detail panel ───────────────────────────────────────────────────

func (a *app) detailPanel(c *ui.Context, p *style.Palette, snaps []download.Snapshot) {
	if !a.showDetail {
		return
	}
	s, ok := a.detailByID(snaps)
	if !ok {
		return
	}
	ui.Column(c).Width(340).FillHeight().Background(p.Card).Padding(16).Gap(12).
		Children(func() {
			ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Text(c, tr("下载详情")).FontSize(15).Bold()
				if smallIconButton(c, p, style.IconClose).Clicked() {
					a.showDetail = false
				}
			})

			// File card.
			card(c, p, func() {
				ui.Row(c).Padding(12).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(44, 44).Radius(10).Background(style.DetailIconBg).Center().Children(func() {
						style.Icon(c, categoryIcon(s.Name), 22, style.DetailIconFg)
					})
					ui.Column(c).Gap(4).Fill().Children(func() {
						ui.Text(c, trunc(s.Name, 26)).SingleLine().Width(230).FontSize(13).Bold()
						ui.Text(c, sizeText(s)+"，"+categoryLabel(s.Name)+tr("文件")).
							SingleLine().FontSize(11).TextColor(p.TextFaint)
						bg, fg, label := stateBadge(p, s)
						pill(c, bg, fg, label+" "+pctText(s))
					})
				})
			})

			// Basic info.
			card(c, p, func() {
				ui.Column(c).Padding(12).Gap(8).Children(func() {
					ui.Text(c, tr("基本信息")).FontSize(13).Bold()
					infoRow(c, p, "文件名", trunc(s.Name, 26), false)
					infoRow(c, p, "文件大小", sizeText(s), false)
					infoRow(c, p, "文件类型", categoryLabel(s.Name)+tr("文件 (")+filepath.Ext(s.Name)+")", false)
					infoRow(c, p, "来源网站", hostOf(s.URL), true)
					infoRow(c, p, "下载链接", trunc(s.URL, 30), true)
					infoRow(c, p, "创建时间", s.Created.Format("2006-01-02 15:04:05"), false)
				})
			})

			// Storage path.
			card(c, p, func() {
				ui.Column(c).Padding(12).Gap(8).Children(func() {
					ui.Text(c, tr("存储路径")).FontSize(13).Bold()
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						style.Icon(c, style.IconFolder, 15, p.TextSoft)
						ui.Text(c, trunc(filepath.Dir(s.FilePath), 24)).SingleLine().
							FontSize(12).TextColor(p.TextSoft).Fill()
						if flatButton(c, "打开文件夹", 6, 10).Clicked() {
							a.status = tr("打开文件夹：") + filepath.Dir(s.FilePath)
						}
					})
				})
			})

			// Threads.
			card(c, p, func() {
				ui.Column(c).Padding(12).Gap(8).Children(func() {
					ui.Text(c, tr("下载线程")).FontSize(13).Bold()
					ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
						ui.Stepper(c, &a.threads, 1, 32, 1).Width(96)
						ui.Slider(c, &a.threads, 1, 32).Fill()
					})
				})
			})

			// Checksums.
			h := a.hashFor(s)
			card(c, p, func() {
				ui.Column(c).Padding(12).Gap(8).Children(func() {
					ui.Text(c, tr("校验信息")).FontSize(13).Bold()
					hashRow(c, p, a, "SHA-256", h.sha)
					hashRow(c, p, a, "MD5", h.md5)
				})
			})

			// Schedule options.
			card(c, p, func() {
				ui.Column(c).Padding(12).Gap(10).Children(func() {
					ui.Text(c, tr("调度选项")).FontSize(13).Bold()
					ui.Segmented(c, &a.seg, "自动", "手动", "定时")
					switchRow(c, p, &a.limitOn, "下载限速", "100 MB/s")
					switchRow(c, p, &a.shutdownOn, "完成后关机", "下载完成后自动关机")
					switchRow(c, p, &a.verifyOn, "完成后校验", "下载完成后自动校验文件完整性")
				})
			})

			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, tr("暂停")).Clicked() {
					a.mgr.Cancel(s.ID)
					a.status = tr("已暂停：") + s.Name
				}
				if toolButton(c, p, style.IconPlay, "继续").Clicked() {
					a.mgr.Add(download.Request{URL: s.URL})
					a.status = tr("已重新排队：") + s.Name
				}
				if toolButton(c, p, style.IconFolder, "打开文件夹").Clicked() {
					a.status = tr("打开文件夹：") + filepath.Dir(s.FilePath)
				}
			})
		})
}

func infoRow(c *ui.Context, p *style.Palette, label, value string, link bool) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, tr(label)).FontSize(12).TextColor(p.TextFaint).Width(60)
		v := ui.Text(c, tr(value)).SingleLine().Width(228).FontSize(12)
		if link {
			v = v.TextColor(style.BadgeBlueFg)
		}
	})
}

func hashRow(c *ui.Context, p *style.Palette, a *app, label, value string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, tr(label)).FontSize(12).TextColor(p.TextFaint).Width(60)
		ui.Text(c, tr(value)).SingleLine().Width(190).FontSize(12).TextColor(p.TextSoft)
		if smallIconButton(c, p, style.IconCopy).Clicked() {
			a.copyText(value)
		}
	})
}

func switchRow(c *ui.Context, p *style.Palette, on *bool, label, hint string) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Switch(c, on)
		ui.Text(c, tr(label)).FontSize(12)
		ui.Text(c, tr(hint)).FontSize(11).TextColor(p.TextFaint)
	})
}

// ── data plumbing for the detail panel ───────────────────────────────────

func (a *app) detailByID(snaps []download.Snapshot) (download.Snapshot, bool) {
	id := a.detailID
	if a.sel >= 0 && a.sel < len(snaps) {
		id = snaps[a.sel].ID
		a.detailID = id
	}
	if id == "" && len(snaps) > 0 {
		id = snaps[0].ID
		a.detailID = id
	}
	for _, s := range snaps {
		if s.ID == id {
			return s, true
		}
	}
	return download.Snapshot{}, false
}

func (a *app) copyText(s string) {
	if a.svc != nil {
		a.svc.WriteClipboard(s)
		a.copyUntil = time.Now().Add(1600 * time.Millisecond)
	}
}

// hashFor returns the checksums of the selected file, computing them in the
// background on first ask.
func (a *app) hashFor(s download.Snapshot) fileHash {
	a.hashMu.Lock()
	defer a.hashMu.Unlock()
	if a.hashes == nil {
		a.hashes = map[string]fileHash{}
	}
	h, ok := a.hashes[s.ID]
	if ok {
		return h
	}
	if s.FilePath == "" {
		return fileHash{sha: "—", md5: "—", done: true}
	}
	a.hashes[s.ID] = fileHash{sha: "计算中…", md5: "计算中…"}
	path := s.FilePath
	id := s.ID
	go func() {
		sha, md := hashFile(path)
		a.hashMu.Lock()
		a.hashes[id] = fileHash{sha: sha, md5: md, done: true}
		a.hashMu.Unlock()
		if a.svc != nil {
			a.svc.Invalidate()
		}
	}()
	return a.hashes[s.ID]
}

func hashFile(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "—", "—"
	}
	defer f.Close()
	sha := sha256.New()
	md := md5.New()
	if _, err := io.Copy(io.MultiWriter(sha, md), f); err != nil {
		return "—", "—"
	}
	return shortHash(hex.EncodeToString(sha.Sum(nil))), shortHash(hex.EncodeToString(md.Sum(nil)))
}

// shortHash renders "3f2a6e…d9c4e7" like the reference.
func shortHash(h string) string {
	if len(h) <= 11 {
		return h
	}
	return h[:6] + "…" + h[len(h)-5:]
}
