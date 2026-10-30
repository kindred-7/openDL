//go:build windows

package download

import (
	"os/exec"
	"syscall"
)

func hideMediaMux(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
