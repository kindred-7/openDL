package appupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"openDL/i18n"
)

type Pending struct {
	Target         string   `json:"target"`
	Directory      string   `json:"directory"`
	Envelope       Envelope `json:"envelope"`
	ParentPID      int      `json:"parent_pid"`
	OriginalSHA256 string   `json:"original_sha256"`
}

func (p *Pending) candidate() string { return filepath.Join(p.Directory, "candidate") }
func (p *Pending) helper() string    { return filepath.Join(p.Directory, "updater.exe") }
func (p *Pending) plan() string      { return filepath.Join(p.Directory, "plan.json") }
func (p *Pending) health() string    { return filepath.Join(p.Directory, "healthy") }
func (p *Pending) backup() string    { return filepath.Join(p.Directory, "previous") }

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *Pending) Cleanup() {
	// Only known files in our own staging directory are removed, never recursive.
	for _, path := range []string{p.candidate(), p.helper(), p.plan(), p.health()} {
		_ = os.Remove(path)
	}
	_ = os.Remove(p.Directory)
}

// Start launches the old, trusted executable as the helper. It does not execute
// the downloaded candidate before the signed envelope and bytes are rechecked.
func (p *Pending) Start() error {
	cmd := exec.Command(p.helper(), "--apply-update", p.plan())
	hideHelper(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Once Start succeeds the helper may already be waiting for this process.
	// A handle-release error must not be reported as a launch failure: callers
	// would unfreeze downloads and remove the plan while the helper is alive.
	_ = cmd.Process.Release()
	return nil
}

// LaunchInstaller 启动已校验的安装器升级包；替换与回退由安装器完成。
func LaunchInstaller(path string) error { return runInstaller(path) }

// DownloadInstaller 下载并校验安装器升级包，返回暂存路径。安装器自行完成
// 替换与回退；应用在成功启动安装器后退出。
func DownloadInstaller(ctx context.Context, config Config, release *Release, current, directory string) (string, error) {
	if release == nil {
		return "", i18n.NewError("没有可安装的更新")
	}
	verified, err := Decode(config, release.Envelope, time.Now())
	if err != nil {
		return "", err
	}
	kind, err := AssetKind(verified.Asset)
	if err != nil {
		return "", err
	}
	if kind != "installer" {
		return "", i18n.NewError("当前更新不是安装器升级包")
	}
	newer, err := Newer(verified.Manifest.Version, current)
	if err != nil {
		return "", err
	}
	if !newer {
		return "", i18n.NewError("拒绝安装旧版本或相同版本")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	suffix := ""
	if u, err := url.Parse(verified.Asset.URL); err == nil {
		if ext := filepath.Ext(u.Path); len(ext) <= 16 && ext != "" {
			suffix = ext
		}
	}
	path := filepath.Join(directory, "opendl-installer"+suffix)
	resp, err := get(ctx, verified.Asset.URL, config.Transport)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(resp.Body, verified.Asset.Size+1), total: verified.Asset.Size, report: config.Progress})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if n != verified.Asset.Size {
		return "", i18n.NewError("更新文件大小与签名清单不一致")
	}
	if err = verifyArtifact(path, verified.Asset); err != nil {
		return "", err
	}
	return path, nil
}

// VerifyInstaller revalidates the signed release and staged file before launch.
func VerifyInstaller(config Config, release *Release, current, path string) error {
	if release == nil {
		return i18n.NewError("没有可安装的更新")
	}
	verified, err := Decode(config, release.Envelope, time.Now())
	if err != nil {
		return err
	}
	kind, err := AssetKind(verified.Asset)
	if err != nil {
		return err
	}
	if kind != "installer" {
		return i18n.NewError("当前更新不是安装器升级包")
	}
	newer, err := Newer(verified.Manifest.Version, current)
	if err != nil {
		return err
	}
	if !newer {
		return i18n.NewError("拒绝安装旧版本或相同版本")
	}
	return verifyArtifact(path, verified.Asset)
}

func loadPending(path string) (*Pending, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, i18n.NewError("更新安装计划无效")
	}
	var p Pending
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Target) || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != filepath.Dir(abs) || filepath.Base(abs) != "plan.json" || !strings.HasPrefix(filepath.Base(p.Directory), ".OpenDL-update-") || filepath.Dir(p.Directory) != filepath.Dir(p.Target) || p.ParentPID <= 0 {
		return nil, i18n.NewError("更新安装计划无效")
	}
	if info, err := os.Lstat(p.Directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, i18n.NewError("更新暂存目录无效")
	}
	if info, err := os.Lstat(p.Target); err != nil || !info.Mode().IsRegular() {
		return nil, i18n.NewError("更新目标不是普通可执行文件")
	}
	return &p, nil
}

