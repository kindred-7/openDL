//go:build windows

package download

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var volumeInformationByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetVolumeInformationByHandleW")

func platformOutputLimit(file *os.File) int64 {
	if volumeInformationByHandle.Find() != nil {
		return 0
	}
	var name [64]uint16
	result, _, _ := volumeInformationByHandle.Call(file.Fd(), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	runtime.KeepAlive(file)
	if result == 0 {
		return 0
	}
	if strings.EqualFold(syscall.UTF16ToString(name[:]), "FAT32") {
		return fat32MaxFileSize
	}
	return 0
}
