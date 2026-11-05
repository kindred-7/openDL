package main

import (
	"openDL/download"

	"github.com/egoist/mygo/ui"
)

func (s *settingsPage) syncUserAgent() {
	s.userAgentDraft = s.owner.preferences.UserAgent
	if s.userAgentDraft == "" {
		s.userAgentDraft = download.DefaultUserAgent
	}
}

func (s *settingsPage) applyUserAgent() {
	value, err := download.NormalizeUserAgent(s.userAgentDraft)
	if err != nil {
		s.userAgentError = err.Error()
		s.changed()
		return
	}
	// Keep the built-in choice implicit so future application updates can
	// maintain it without overwriting a custom user agent.
	if value == download.DefaultUserAgent {
		value = ""
	}
	if err := s.owner.mgr.SetUserAgent(value); err != nil {
		s.userAgentError = err.Error()
		s.changed()
		return
	}
	s.owner.preferences.UserAgent = value
	s.owner.saveAppPreferences()
	s.userAgentDirty, s.userAgentError = false, ""
	s.syncUserAgent()
	s.changed()
}

func (s *settingsPage) restoreDefaultUserAgent() {
	s.userAgentDraft = download.DefaultUserAgent
	s.applyUserAgent()
}

func (s *settingsPage) userAgentFields(c *ui.Context, p *d1Palette) {
	const buttonWidth = 54
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		settingsInput(c, p, "默认用户代理（UA）", "settings-user-agent", &s.userAgentDraft, "留空使用内置默认 UA").
			Grow(1).Shrink(1).Basis(0).MinWidth(0).OnChange(func() {
				s.userAgentDirty, s.userAgentError = true, ""
				s.changed()
			}).OnSubmit(s.applyUserAgent)
		d1Button(c, p, "应用", "").Width(buttonWidth).Height(settingsInputHeight).Shrink(0).Padding(0, 6).
			Disabled(!s.userAgentDirty).OnClick(s.applyUserAgent)
		restorePalette := *p
		restorePalette.bg, restorePalette.fg, restorePalette.bd = p.fg, p.bg, p.fg
		restorePalette.hover = p.fg.Mix(p.bg, 0.12)
		d1Button(c, &restorePalette, "恢复", "").Width(buttonWidth).Height(settingsInputHeight).Shrink(0).Padding(0, 6).
			Label(tr("恢复默认")).Tooltip(tr("恢复默认")).OnClick(s.restoreDefaultUserAgent)
	})
	settingsError(c, p, s.userAgentError)
}
