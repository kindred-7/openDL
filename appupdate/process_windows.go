//go:build windows

package appupdate

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"openDL/i18n"
)

func hideHelper(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

// runInstaller 以提权方式启动安装器升级包；用户取消提权时返回错误，应用
// 保持运行。
func runInstaller(path string) error {
	shellExecute := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")
	operation, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	parameters, err := syscall.UTF16PtrFromString("")
	if err != nil {
		return err
	}
	directory, err := syscall.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return err
	}
	result, _, callErr := shellExecute.Call(0, uintptr(unsafe.Pointer(operation)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(parameters)), uintptr(unsafe.Pointer(directory)), 1)
	if result <= 32 {
		return fmt.Errorf(i18n.T("启动安装器失败（%v）"), callErr)
	}
	return nil
}

func waitParent(ctx context.Context, pid int) error {
	handle, err := syscall.OpenProcess(0x100000, false, uint32(pid))
	if err == syscall.Errno(87) {
		return nil
	} // process already exited
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	for {
		result, err := syscall.WaitForSingleObject(handle, 200)
		if err != nil {
			return err
		}
		if result == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
