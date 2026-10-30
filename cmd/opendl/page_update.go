package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"openDL/appupdate"
	"openDL/i18n"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type updaterState struct {
	busy                                                      bool
	cancelled                                                 bool
	job                                                       uint64
	cancel                                                    context.CancelFunc
	message                                                   string
	release                                                   *appupdate.Release
	pending                                                   *appupdate.Pending
	installer, installerDir                                   string
	downloaded, total                                         int64
	downloading, showDialog                                   bool
	badgeOpen, badgeInside, badgeDismissed, badgeClosePending bool
}

type updateDraft struct {
	automatic bool
	err       string
}

// Release builds can override the official source with -ldflags.
var officialUpdateURL = "https://api.gitcode.com/api/v5/repos/kindred-wu/openDL/releases/latest"
var officialUpdatePublicKey = "mXwJGLJRAkQS8bd8aNxl93y2hAEv2RT9miR0t/cn9MQ="
var officialUpdateProvider string

func effectiveUpdateConfig(config appupdate.Config) appupdate.Config {
	if strings.TrimSpace(config.URL) == "" && strings.TrimSpace(config.PublicKey) == "" {
		config.URL = strings.TrimSpace(officialUpdateURL)
		config.PublicKey = strings.TrimSpace(officialUpdatePublicKey)
		config.Provider = strings.TrimSpace(officialUpdateProvider)
		if config.Provider == "" && strings.HasPrefix(config.URL, "https://api.gitcode.com/api/v5/repos/") && strings.HasSuffix(config.URL, "/releases/latest") {
			config.Provider = "gitcode"
		}
	}
	return config
}

func updateSourceAvailable(config appupdate.Config) bool {
	config = effectiveUpdateConfig(config)
	return config.URL != "" && config.Validate() == nil
}

func runUpdateHelper() bool {
	if len(os.Args) < 2 || os.Args[1] != "--apply-update" {
		return false
	}
	if len(os.Args) != 3 {
		log.Print("update: missing plan")
		return true
	}
	i18n.Set(loadAppPreferences().Language)
	if err := appupdate.Apply(context.Background(), os.Args[2], func() appupdate.Config { return effectiveUpdateConfig(loadAppPreferences().Updates) }, d1AppVersion()); err != nil {
		log.Printf("update: %v", err)
	}
	return true
}

func acknowledgeUpdate() {
	if len(os.Args) == 3 && os.Args[1] == "--update-health" {
		if err := appupdate.Acknowledge(os.Args[2], d1AppVersion()); err != nil {
			log.Printf("update health: %v", err)
		}
	}
}

// Only a fixed stage code crosses the process boundary, never raw errors.
func updateFailureMessage(args []string) string {
	if len(args) != 3 || args[1] != "--update-failed" {
		return ""
	}
	switch args[2] {
	case "precheck":
		return tr("更新验证失败，已重新打开原版本；请重新检查更新设置。")
	case "install":
		return tr("更新安装失败，已重新打开原版本；请检查安装目录权限和文件占用。")
	case "startup":
		return tr("新版本未能启动，已恢复并重新打开原版本。")
	default:
		return ""
	}
}

func (pg *mainPage) updateChanged() {
	if pg.updater.release != nil && !pg.updater.busy && pg.updater.message != "" && pg.updater.message != fmt.Sprintf(tr("发现新版本 %s"), appVersionLabel(pg.updater.release.Manifest.Version)) {
		pg.flash(pg.updater.message)
	}
	if pg.win != nil {
		pg.win.Invalidate()
	}
	if s := pg.settingsWindow; s != nil && s.win != nil {
		s.win.Invalidate()
	}
}

