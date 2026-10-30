package download

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestFileSizeLimitRejectsDeclaredOverflowWithoutAllocatingFile(t *testing.T) {
	if err := checkDeclaredFileSize(fat32MaxFileSize, fat32MaxFileSize); err != nil {
		t.Fatal(err)
	}
	var limitErr *fileSizeLimitError
	if err := checkDeclaredFileSize(fat32MaxFileSize, fat32MaxFileSize+1); !errors.As(err, &limitErr) {
		t.Fatalf("oversized file was accepted: %v", err)
	}
	if err := checkDeclaredFileSize(0, fat32MaxFileSize+1); err != nil {
		t.Fatalf("unknown filesystem acquired a guessed limit: %v", err)
	}
}

func TestUnknownLengthStreamStopsBeforeFileSystemLimit(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{cancel: make(chan struct{})}
	task.outputLimit.Store(10)
	var output bytes.Buffer
	written, err := m.copyWithProgress(task, &output, iotest.OneByteReader(strings.NewReader("12345678901")))
	var limitErr *fileSizeLimitError
	if !errors.As(err, &limitErr) || written != 10 || output.String() != "1234567890" || task.done.Load() != 10 || task.transferred.Load() != 11 {
		t.Fatalf("stream exceeded limit: written=%d progress=%d network=%d output=%q err=%v", written, task.done.Load(), task.transferred.Load(), output.String(), err)
	}
}

func TestResumedStreamChecksGrowthFromExistingPrefix(t *testing.T) {
	task := &Task{}
	task.outputLimit.Store(fat32MaxFileSize)
	task.done.Store(fat32MaxFileSize - 188)
	if err := checkOutputGrowth(task, 188); err != nil {
		t.Fatalf("exact boundary was rejected: %v", err)
	}
	if err := checkOutputGrowth(task, 189); err == nil {
		t.Fatal("resumed output could exceed the filesystem limit")
	}
}
