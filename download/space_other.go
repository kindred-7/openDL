//go:build !windows

package download

import (
	"errors"
	"os"
	"syscall"
)

// platformAvailableSpace 返回文件所在文件系统的可用空间与文件的物理分
// 配量。POSIX 的 st_blocks 以 512 字节计；不支持的平台保持未知。
func platformAvailableSpace(file *os.File) (int64, int64, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(file.Name(), &stat); err != nil {
		return 0, 0, false
	}
	unit := int64(stat.Bsize)
	if unit <= 0 {
		return 0, 0, false
	}
	free := int64(stat.Bavail) * unit
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return 0, 0, false
	}
	allocated := int64(info.Blocks) * 512
	if free < 0 || allocated < 0 {
		return 0, 0, false
	}
	return free, allocated, true
}

func platformDiskFull(err error) bool { return errors.Is(err, syscall.ENOSPC) }