// updateBadge uses the signed manifest already checked by the updater.
func (pg *mainPage) updateBadge(c *ui.Context, p *d1Palette) {
	release := pg.updater.release
	if release == nil {
		pg.updater.badgeOpen = false
		pg.updater.badgeInside = false
		return
	}
	label := tr("有更新")
	if pg.updater.busy {
		label = tr("更新中")
	}
	version := appVersionLabel(release.Manifest.Version)
	notes := strings.TrimSpace(release.Manifest.Notes)
	if notes == "" {
		notes = tr("暂无更新说明")
	}
	description := fmt.Sprintf(tr("发现新版本 %s"), version) + "\n" + notes
	if pg.updater.message != "" {
		description += "\n" + pg.updater.message
	}
	c.Key("update-badge")
	button := ui.ButtonBase(c).Height(24).Padding(0, 8).Gap(4).AlignItems(ui.Center).
		Radius(style.RadiusSM).Background(p.sel).Label(label).Description(description)
	hoverMotion(button, p.sel, p.hover)
	button.Children(func() {
		style.Icon(c, style.IconArrowUp, 14, p.pri)
		ui.Text(c, label).FontSize(style.TextXS).FontWeight(style.WeightMedium).TextColor(p.pri).SingleLine()
	})
	anchorInside := button.Hovered() || button.FocusVisible()
	if anchorInside && !pg.updater.badgeDismissed && !pg.updater.busy {
		pg.updater.badgeOpen = true
	}
	panel := ui.PopoverBase(c, button, &pg.updater.badgeOpen, func(tip ui.Element) {
		tip.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).
			Width(320).Padding(12).Radius(style.RadiusSM).Border(1, p.bd).Background(p.card).
			Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, .2)).Children(func() {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, fmt.Sprintf(tr("发现新版本 %s"), version)).FontSize(style.TextBase).FontWeight(style.WeightMedium).TextColor(p.fg)
				ui.Scroll(c).FillWidth().MaxHeight(220).Children(func() {
					ui.Text(c, notes).FontSize(style.TextSM).TextColor(p.mu)
				})
				if pg.updater.message != "" && pg.updater.message != fmt.Sprintf(tr("发现新版本 %s"), version) {
					ui.Text(c, pg.updater.message).FontSize(style.TextSM).TextColor(p.mu)
				}
				c.Key("update-popover-install")
				d1PrimaryButton(c, p, "下载更新", "down", !pg.updater.busy).OnClick(func() { pg.updater.showDialog = true; pg.updater.badgeOpen = false; pg.installUpdate() })
			})
		})
	})
	panelInside := panel.Valid() && (panel.Hovered() || panel.FocusWithin())
	pg.updater.badgeInside = anchorInside || panelInside
	if panel.Valid() && panel.Dismissed() {
		pg.updater.badgeDismissed = true
	}
	if !pg.updater.badgeInside {
		pg.updater.badgeDismissed = false
		if pg.updater.badgeOpen && !pg.updater.badgeClosePending && pg.win != nil {
			pg.updater.badgeClosePending = true
			win := pg.win
			time.AfterFunc(300*time.Millisecond, func() {
				select {
				case <-pg.stop:
					return
				default:
				}
				win.Update(func() {
					select {
					case <-pg.stop:
						return
					default:
					}
					pg.updater.badgeClosePending = false
					if !pg.updater.badgeInside {
						pg.updater.badgeOpen = false
						win.Invalidate()
					}
				})
			})
		}
	}
	button.OnClick(func() {
		pg.updater.badgeOpen = false
		pg.updater.badgeDismissed = true
		pg.updater.showDialog = true
		pg.installUpdate()
	})
}

