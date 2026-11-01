package download

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A slow first range must remain open while an idle peer starts its tail.
// The assembled file must still match the original bytes.
func TestHTTPRangeSplitCompletesWithoutInterruptingOriginal(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), (12<<20)/16)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var slowStarted, splitObserved, originalInterrupted atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"split-test-v1"`)
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err == nil {
			if start == 0 && slowStarted.CompareAndSwap(false, true) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(data[:512<<10])
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = w.Write(data[512<<10 : end+1])
				case <-r.Context().Done():
					originalInterrupted.Store(true)
				}
				return
			}
			if start > 0 && start < 4<<20 && slowStarted.Load() {
				splitObserved.Store(true)
				unblock()
			}
		}
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer server.Close()
	defer unblock()

	m := NewManager(t.TempDir(), 1, 2)
	if err := m.EnablePersistence(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id := m.Add(Request{URL: server.URL + "/file.bin", Connections: 2})
	defer m.Cancel(id)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, snap := range m.Snapshots() {
			if snap.ID != id {
				continue
			}
			if snap.State == ErrorS {
				t.Fatalf("download failed: %s", snap.Err)
			}
			if snap.State == Done {
				if !splitObserved.Load() || originalInterrupted.Load() {
					t.Fatalf("split observed = %v, original interrupted = %v", splitObserved.Load(), originalInterrupted.Load())
				}
				got, err := os.ReadFile(snap.FilePath)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("assembled file mismatch: size %d, error %v", len(got), err)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("download timed out")
}

// Pausing a ranged transfer must retain its written prefixes and resume them
// without gaps or duplicated bytes, including with multiple active requests.
func TestHTTPRangePauseResumePreservesPartialProgress(t *testing.T) {
	data := bytes.Repeat([]byte("pause-resume-123"), (8<<20)/16)
	var hold atomic.Bool
	hold.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"resume-test-v1"`)
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err == nil && hold.Load() {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			prefixEnd := min(end+1, start+(256<<10))
			_, _ = w.Write(data[start:prefixEnd])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer server.Close()

	m := NewManager(t.TempDir(), 1, 2)
	if err := m.EnablePersistence(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id := m.Add(Request{URL: server.URL + "/file.bin", Connections: 2})
	defer m.Cancel(id)
	wait := func(want State, requireProgress bool) Snapshot {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			for _, snap := range m.Snapshots() {
				if snap.ID != id {
					continue
				}
				if snap.State == ErrorS {
					t.Fatalf("download failed: %s", snap.Err)
				}
				if snap.State == want && (!requireProgress || snap.Done > 0) {
					return snap
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", want)
		return Snapshot{}
	}
	wait(Running, true)
	m.Cancel(id)
	paused := wait(Canceled, false)
	if paused.Done == 0 {
		t.Fatal("pause discarded all written progress")
	}
	hold.Store(false)
	if !m.Resume(id) {
		t.Fatal("could not resume paused task")
	}
	done := wait(Done, false)
	got, err := os.ReadFile(done.FilePath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("resumed file mismatch: size %d, error %v", len(got), err)
	}
}
