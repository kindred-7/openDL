package main

import (
	"errors"
	"fmt"
	"log"

	"openDL/extensionstore"
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

func (s *settingsPage) browserExtensionFields(c *ui.Context, p *d1Palette) {
	ui.Row(c).FillWidth().Gap(style.GapSM).Wrap().Justify(ui.Start).Children(func() {
		for _, option := range []struct {
			browser extensionstore.Browser
			name    string
			icon    string
		}{
			{extensionstore.Chrome, "Chrome", style.IconBrowserChrome},
			{extensionstore.Edge, "Edge", style.IconBrowserEdge},
			{extensionstore.Firefox, "Firefox", style.IconBrowserFirefox},
		} {
			label := fmt.Sprintf(tr("在 %s 中打开扩展商店"), option.browser)
			c.Key(string(option.browser))
			button, color := d1ButtonFrame(c, p, !s.browserExtensionOpening)
			button.Shrink(0).Height(settingsInputHeight).Padding(0, 10).Label(label).Tooltip(label).
				Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Row(c).Gap(style.GapXS).AlignItems(ui.Center).Children(func() {
							d1Icon(c, option.icon, style.SizeIcon, color)
							ui.Text(c, option.name).FontSize(style.TextTitle).TextColor(color).SingleLine()
						})
						d1Icon(c, "external-link", style.SizeIcon-2, color)
					})
				}).
				OnClick(func() { s.openBrowserExtension(option.browser) })
		}
	})
	settingsError(c, p, s.browserExtensionError)
}

func (s *settingsPage) openBrowserExtension(browser extensionstore.Browser) {
	win := s.win
	if win == nil || s.browserExtensionOpening {
		return
	}
	s.browserExtensionOpening, s.browserExtensionError = true, ""
	go func() {
		err := extensionstore.Open(browser)
		win.Update(func() {
			s.browserExtensionOpening = false
			if errors.Is(err, extensionstore.ErrBrowserNotFound) {
				s.browserExtensionError = fmt.Sprintf(tr("找不到 %s，请先安装该浏览器。"), browser)
			} else if err != nil {
				log.Printf("open extension store in %s: %v", browser, err)
				s.browserExtensionError = fmt.Sprintf(tr("无法在 %s 中打开扩展商店，请重试。"), browser)
			}
		})
	}()
}