func (pg *mainPage) checkForUpdates(quiet bool) {
	if pg.updater.busy || pg.win == nil {
		return
	}
	pg.discardStagedUpdate()
	config := effectiveUpdateConfig(pg.preferences.Updates)
	if pg.mgr != nil {
		config.Transport = pg.mgr.UpdateTransport()
	}
	if err := config.Validate(); err != nil || config.URL == "" {
		if !quiet {
			pg.updater.message = tr("此版本暂不支持检查更新")
			pg.updateChanged()
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pg.updater.busy = true
	pg.updater.cancelled = false
	pg.updater.cancel = cancel
	pg.updater.job++
	job := pg.updater.job
	pg.updater.message = tr("正在检查更新…")
	pg.updateChanged()
	go func() {
		release, err := appupdate.Check(ctx, config, d1AppVersion())
		cancel()
		pg.win.Update(func() {
			select {
			case <-pg.stop:
				return
			default:
			}
			if pg.updater.job != job {
				return
			}
			pg.updater.busy = false
			pg.updater.cancel = nil
			if pg.updater.cancelled {
				err = i18n.NewError("更新已取消")
			}
			if err != nil {
				pg.updater.release = nil
				pg.updater.message = err.Error()
			} else {
				pg.preferences.Updates.LastChecked = time.Now()
				pg.saveAppPreferences()
				pg.updater.release = release
				pg.updater.message = tr("当前已是最新版本")
				if release != nil {
					pg.updater.message = fmt.Sprintf(tr("发现新版本 %s"), appVersionLabel(release.Manifest.Version))
					pg.flash(pg.updater.message)
				}
			}
			pg.updateChanged()
		})
	}()
}

// installUpdate downloads and verifies the release without starting installation.
func (pg *mainPage) installUpdate() {
	if pg.updater.busy || pg.updater.release == nil || pg.win == nil || pg.updater.pending != nil || pg.updater.installer != "" {
		return
	}
	if mygo.IsDev() {
		pg.updater.message = tr("开发模式不安装更新，请使用打包后的应用")
		pg.updateChanged()
		return
	}
	config, release, current := effectiveUpdateConfig(pg.preferences.Updates), pg.updater.release, d1AppVersion()
	if pg.mgr != nil {
		config.Transport = pg.mgr.UpdateTransport()
	}
	exe, err := os.Executable()
	if err != nil {
		pg.updater.message = err.Error()
		pg.updateChanged()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pg.updater.busy, pg.updater.downloading, pg.updater.cancelled = true, true, false
	pg.updater.cancel = cancel
	pg.updater.job++
	job, win := pg.updater.job, pg.win
	pg.updater.downloaded, pg.updater.total = 0, release.Asset.Size
	pg.updater.message = tr("正在下载并验证更新…")
	pg.updateChanged()
	var last time.Time
	config.Progress = func(done, total int64) {
		if done < total && time.Since(last) < 150*time.Millisecond {
			return
		}
		last = time.Now()
		win.Update(func() {
			select {
			case <-pg.stop:
				return
			default:
			}
			if pg.updater.job == job && pg.updater.downloading {
				pg.updater.downloaded, pg.updater.total = done, total
				pg.updateChanged()
			}
		})
	}
	go func() {
		var pending *appupdate.Pending
		var directory, installer string
		kind, err := appupdate.AssetKind(release.Asset)
		if err == nil && kind == "installer" {
			directory, err = os.MkdirTemp("", "OpenDL-installer-")
			if err == nil {
				installer, err = appupdate.DownloadInstaller(ctx, config, release, current, directory)
			}
		} else if err == nil {
			pending, err = appupdate.Stage(ctx, config, release, current, exe)
		}
		cancel()
		cleanup := func() {
			if pending != nil {
				pending.Cleanup()
			}
			if directory != "" {
				_ = os.RemoveAll(directory)
			}
		}
		win.Update(func() {
			select {
			case <-pg.stop:
				cleanup()
				return
			default:
			}
			if pg.updater.job != job {
				cleanup()
				return
			}
			pg.updater.busy, pg.updater.downloading, pg.updater.cancel = false, false, nil
			if pg.updater.cancelled {
				cleanup()
				pg.updater.message = tr("更新已取消")
			} else if err != nil {
				cleanup()
				pg.updater.message = err.Error()
			} else {
				pg.updater.pending, pg.updater.installer, pg.updater.installerDir = pending, installer, directory
				pg.updater.message = tr("更新已下载，准备重启安装")
			}
			pg.updateChanged()
		})
	}()
}

func (pg *mainPage) restartForUpdate() {
	if pg.updater.busy || pg.win == nil || (pg.updater.pending == nil && pg.updater.installer == "") {
		return
	}
	if pg.hasActive() || (pg.mgr != nil && pg.mgr.HasMaintenanceWork()) || (pg.mgr != nil && !pg.mgr.PrepareForUpdate()) {
		pg.updater.message = tr("请等待当前下载、校验或同步结束后再安装更新")
		pg.updateChanged()
		return
	}
	if pg.updater.pending != nil {
		if err := pg.updater.pending.Start(); err != nil {
			if pg.mgr != nil {
				pg.mgr.AbortUpdate()
			}
			pg.updater.message = err.Error()
			pg.updateChanged()
			return
		}
		pg.updater.pending = nil
		pg.quitting = true
		pg.saveAppPreferences()
		mygo.App.Quit()
		return
	}
	config, release, current, installer, win := effectiveUpdateConfig(pg.preferences.Updates), pg.updater.release, d1AppVersion(), pg.updater.installer, pg.win
	pg.updater.busy = true
	pg.updater.message = tr("正在验证更新…")
	pg.updateChanged()
	go func() {
		err := appupdate.VerifyInstaller(config, release, current, installer)
		win.Update(func() {
			select {
			case <-pg.stop:
				if pg.mgr != nil {
					pg.mgr.AbortUpdate()
				}
				return
			default:
			}
			pg.updater.busy = false
			if err == nil {
				err = appupdate.LaunchInstaller(installer)
			}
			if err != nil {
				if pg.mgr != nil {
					pg.mgr.AbortUpdate()
				}
				pg.updater.message = err.Error()
				pg.updateChanged()
				return
			}
			pg.updater.installer, pg.updater.installerDir = "", ""
			pg.quitting = true
			pg.saveAppPreferences()
			mygo.App.Quit()
		})
	}()
}

func (pg *mainPage) discardStagedUpdate() {
	if pg.updater.pending != nil {
		pg.updater.pending.Cleanup()
		pg.updater.pending = nil
	}
	if pg.updater.installerDir != "" {
		_ = os.RemoveAll(pg.updater.installerDir)
		pg.updater.installerDir, pg.updater.installer = "", ""
	}
}

func (pg *mainPage) watchUpdates() {
	if pg.win == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		check := func(startup bool) {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
					return
				default:
				}
				if pg.preferences.Updates.Automatic && (startup || time.Since(pg.preferences.Updates.LastChecked) >= 24*time.Hour) {
					pg.checkForUpdates(true)
				}
			})
		}
		check(true)
		for {
			select {
			case <-pg.stop:
				// App cancellation is dispatched on the UI thread via OnClosed.
				return
			case <-ticker.C:
				check(false)
			}
		}
	}()
	pg.win.OnClosed(func() {
		if pg.updater.cancel != nil {
			pg.updater.cancel()
		}
		pg.discardStagedUpdate()
	})
}

