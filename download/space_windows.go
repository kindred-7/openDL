//go:build windows

package download

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	spaceKernel       = syscall.NewLazyDLL("kernel32.dll")
	fileInformationEx = spaceKernel.NewProc("GetFileInformationByHandleEx")
	finalPathByHandle = spaceKernel.NewProc("GetFinalPathNameByHandleW")
	diskFreeSpaceEx   = spaceKernel.NewProc("GetDiskFreeSpaceExW")
)

type fileStandardInformation struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  byte
	Directory      byte
	_              [2]byte
}

func platformAvailableSpace(file *os.File) (free, allocated int64, ok bool) {
	if fileInformationEx.Find() != nil || finalPathByHandle.Find() != nil || diskFreeSpaceEx.Find() != nil {
		return 0, 0, false
	}
	defer runtime.KeepAlive(file)
	var attributes syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &attributes) != nil {
		return 0, 0, false
	}
	// Sparse/compressed output needs filesystem-specific allocation accounting.
	// Let actual writes report errors rather than infer space from logical EOF.
	if attributes.FileAttributes&(0x800|0x200) != 0 {
		return 0, 0, false
	}
	var info fileStandardInformation
	result, _, _ := fileInformationEx.Call(file.Fd(), 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)) // FileStandardInfo
	if result == 0 || info.AllocationSize < 0 {
		return 0, 0, false
	}
	path := make([]uint16, 32768)
	length, _, _ := finalPathByHandle.Call(file.Fd(), uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
	if length == 0 || length >= uintptr(len(path)) {
		return 0, 0, false
	}
	directory := filepath.Dir(syscall.UTF16ToString(path[:int(length)]))
	if !strings.HasSuffix(directory, `\`) {
		directory += `\`
	}
	encoded, err := syscall.UTF16PtrFromString(directory)
	if err != nil {
		return 0, 0, false
	}
	var available uint64
	result, _, _ = diskFreeSpaceEx.Call(uintptr(unsafe.Pointer(encoded)), uintptr(unsafe.Pointer(&available)), 0, 0)
	if result == 0 {
		return 0, 0, false
	}
	const maxInt64 = uint64(1<<63 - 1)
	if available > maxInt64 {
		available = maxInt64
	}
	return int64(available), info.AllocationSize, true
}

func platformDiskFull(err error) bool {
	return errors.Is(err, syscall.Errno(112)) || errors.Is(err, syscall.Errno(39)) // ERROR_DISK_FULL / ERROR_HANDLE_DISK_FULL
}
