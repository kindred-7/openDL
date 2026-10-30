//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
	getLogicalDrives = kernel32.NewProc("GetLogicalDrives")
)

// diskStatus 枚举 Windows 报告的所有卷，跳过无介质的驱动器。
func diskStatus() []diskStat {
	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil
	}
	var list []diskStat
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		free, total := diskSpace(root)
		if total <= 0 {
			continue
		}
		list = append(list, diskStat{name: string(rune('A'+i)) + ":", free: free, total: total})
	}
	return list
}

// diskSpace returns the free and total bytes on the volume holding path
// (both -1 when unavailable). A directory that does not exist yet resolves
// to its nearest existing ancestor.
func diskSpace(path string) (free, total int64) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return -1, -1
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return -1, -1
	}
	var available, totalBytes, totalFree uint64
	r, _, _ := getDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&available)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return -1, -1
	}
	return int64(available), int64(totalBytes)
}