func (s *settingsPage) syncUpdateDraft() {
	config := s.owner.preferences.Updates
	s.updateDraft.automatic = config.Automatic
}

// The automatic-check switch is independent of an unfinished URL/key draft.
// Only the currently active, validated source can be scheduled.
func (s *settingsPage) applyUpdateAutomatic() {
	config := s.owner.preferences.Updates
	if s.updateDraft.automatic {
		config.Automatic = true
		if !updateSourceAvailable(config) {
			s.updateDraft.automatic = false
			s.updateDraft.err = tr("此版本暂不支持检查更新")
			s.changed()
			return
		}
	} else {
		config.Automatic = false
	}
	s.owner.preferences.Updates = config
	s.owner.saveAppPreferences()
	s.updateDraft.err = ""
	s.changed()
	if config.Automatic && time.Since(config.LastChecked) >= 24*time.Hour {
		s.owner.checkForUpdates(true)
	}
}

func (pg *mainPage) updateProgress(c *ui.Context, p *d1Palette) {
	total, done := pg.updater.total, pg.updater.downloaded
	fraction := float32(0)
	if total > 0 {
		fraction = min(float32(1), max(float32(0), float32(done)/float32(total)))
	}
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Children(func() {
			ui.Text(c, tr("正在下载更新")).FontSize(style.TextSM).TextColor(p.mu)
			ui.Text(c, fmt.Sprintf("%d%%", int(fraction*100))).FontSize(style.TextSM).TextColor(p.fg)
		})
		ui.Box(c).FillWidth().Height(8).Role(ui.RoleProgress).Label(tr("正在下载更新")).Range(0, 1, float64(fraction)).Draw(func(painter *ui.Painter, bounds ui.Rect) {
			painter.Fill(bounds, p.bd, 4)
			if fraction > 0 {
				painter.Fill(ui.Rect{X: bounds.X, Y: bounds.Y, W: bounds.W * fraction, H: bounds.H}, p.pri, 4)
			}
		})
	})
}

