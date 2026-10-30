package main

import (
	"fmt"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo/ui"
)

// Called before the local API can accept tasks. Invalid manual settings never
// result in an implicit environment/direct connection.
func initializeDownloadProxy(manager *download.Manager, preferences appPreferences) (string, error) {
	config, err := download.NormalizeProxyConfig(preferences.Proxy)
	if err != nil {
		manager.BlockProxy(tr("代理配置无效，请在设置中修正：") + err.Error())
		return "", err
	}
	var credentialError error
	if preferences.ProxySecret != "" {
		config.Password, credentialError = openProxyPassword(config, preferences.ProxySecret)
	}
	if credentialError != nil && config.Mode == "manual" {
		manager.BlockProxy(credentialError.Error())
		return "", credentialError
	}
	if err := manager.SetProxyConfig(config); err != nil {
		manager.BlockProxy(err.Error())
		return "", err
	}
	return config.Password, credentialError
}

func (s *settingsPage) syncProxy() {
	config := s.owner.preferences.Proxy
	s.proxyMode = config.Mode
	if s.proxyMode == "" {
		s.proxyMode = "environment"
	}
	s.proxyAddress, s.proxyBypass, s.proxyUser = config.Address, config.Bypass, config.Username
	s.proxyHTTPAddress, s.proxyHTTPSAddress = config.HTTPAddress, config.HTTPSAddress
	s.proxyIntegrated = config.IntegratedAuth
	s.proxyPassword = s.owner.proxyPassword
	s.proxyRemember = s.owner.preferences.ProxySecret != ""
	s.proxyError = s.owner.proxyError
}

func (s *settingsPage) proxyEdited() {
	s.proxyDirty = true
	s.proxyError = ""
	s.proxyRevision++
	s.applyProxy()
}

func (s *settingsPage) proxyFields(c *ui.Context, p *d1Palette) {
	ui.Column(c).FillWidth().Gap(12).Children(func() {
		if s.proxyMode == "system" && s.owner.mgr != nil {
			if note := s.owner.mgr.ProxyNote(); note != "" {
				ui.Text(c, tr(note)).FontSize(style.TextSM).TextColor(p.mu)
			}
		}
		if s.proxyMode == "manual" {
			ui.Column(c).FillWidth().Gap(5).Children(func() {
				ui.Text(c, tr("代理地址")).FontSize(style.TextSM).TextColor(p.fg)
				settingsInput(c, p, "代理地址", "settings-proxy-address", &s.proxyAddress, "http://127.0.0.1:7890").FillWidth().OnChange(s.proxyEdited)
			})
			ui.Row(c).FillWidth().Gap(12).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
					ui.Text(c, tr("HTTP 代理")).FontSize(style.TextSM).TextColor(p.fg)
					settingsInput(c, p, "HTTP 代理", "settings-proxy-http", &s.proxyHTTPAddress, "跟随代理地址").FillWidth().OnChange(s.proxyEdited)
				})
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
					ui.Text(c, tr("HTTPS 代理")).FontSize(style.TextSM).TextColor(p.fg)
					settingsInput(c, p, "HTTPS 代理", "settings-proxy-https", &s.proxyHTTPSAddress, "跟随代理地址").FillWidth().OnChange(s.proxyEdited)
				})
			})
			ui.Row(c).FillWidth().Gap(12).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
					ui.Text(c, tr("用户名")).FontSize(style.TextSM).TextColor(p.fg)
					settingsInput(c, p, "代理用户名", "settings-proxy-username", &s.proxyUser, "").FillWidth().OnChange(s.proxyEdited)
				})
				ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(5).Children(func() {
					ui.Text(c, tr("密码")).FontSize(style.TextSM).TextColor(p.fg)
					c.Key("settings-proxy-password")
					// Password fields must never reuse settingsInput's value tooltip.
					input := ui.TextInputBase(c, &s.proxyPassword).Password().FillWidth().Height(32).
						Padding(0, 10).Radius(style.RadiusSM).Border(1, p.bd2).Background(p.bg).
						TextColor(p.fg).FontSize(style.TextSM).Label(tr("代理密码"))
					if input.Focused() {
						input.BorderColor(p.pri)
					}
					input.OnChange(s.proxyEdited)
				})
			})
			ui.Row(c).Disabled(!proxyPasswordStorageAvailable && !s.proxyRemember).Children(func() {
				c.Key("settings-proxy-remember")
				ui.CheckboxBase(c, &s.proxyRemember).Gap(8).Justify(ui.Start).AlignItems(ui.Center).
					Label(tr("加密记住密码")).Children(func() {
					d1CheckMark(c, p, s.proxyRemember)
					ui.Text(c, tr("加密记住密码")).FontSize(style.TextBase).TextColor(p.fg)
				}).OnChange(s.proxyEdited)
			})
			ui.Row(c).Children(func() {
				c.Key("settings-proxy-integrated")
				ui.CheckboxBase(c, &s.proxyIntegrated).Gap(8).Justify(ui.Start).AlignItems(ui.Center).
					Label(tr("使用系统集成认证")).Children(func() {
					d1CheckMark(c, p, s.proxyIntegrated)
					ui.Text(c, tr("使用系统集成认证")).FontSize(style.TextBase).TextColor(p.fg)
				}).OnChange(s.proxyEdited)
			})
			ui.Column(c).FillWidth().Gap(5).Children(func() {
				ui.Text(c, tr("不使用代理的地址")).FontSize(style.TextSM).TextColor(p.fg)
				settingsInput(c, p, "不使用代理的地址", "settings-proxy-bypass", &s.proxyBypass, "example.com, .internal.test, 192.168.0.0/16").
					FillWidth().OnChange(s.proxyEdited)
			})
		}
		settingsError(c, p, s.proxyError)
	})
}

