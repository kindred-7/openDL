package main

// 所在文件夹定位：Windows 用资源管理器打开目录并选中文件。

import (
	"os/exec"
	"syscall"
)

func revealFile(path string) bool {
	cmd := exec.Command("explorer", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start() == nil
}