func (pg *mainPage) updateDialog(c *ui.Context, p *d1Palette) {
	ui.DialogBase(c, &pg.updater.showDialog, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, .42))
		panel.Width(420).Padding(18).Radius(style.RadiusSM).Border(1, p.bd).Background(p.bg).Label(tr("应用更新"))
		ui.Column(c).FillWidth().Gap(12).Children(func() {
			ui.Text(c, tr("应用更新")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
			if pg.updater.message != "" {
				ui.Text(c, pg.updater.message).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.mu)
			}
			if pg.updater.downloading {
				pg.updateProgress(c, p)
			}
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				if pg.updater.downloading {
					d1Button(c, p, "取消", "").OnClick(func() {
						pg.updater.cancelled = true
						if pg.updater.cancel != nil {
							pg.updater.cancel()
						}
					})
				} else {
					d1Button(c, p, "关闭", "").OnClick(func() { pg.updater.showDialog = false })
				}
				if pg.updater.pending != nil || pg.updater.installer != "" {
					d1PrimaryButton(c, p, "重启安装", "", !pg.updater.busy).OnClick(pg.restartForUpdate)
				} else if !pg.updater.busy && pg.updater.release != nil {
					d1PrimaryButton(c, p, "下载更新", "down").OnClick(pg.installUpdate)
				}
			})
		})
	})
}

func (s *settingsPage) updateFields(c *ui.Context, p *d1Palette) {
	pg := s.owner
	available := updateSourceAvailable(pg.preferences.Updates)
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Disabled(pg.updater.busy || !available).Children(func() {
			ui.Text(c, tr("自动检查更新")).Grow(1).Shrink(1).Basis(0).MinWidth(0).FontSize(style.TextBase).TextColor(p.fg).SingleLine()
			settingsSwitch(c, p, "自动检查更新", &s.updateDraft.automatic).OnChange(s.applyUpdateAutomatic)
		})
		if !available {
			ui.Text(c, tr("此版本暂不支持检查更新")).FontSize(style.TextSM).TextColor(p.mu)
		}
		settingsError(c, p, s.updateDraft.err)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			d1Button(c, p, "立即检查", "", !pg.updater.busy && available).OnClick(func() { pg.checkForUpdates(false) })
			if pg.updater.release != nil && pg.updater.pending == nil && pg.updater.installer == "" {
				d1PrimaryButton(c, p, "安装更新", "", !pg.updater.busy && available).OnClick(pg.installUpdate)
			}
			if pg.updater.pending != nil || pg.updater.installer != "" {
				d1PrimaryButton(c, p, "重启安装", "", !pg.updater.busy).OnClick(pg.restartForUpdate)
			}
			if pg.updater.busy && pg.updater.cancel != nil {
				d1Button(c, p, "取消", "").OnClick(func() {
					pg.updater.cancelled = true
					if pg.updater.cancel != nil {
						pg.updater.cancel()
					}
				})
			}
		})
		if pg.updater.downloading {
			pg.updateProgress(c, p)
		}
		if pg.updater.message != "" {
			ui.Text(c, pg.updater.message).FontSize(style.TextSM).TextColor(p.mu)
		}
		if release := pg.updater.release; release != nil {
			ui.Text(c, appVersionLabel(release.Manifest.Version)).FontSize(style.TextBase).TextColor(p.fg)
			if release.Manifest.Notes != "" {
				ui.Text(c, release.Manifest.Notes).FontSize(style.TextSM).TextColor(p.mu)
			}
		}
	})
}
