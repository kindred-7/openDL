package main

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"openDL/download"
	"openDL/pathrec"
	"openDL/style"
)

// Each request owns its window and draft so concurrent browser prompts never
// share credentials or overwrite one another.
type browserDownloadWindow struct {
	owner                         *mainPage
	win                           *mygo.Window
	req                           download.Request
	directory, name, message      string
	suggestedName                 string
	keepExtension, saving, closed bool
}

func (pg *mainPage) openBrowserDownload(req download.Request) {
	d := &browserDownloadWindow{owner: pg, req: req, directory: pg.saveDir, name: req.Name, keepExtension: true}
	if d.name == "" {
		if name := pathrec.FromURLPath(req.URL); name != "" {
			d.name = pathrec.SanitizeFilename(name)
			d.suggestedName = d.name
		}
	}
	if req.Directory != "" {
		d.directory = req.Directory
	}
	title := tr("添加下载")
	if name := strings.TrimSpace(req.Name); name != "" {
		title += " - " + pathrec.SanitizeFilename(name)
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: title, Width: 520, Height: 320, MinWidth: 420, MinHeight: 290, AlwaysOnTop: true, Content: ui.View(d.view)})
	d.win = w
	_ = w.SetIcon(appIconPNG)
	w.OnClosed(func() {
		d.closed = true
		d.req.Headers = nil
		d.win = nil
		if pg.browserPromptSlot != nil {
			pg.browserPromptSlot()
		}
	})
}

func (d *browserDownloadWindow) view(c *ui.Context) {
	p := d1AccentPalette(d.owner.dark, d.owner.preferences.AccentColor)
	c.SetTheme(p.theme())
	ui.Column(c).Fill().Padding(16).Gap(12).Background(p.bg).Children(func() {
		ui.Text(c, tr("添加下载")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
		ui.Column(c).FillWidth().MinWidth(0).Gap(5).Children(func() {
			ui.Text(c, tr("下载链接")).FontSize(style.TextSM).TextColor(p.mu)
			ui.Text(c, d.req.URL).Selectable().Tooltip(d.req.URL).FillWidth().MinWidth(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine()
		})
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			ui.Text(c, tr("保存到")).FontSize(style.TextSM).TextColor(p.mu)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				c.Key("browser-directory")
				ui.TextInputBase(c, &d.directory).Grow(1).Shrink(1).Basis(0).MinWidth(0).Height(30).Padding(0, 10).Radius(style.RadiusSM).Border(1, p.bd).Background(p.card).FontSize(style.TextSM).TextColor(p.fg).Label(tr("保存到"))
				d1Button(c, p, "选择文件夹", "folder").Disabled(d.saving).OnClick(func() {
					path, cancel := chooseFolder(d.win, tr("选择保存文件夹"), d.directory)
					if !cancel {
						d.directory = path
					}
				})
			})
		})
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() { d1Input(c, p, "文件名（可选，仅单个链接）", "browser-filename", &d.name, false) })
			ui.Row(c).Shrink(0).Height(30).AlignItems(ui.Center).Children(func() { d1Switch(c, p, "保持原文件后缀", "", &d.keepExtension).Disabled(d.saving) })
		})
		if d.message != "" {
			ui.Text(c, d.message).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
		}
		ui.Box(c).Grow(1).Shrink(1).Basis(0)
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			d1Button(c, p, "取消", "").Disabled(d.saving).OnClick(func() { d.win.Close() })
			d1PrimaryButton(c, p, "开始下载", "down", !d.saving).OnClick(d.submit)
		})
	})
}

func (d *browserDownloadWindow) submit() {
	if d.saving || d.win == nil {
		return
	}
	dir := strings.TrimSpace(d.directory)
	if !filepath.IsAbs(dir) {
		d.message = tr("保存目录需要填写完整的绝对路径")
		return
	}
	if d.name != "" && d.name != pathrec.SanitizeFilename(d.name) {
		d.message = tr("文件名无效，请检查非法字符及首尾空格")
		return
	}
	req := d.req
	req.Directory, req.Name, req.KeepExtension = dir, d.name, d.keepExtension
	if d.suggestedName != "" && d.name == d.suggestedName {
		req.Name = ""
	}
	d.saving, d.message = true, ""
	win := d.win
	go func() {
		id, err := d.owner.mgr.AddRecorded(req)
		win.Update(func() {
			if d.closed {
				return
			}
			d.saving = false
			if err != nil {
				d.message = err.Error()
				return
			}
			d.owner.sel = id
			win.Close()
			d.owner.showMainWindow()
		})
	}()
}
