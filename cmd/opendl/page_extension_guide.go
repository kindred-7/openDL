package main

import (
	"log"
	"os"
	"path/filepath"

	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func extensionGuidePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "extension", "安装说明.txt")
}

func extensionGuideMarker() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "opendl", "extension-guide-seen")
}

func shouldShowExtensionGuide() bool {
	guide, marker := extensionGuidePath(), extensionGuideMarker()
	if guide == "" || marker == "" {
		return false
	}
	if _, err := os.Stat(guide); err != nil {
		return false
	}
	_, err := os.Stat(marker)
	return os.IsNotExist(err)
}

func (pg *mainPage) closeExtensionGuide() {
	pg.showExtensionGuide = false
	marker := extensionGuideMarker()
	if marker == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err == nil {
		err = os.WriteFile(marker, []byte("1\n"), 0600)
		if err != nil {
			log.Printf("extension guide: %v", err)
		}
	} else {
		log.Printf("extension guide: %v", err)
	}
}

func (pg *mainPage) extensionGuideDialog(c *ui.Context, p *d1Palette) {
	c.Key("extension-guide-dialog")
	ui.DialogBase(c, &pg.showExtensionGuide, func(back, panel ui.Element) {
		back.Background(p.fg.Alpha(.15)).Center()
		panel.Width(440).Radius(style.RadiusLG).Border(1, p.bd).Background(p.bg).Children(func() {
			ui.Column(c).FillWidth().Children(func() {
				ui.Row(c).FillWidth().Padding(12, 16).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("安装浏览器扩展")).Grow(1).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg)
					d1IconButton(c, p, "x").Size(28, 28).Label(tr("关闭")).OnClick(pg.closeExtensionGuide)
				})
				ui.Box(c).FillWidth().Height(1).Background(p.bd)
				ui.Column(c).FillWidth().Padding(24, 20).Gap(12).Children(func() {
					ui.Text(c, tr("安装扩展后，可以从浏览器发送下载任务到 OpenDL。扩展尚未上架，请按照说明手动安装。")).FontSize(style.TextSM).TextColor(p.fg)
					ui.Text(c, tr("以后可在应用菜单中再次打开安装说明。")).FontSize(style.TextSM).TextColor(p.mu)
				})
				ui.Box(c).FillWidth().Height(1).Background(p.bd)
				ui.Row(c).FillWidth().Padding(12, 16).Gap(8).Justify(ui.End).Children(func() {
					d1Button(c, p, "稍后再看", "").OnClick(pg.closeExtensionGuide)
					d1Button(c, p, "打开安装说明", "").OnClick(func() {
						if err := mygo.Shell.OpenPath(extensionGuidePath()); err != nil {
							pg.status = tr("无法打开浏览器扩展安装说明")
							return
						}
						pg.closeExtensionGuide()
					})
				})
			})
		})
	})
}