// Apply runs only in the helper process. Failure before replacement keeps the
// original intact; failed startup restores it and reopens the previous version.
func Apply(ctx context.Context, path string, loadConfig func() Config, current string) error {
	p, err := loadPending(path)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err = waitParent(waitCtx, p.ParentPID); err != nil {
		return err
	}
	// The parent flushes preferences before exit, including a newly pinned key.
	// A failure at this stage reopens the untouched original executable.
	reopen := func(stage string, cause error) error {
		if err := exec.Command(p.Target, "--update-failed", stage).Start(); err != nil {
			return fmt.Errorf(i18n.T("更新失败：%v；重启旧版本失败：%w"), cause, err)
		}
		return cause
	}
	originalHash, err := fileDigest(p.Target)
	if err != nil {
		return err
	}
	if originalHash != p.OriginalSHA256 {
		return i18n.NewError("已安装文件发生变化，取消更新")
	}
	release, err := Decode(loadConfig(), p.Envelope, time.Now())
	if err != nil {
		return reopen("precheck", err)
	}
	newer, err := Newer(release.Manifest.Version, current)
	if err != nil {
		return reopen("precheck", err)
	}
	if !newer {
		return reopen("precheck", i18n.NewError("拒绝安装旧版本或相同版本"))
	}
	// Reopen and recheck immediately before publication, after parent shutdown.
	if err = verifyArtifact(p.candidate(), release.Asset); err != nil {
		return reopen("precheck", err)
	}
	if err = os.Rename(p.Target, p.backup()); err != nil {
		return reopen("install", err)
	}
	if err = os.Rename(p.candidate(), p.Target); err != nil {
		if restoreErr := os.Rename(p.backup(), p.Target); restoreErr != nil {
			return fmt.Errorf(i18n.T("安装更新失败：%v；恢复旧版本失败：%w"), err, restoreErr)
		}
		return reopen("install", err)
	}
	_ = os.Remove(p.health())
	cmd := exec.Command(p.Target, "--update-health", p.health())
	startErr := cmd.Start()
	if startErr == nil {
		exited := make(chan error, 1)
		didExit := false
		go func() { exited <- cmd.Wait() }()
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for startErr == nil {
			select {
			case <-ctx.Done():
				startErr = ctx.Err()
			case err := <-exited:
				didExit = true
				startErr = fmt.Errorf(i18n.T("更新后的应用在启动确认前退出：%v"), err)
			case <-timer.C:
				startErr = i18n.NewError("更新后的应用启动确认超时")
			case <-ticker.C:
				ack, readErr := os.ReadFile(p.health())
				ackVersion, ackErr := parseSemVer(string(ack))
				wantVersion, _ := parseSemVer(release.Manifest.Version)
				if readErr == nil && ackErr == nil && ackVersion.compare(wantVersion) == 0 {
					_ = os.Remove(p.backup())
					p.Cleanup()
					return nil
				}
			}
		}
		if !didExit {
			_ = cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				return i18n.NewError("更新进程未退出，旧版本保留在暂存目录")
			}
		}
	}
	// Preserve the failed candidate for diagnosis; no user download is touched.
	if err = os.Rename(p.Target, p.candidate()); err != nil {
		return fmt.Errorf(i18n.T("更新后启动失败：%v；保留候选文件失败：%w"), startErr, err)
	}
	if err = os.Rename(p.backup(), p.Target); err != nil {
		return fmt.Errorf(i18n.T("更新后启动失败：%v；恢复旧版本失败：%w"), startErr, err)
	}
	return reopen("startup", startErr)
}

// Acknowledge writes only a health marker inside a valid sibling staging folder.
// The new executable calls it after its native window has initialized.
func Acknowledge(path, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if filepath.Base(abs) != "healthy" || !strings.HasPrefix(filepath.Base(dir), ".OpenDL-update-") || filepath.Dir(dir) != filepath.Dir(exe) {
		return i18n.NewError("更新启动确认路径无效")
	}
	return os.WriteFile(abs, []byte(version), 0600)
}
