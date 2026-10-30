//go:build windows

package download

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// MoveFile fails if the destination already exists, including when it was
// created after the download claimed its name.
func publishFile(part, destination string) error {
	part, destination = extendedPath(part), extendedPath(destination)
	from, err := syscall.UTF16PtrFromString(part)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(from, to); err != nil {
		// 跨卷移动时改用复制发布，仍然拒绝覆盖已存在的目标。
		if errors.Is(err, syscall.Errno(17)) {
			return copyPublishedFile(part, destination)
		}
		return &os.LinkError{Op: "move", Old: part, New: destination, Err: err}
	}
	return nil
}

func replaceSyncedFile(part, destination string) error {
	from, err := syscall.UTF16PtrFromString(extendedPath(part))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(extendedPath(destination))
	if err != nil {
		return err
	}
	move := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
	ok, _, err := move.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x2|0x8)
	if ok == 0 {
		return &os.LinkError{Op: "replace", Old: part, New: destination, Err: err}
	}
	return nil
}

// Temporary antivirus/indexer locks commonly surface as access, sharing or
// lock violations. Name collisions and unrelated failures are not retried.
func retryablePublicationError(err error) bool {
	return errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
