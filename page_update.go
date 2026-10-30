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
	busy      bool
	cancelled bool
	job       uint64
	cancel    context.CancelFunc
	message   string
	release   *appupdate.Release
}

type updateDraft struct {
	automatic bool
	err       string
}

// Release builds inject both values with -ldflags. Empty values leave legacy
// configured installations working until an official source is published.
var officialUpdateURL string
var officialUpdatePublicKey string

func effectiveUpdateConfig(config appupdate.Config) appupdate.Config {
	if officialUpdateURL != "" || officialUpdatePublicKey != "" {
		config.URL = strings.TrimSpace(officialUpdateURL)
		config.PublicKey = strings.TrimSpace(officialUpdatePublicKey)
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
	if pg.win != nil {
		pg.win.Invalidate()
	}
	if s := pg.settingsWindow; s != nil && s.win != nil {
		s.win.Invalidate()
	}
}

func (pg *mainPage) checkForUpdates(quiet bool) {
	if pg.updater.busy || pg.win == nil {
		return
	}
	config := effectiveUpdateConfig(pg.preferences.Updates)
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

func (pg *mainPage) installUpdate() {
	if pg.updater.busy || pg.updater.release == nil || pg.win == nil {
		return
	}
	if mygo.IsDev() {
		pg.updater.message = tr("开发模式不安装更新，请使用打包后的应用")
		pg.updateChanged()
		return
	}
	if pg.hasActive() || (pg.mgr != nil && pg.mgr.HasMaintenanceWork()) {
		pg.updater.message = tr("请等待当前下载、校验或同步结束后再安装更新")
		pg.updateChanged()
		return
	}
	config, release, current := effectiveUpdateConfig(pg.preferences.Updates), pg.updater.release, d1AppVersion()
	exe, err := os.Executable()
	if err != nil {
		pg.updater.message = err.Error()
		pg.updateChanged()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pg.updater.busy = true
	pg.updater.cancelled = false
	pg.updater.cancel = cancel
	pg.updater.job++
	job := pg.updater.job
	pg.updater.message = tr("正在下载并验证更新…")
	pg.updateChanged()
	if kind, kindErr := appupdate.AssetKind(release.Asset); kindErr == nil && kind == "installer" {
		pg.installWithInstaller(ctx, config, release, current, job, cancel)
		return
	}
	go func() {
		pending, err := appupdate.Stage(ctx, config, release, current, exe)
		cancel()
		pg.win.Update(func() {
			select {
			case <-pg.stop:
				if pending != nil {
					pending.Cleanup()
				}
				return
			default:
			}
			if job != pg.updater.job {
				if pending != nil {
					pending.Cleanup()
				}
				return
			}
			pg.updater.busy = false
			pg.updater.cancel = nil
			if pg.updater.cancelled {
				if pending != nil {
					pending.Cleanup()
				}
				pg.updater.message = tr("更新已取消")
				pg.updateChanged()
				return
			}
			if err != nil {
				pg.updater.message = err.Error()
				pg.updateChanged()
				return
			}
			// The user may have started another task during the package download.
			if pg.hasActive() || (pg.mgr != nil && pg.mgr.HasMaintenanceWork()) {
				pending.Cleanup()
				pg.updater.message = tr("请等待当前下载、校验或同步结束后再安装更新")
				pg.updateChanged()
				return
			}
			if pg.mgr != nil && !pg.mgr.PrepareForUpdate() {
				pending.Cleanup()
				pg.updater.message = tr("请等待当前下载、校验或同步结束后再安装更新")
				pg.updateChanged()
				return
			}
			if err := pending.Start(); err != nil {
				if pg.mgr != nil {
					pg.mgr.AbortUpdate()
				}
				pending.Cleanup()
				pg.updater.message = err.Error()
				pg.updateChanged()
				return
			}
			pg.quitting = true
			pg.saveAppPreferences()
			mygo.App.Quit()
		})
	}()
}

// installWithInstaller 下载并启动安装器升级包；替换、提权与回退由安装器完
// 成，应用在成功启动安装器后退出，暂存目录留给安装器。
func (pg *mainPage) installWithInstaller(ctx context.Context, config appupdate.Config, release *appupdate.Release, current string, job uint64, cancel context.CancelFunc) {
	go func() {
		directory, err := os.MkdirTemp("", "OpenDL-installer-")
		var installer string
		if err == nil {
			installer, err = appupdate.DownloadInstaller(ctx, config, release, current, directory)
		}
		cancel()
		pg.win.Update(func() {
			select {
			case <-pg.stop:
				_ = os.RemoveAll(directory)
				return
			default:
			}
			if job != pg.updater.job {
				_ = os.RemoveAll(directory)
				return
			}
			pg.updater.busy = false
			pg.updater.cancel = nil
			if pg.updater.cancelled {
				_ = os.RemoveAll(directory)
				pg.updater.message = tr("更新已取消")
				pg.updateChanged()
				return
			}
			if err != nil {
				_ = os.RemoveAll(directory)
				pg.updater.message = err.Error()
				pg.updateChanged()
				return
			}
			// The user may have started another task during the package download.
			if pg.hasActive() || (pg.mgr != nil && pg.mgr.HasMaintenanceWork()) {
				_ = os.RemoveAll(directory)
				pg.updater.message = tr("请等待当前下载、校验或同步结束后再安装更新")
				pg.updateChanged()
				return
			}
			if err := appupdate.LaunchInstaller(installer); err != nil {
				_ = os.RemoveAll(directory)
				pg.updater.message = err.Error()
				pg.updateChanged()
				return
			}
			pg.quitting = true
			pg.saveAppPreferences()
			mygo.App.Quit()
		})
	}()
}

func (pg *mainPage) watchUpdates() {
	if pg.win == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		check := func() {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
					return
				default:
				}
				if pg.preferences.Updates.Automatic && time.Since(pg.preferences.Updates.LastChecked) >= 24*time.Hour {
					pg.checkForUpdates(true)
				}
			})
		}
		check()
		for {
			select {
			case <-pg.stop:
				// App cancellation is dispatched on the UI thread via OnClosed.
				return
			case <-ticker.C:
				check()
			}
		}
	}()
	pg.win.OnClosed(func() {
		if pg.updater.cancel != nil {
			pg.updater.cancel()
		}
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
			if pg.updater.release != nil {
				d1PrimaryButton(c, p, "安装并重启", "", !pg.updater.busy && available).OnClick(pg.installUpdate)
			}
			if pg.updater.busy {
				d1Button(c, p, "取消", "").OnClick(func() {
					pg.updater.cancelled = true
					if pg.updater.cancel != nil {
						pg.updater.cancel()
					}
				})
			}
		})
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
