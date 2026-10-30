//go:build !windows

package download

import (
	"errors"
	"os"
	"syscall"
)

// Link publishes atomically without replacing a file created in the meantime.
// Filesystems without hard links or cross-volume destinations fall back to a
// copy that still refuses to overwrite.
func publishFile(part, destination string) error {
	part, destination = extendedPath(part), extendedPath(destination)
	if err := os.Link(part, destination); err != nil {
		if !publishFallbackError(err) {
			return err
		}
		return copyPublishedFile(part, destination)
	}
	// The complete destination is already available even if removing the
	// temporary hard link fails; do not report a failed download in that case.
	_ = os.Remove(part)
	return nil
}

// publishFallbackError 报告硬链接失败是否值得改用复制发布。
func publishFallbackError(err error) bool {
	return errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS)
}

func retryablePublicationError(error) bool { return false }

func replaceSyncedFile(part, destination string) error {
	part, destination = extendedPath(part), extendedPath(destination)
	if err := os.Rename(part, destination); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return err
		}
		return replaceByCopy(part, destination)
	}
	return nil
}
