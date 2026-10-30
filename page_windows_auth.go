package main

import (
	"openDL/download"
	"openDL/style"
	"openDL/winauth"
	"runtime"

	"github.com/egoist/mygo/ui"
)

func (s *settingsPage) windowsAuthFields(c *ui.Context, p *d1Palette) {
	if s.windowsPolicy == "" {
		s.windowsPolicy = download.AuthKerberosOnly
	}
	ui.Column(c).FillWidth().Gap(12).Children(func() {
		ui.Text(c, tr("按协议、主机和端口精确匹配，不包含子域名；配置后优先使用系统身份，不使用该站点保存的账号密码。")).FillWidth().FontSize(style.TextSM).TextColor(p.mu)
		if !winauth.Available {
			settingsError(c, p, tr("当前版本未启用系统集成认证"))
		}
		if _, err := download.NormalizeWindowsAuthSites(s.owner.preferences.WindowsAuth); err != nil {
			settingsError(c, p, err.Error())
			d1Button(c, p, "清空无效配置", "").OnClick(func() { s.saveWindowsAuth(nil) })
		}
		for _, entry := range s.owner.preferences.WindowsAuth {
			c.Key(entry.Origin)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
					ui.Text(c, entry.Origin).FontSize(style.TextSM).TextColor(p.fg).SingleLine().Tooltip(entry.Origin)
					label := tr("仅 Kerberos")
					if entry.Policy == download.AuthAllowNTLM {
						label = tr("允许 NTLM 回退")
					}
					if entry.SPN != "" {
						label += "（" + entry.SPN + "）"
					}
					ui.Text(c, label).FontSize(style.TextSM).TextColor(p.mu).SingleLine().Tooltip(label)
				})
				d1Button(c, p, "删除", "trash").OnClick(func() {
					sites := make([]download.WindowsAuthSite, 0)
					for _, site := range s.owner.preferences.WindowsAuth {
						if site.Origin != entry.Origin {
							sites = append(sites, site)
						}
					}
					s.saveWindowsAuth(sites)
				})
			})
		}
		ui.Text(c, tr("站点地址")).FontSize(style.TextSM).TextColor(p.fg)
		settingsInput(c, p, "站点地址", "windows-auth-origin", &s.windowsOrigin, "https://intranet.example.com").FillWidth()
		c.Key("windows-auth-policy")
		d1Choice(c, p, "认证策略", &s.windowsPolicy, []d1Option{{value: download.AuthKerberosOnly, label: "仅 Kerberos"}, {value: download.AuthAllowNTLM, label: "允许 NTLM 回退"}}).FillWidth()
		ui.Text(c, tr("服务 SPN（可选）")).FontSize(style.TextSM).TextColor(p.fg)
		settingsInput(c, p, "服务 SPN（可选）", "windows-auth-spn", &s.windowsSPN, "自动：HTTP/主机").FillWidth()
		if runtime.GOOS == "windows" {
			ui.Text(c, tr("仅 Kerberos 会在协商前禁止 NTLM；允许回退时由 Windows 优先协商 Kerberos，必要时使用 NTLM。")).FillWidth().FontSize(style.TextSM).TextColor(p.mu)
		} else {
			ui.Text(c, tr("GSSAPI 使用系统身份；NTLM 支持取决于已安装的系统机制。")).FillWidth().FontSize(style.TextSM).TextColor(p.mu)
		}
		settingsError(c, p, s.windowsError)
		d1PrimaryButton(c, p, "添加或替换", "").Disabled(!winauth.Available).AlignSelf(ui.End).OnClick(func() {
			one, err := download.NormalizeWindowsAuthSites([]download.WindowsAuthSite{{Origin: s.windowsOrigin, Policy: s.windowsPolicy, SPN: s.windowsSPN}})
			if err != nil {
				s.windowsError = err.Error()
				s.changed()
				return
			}
			sites := make([]download.WindowsAuthSite, 0, len(s.owner.preferences.WindowsAuth)+1)
			for _, site := range s.owner.preferences.WindowsAuth {
				if site.Origin != one[0].Origin {
					sites = append(sites, site)
				}
			}
			if s.saveWindowsAuth(append(sites, one[0])) {
				s.windowsOrigin = ""
				s.windowsSPN = ""
			}
		})
	})
}

func (s *settingsPage) saveWindowsAuth(sites []download.WindowsAuthSite) bool {
	normalized, err := download.NormalizeWindowsAuthSites(sites)
	if err == nil && s.owner.mgr != nil {
		err = s.owner.mgr.SetWindowsAuthSites(normalized)
	}
	if err != nil {
		s.windowsError = err.Error()
		s.changed()
		return false
	}
	s.owner.preferences.WindowsAuth = normalized
	s.windowsError = ""
	s.owner.saveAppPreferences()
	s.changed()
	return true
}
