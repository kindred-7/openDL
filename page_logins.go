package main

// 站点登录表设置：已有条目列表与新增行，凭据加密保存后作用于新任务。

import (
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

// syncSiteLogins 读取保存的登录表作为编辑草稿。
func (s *settingsPage) syncSiteLogins() {
	entries, err := openSiteLogins(s.owner.preferences.SiteLoginsSecret)
	s.siteLogins = entries
	s.loginHost, s.loginUser, s.loginPassword = "", "", ""
	s.loginError = ""
	if err != nil {
		s.loginError = err.Error()
	}
}

func (s *settingsPage) siteLoginsFields(c *ui.Context, p *d1Palette) {
	ui.Column(c).FillWidth().Gap(12).Children(func() {
		for i, entry := range s.siteLogins {
			index := i
			c.Key(entry.Host)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, entry.Host).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.fg).SingleLine().Tooltip(entry.Host)
				ui.Text(c, entry.User).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextSM).TextColor(p.mu).SingleLine().Tooltip(entry.User)
				d1SelectionAction(c, p, "删除", "trash", p.bad, true).OnClick(func() { s.removeSiteLogin(index) })
			})
		}
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			ui.Text(c, tr("站点")).FontSize(style.TextSM).TextColor(p.fg)
			settingsInput(c, p, "站点", "settings-login-host", &s.loginHost, "example.com").FillWidth()
		})
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
				ui.Text(c, tr("用户名")).FontSize(style.TextSM).TextColor(p.fg)
				settingsInput(c, p, "用户名", "settings-login-user", &s.loginUser, "").FillWidth()
			})
			ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
				ui.Text(c, tr("密码")).FontSize(style.TextSM).TextColor(p.fg)
				c.Key("settings-login-password")
				// 密码输入不复用 settingsInput 的取值提示。
				input := ui.TextInputBase(c, &s.loginPassword).Password().FillWidth().Height(32).
					Padding(0, 10).Radius(style.RadiusSM).Border(1, p.bd2).Background(p.bg).
					TextColor(p.fg).FontSize(style.TextSM).Label(tr("站点密码"))
				if input.Focused() {
					input.BorderColor(p.pri)
				}
			})
		})
		settingsError(c, p, s.loginError)
		d1PrimaryButton(c, p, "添加", "").AlignSelf(ui.End).OnClick(s.addSiteLogin)
	})
}

// addSiteLogin 校验新增行并保存；同站点的旧条目被替换。
func (s *settingsPage) addSiteLogin() {
	normalized := normalizeSiteLogins([]siteLoginEntry{{Host: s.loginHost, User: s.loginUser, Password: s.loginPassword}})
	if len(normalized) == 0 {
		s.loginError = tr("请填写站点与用户名")
		s.changed()
		return
	}
	entries := make([]siteLoginEntry, 0, len(s.siteLogins)+1)
	for _, entry := range s.siteLogins {
		if entry.Host != normalized[0].Host {
			entries = append(entries, entry)
		}
	}
	if len(entries) >= maxSiteLogins {
		s.loginError = tr("站点登录最多 256 条")
		s.changed()
		return
	}
	s.saveSiteLogins(append(entries, normalized[0]))
	if s.loginError == "" {
		s.loginHost, s.loginUser, s.loginPassword = "", "", ""
	}
}

// removeSiteLogin 删除一条登录记录。
func (s *settingsPage) removeSiteLogin(index int) {
	if index < 0 || index >= len(s.siteLogins) {
		return
	}
	entries := append(append([]siteLoginEntry{}, s.siteLogins[:index]...), s.siteLogins[index+1:]...)
	s.saveSiteLogins(entries)
}

// saveSiteLogins 校验、加密保存并同步到下载引擎。
func (s *settingsPage) saveSiteLogins(entries []siteLoginEntry) {
	entries = normalizeSiteLogins(entries)
	secret := ""
	if len(entries) > 0 {
		sealed, err := sealSiteLogins(entries)
		if err != nil {
			s.loginError = err.Error()
			s.changed()
			return
		}
		secret = sealed
	}
	s.owner.preferences.SiteLoginsSecret = secret
	if s.owner.mgr != nil {
		s.owner.mgr.SetSiteLogins(siteLoginEngine(entries))
	}
	s.siteLogins = entries
	s.loginError = ""
	s.owner.saveAppPreferences()
	s.changed()
}