func (s *settingsPage) applyProxy() {
	if s.proxySaving || s.owner.mgr == nil || s.owner.win == nil {
		return
	}
	config := download.ProxyConfig{Mode: s.proxyMode, Address: s.proxyAddress, HTTPAddress: s.proxyHTTPAddress, HTTPSAddress: s.proxyHTTPSAddress, Bypass: s.proxyBypass, Username: s.proxyUser, Password: s.proxyPassword, IntegratedAuth: s.proxyIntegrated}
	secret := s.owner.preferences.ProxySecret
	remember := s.proxyRemember
	if config.Mode != "manual" {
		// Switching away from manual mode must not require repairing an
		// inactive draft. Keep previously valid fields for switching back.
		config = s.owner.preferences.Proxy
		config.Mode, config.Password = s.proxyMode, s.owner.proxyPassword
		if _, err := download.NormalizeProxyConfig(config); err != nil {
			config = download.ProxyConfig{Mode: s.proxyMode}
			secret = ""
		}
	}
	config, err := download.NormalizeProxyConfig(config)
	if err != nil {
		s.proxyError = err.Error()
		s.changed()
		return
	}
	s.proxySaving = true
	s.proxyError = ""
	s.owner.proxyJob++
	job := s.owner.proxyJob
	revision := s.proxyRevision
	owner, win := s.owner, s.owner.win
	go func() {
		var saveError error
		if config.Mode == "manual" {
			secret = ""
			if remember && config.Username != "" {
				secret, saveError = sealProxyPassword(config)
			}
		}
		select {
		case <-owner.stop:
			return
		default:
		}
		win.Update(func() {
			defer func() {
				if s.win == nil && !s.proxySaving {
					s.proxyPassword = ""
				}
			}()
			select {
			case <-owner.stop:
				return
			default:
			}
			if job != owner.proxyJob {
				s.proxySaving = false
				return
			}
			s.proxySaving = false
			if revision != s.proxyRevision {
				s.applyProxy()
				return
			}
			if saveError == nil {
				saveError = owner.mgr.SetProxyConfig(config)
			}
			if saveError != nil {
				s.proxyError = saveError.Error()
				owner.proxyError = s.proxyError
				s.changed()
				return
			}
			owner.proxyPassword = config.Password
			config.Password = ""
			owner.preferences.Proxy, owner.preferences.ProxySecret = config, secret
			owner.proxyError = ""
			s.proxyDirty = true // Preserve the text being edited until the window closes.
			owner.saveAppPreferences()
			owner.refreshSettingsWindow()
			s.changed()
		})
	}()
}

func proxySettingsStatus(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(tr("代理设置需要处理：%s"), err)
}
