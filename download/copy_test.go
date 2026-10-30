package download

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type partialWriter struct{ err error }

func (w partialWriter) Write(p []byte) (int, error) { return len(p) / 2, w.err }

func TestCopyCountsPartialWriteBeforeError(t *testing.T) {
	diskError := errors.New("disk error")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{name: "write error", err: diskError, want: diskError},
		{name: "short write", want: io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir(), 1, 1)
			task := &Task{cancel: make(chan struct{})}
			written, err := m.copyWithProgress(task, partialWriter{err: tc.err}, strings.NewReader("12345678"))
			if !errors.Is(err, tc.want) || written != 4 || task.done.Load() != 4 || task.transferred.Load() != 8 {
				t.Fatalf("written=%d progress=%d transferred=%d err=%v", written, task.done.Load(), task.transferred.Load(), err)
			}
		})
	}
}
