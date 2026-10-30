//go:build !windows

package appupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func hideHelper(cmd *exec.Cmd) {}

// runInstaller 启动安装包；提权由安装包自身处理。
func runInstaller(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	return cmd.Start()
}

func waitParent(ctx context.Context, pid int) error {
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
