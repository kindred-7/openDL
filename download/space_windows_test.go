//go:build windows

package download

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestWindowsDiskFullErrorIsExplainedAndPreserved(t *testing.T) {
	for _, code := range []syscall.Errno{112, 39} {
		original := &os.PathError{Op: "write", Path: "file.part", Err: code}
		got := describeStorageError(original)
		if !strings.Contains(got.Error(), "空间不足") || !errors.Is(got, code) {
			t.Fatalf("disk error lost: %v", got)
		}
	}
}
