//go:build !windows

package download

import "os/exec"

func hideMediaMux(cmd *exec.Cmd) {}
