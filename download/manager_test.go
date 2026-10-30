package download

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"test-v1"`)
		if r.Header.Get("Range") != "" {
			http.ServeContent(w, r, "f.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Disposition", `attachment; filename="demo.bin"`)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitFor(t *testing.T, m *Manager, id string, want State) Snapshot {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.Snapshots() {
			if s.ID == id && s.State == want {
				return s
			}
			if s.ID == id && s.State == ErrorS {
				t.Fatalf("task failed: %s", s.Err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", want)
	return Snapshot{}
}

func TestRangeWorkersReassignSlowConnectionWithoutLosingBytes(t *testing.T) {
	data := bytes.Repeat([]byte("open-download-012"), 1<<20)
	var blocked atomic.Bool
	interrupted := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		w.Header().Set("Accept-Ranges", "bytes")
		var start, end int64
		if fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end) == 2 && start == 0 && blocked.CompareAndSwap(false, true) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:512<<10])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				close(interrupted)
			case <-release:
			}
			return
		}
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	defer close(release)
	dir := t.TempDir()
	m := NewManager(dir, 1, 2)
	if err := m.EnablePersistence(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id := m.Add(Request{URL: srv.URL + "/file.bin", Connections: 2})
	snap := waitFor(t, m, id, Done)
	select {
	case <-interrupted:
	case <-time.After(5 * time.Second):
		t.Fatal("slow connection was not reassigned")
	}
	got, err := os.ReadFile(snap.FilePath)
	if err != nil || !bytes.Equal(got, data) || snap.Done != int64(len(data)) {
		t.Fatalf("reassigned download mismatch: done=%d size=%d err=%v", snap.Done, len(got), err)
	}
}

func TestSmallDownloadUsesContentDispositionName(t *testing.T) {
	data := []byte("hello opendl")
	srv := testServer(t, data)
	dir := t.TempDir()
	m := NewManager(dir, 1, 2)
	id := m.Add(Request{URL: srv.URL + "/x/dl"})
	s := waitFor(t, m, id, Done)
	if s.Name != "demo.bin" {
		t.Errorf("name %q", s.Name)
	}
	got, err := os.ReadFile(filepath.Join(dir, "demo.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file %q err %v", got, err)
	}
}

func TestSourceRefererPolicy(t *testing.T) {
	for _, tc := range []struct{ page, target, want string }{
		{"https://user:pass@example.test/page?q=1#fragment", "https://example.test/file", "https://example.test/page?q=1"},
		{"https://example.test/private?q=1", "https://cdn.test/file", "https://example.test/"},
		{"https://example.test/private", "http://cdn.test/file", ""},
		{"file:///tmp/private", "https://cdn.test/file", ""},
	} {
		target, err := url.Parse(tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if got := sourceReferer(tc.page, target); got != tc.want {
			t.Errorf("%s → %s: got %q, want %q", tc.page, tc.target, got, tc.want)
		}
	}
}

func TestSplitDownloadWritesExactBytes(t *testing.T) {
	data := make([]byte, 9<<20) // 9 MiB, forces the split path (> 4 MiB)
	_, _ = rand.Read(data)
	srv := testServer(t, data)
	dir := t.TempDir()
	m := NewManager(dir, 1, 4)
	id := m.Add(Request{URL: srv.URL + "/big.bin", Name: "big.bin"})
	s := waitFor(t, m, id, Done)
	if s.Total != int64(len(data)) {
		t.Errorf("total %d want %d", s.Total, len(data))
	}
	got, err := os.ReadFile(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes differ: got %d", len(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin.part")); !os.IsNotExist(err) {
		t.Error(".part file left behind")
	}
}

func TestSmallRangeAndIgnoredRangeFallback(t *testing.T) {
	data := bytes.Repeat([]byte("range-data"), 40_000)
	for _, ignore := range []bool{false, true} {
		t.Run(strconv.FormatBool(ignore), func(t *testing.T) {
			var ranges, full atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Accept-Ranges", "bytes")
				if r.Header.Get("Range") != "" {
					ranges.Add(1)
					if !ignore {
						http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
						return
					}
				} else {
					full.Add(1)
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				_, _ = w.Write(data)
			}))
			defer srv.Close()
			dir := t.TempDir()
			m := NewManager(dir, 1, 1)
			id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
			s := waitFor(t, m, id, Done)
			got, err := os.ReadFile(s.FilePath)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("file mismatch: %v", err)
			}
			if ranges.Load() == 0 {
				t.Fatal("expected range requests")
			}
			if ignore && (full.Load() < 2 || s.SupportsRanges) {
				t.Fatal("did not fall back to a fresh single stream")
			}
		})
	}
}

func TestUnsatisfiableRangeFetchesFreshRepresentation(t *testing.T) {
	oldData := bytes.Repeat([]byte("old!"), 128<<10)
	newData := bytes.Repeat([]byte("new!"), 96<<10)
	var full, ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
			w.Header().Set("Content-Range", "bytes */"+strconv.Itoa(len(newData)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		data := oldData
		etag := `"old"`
		if full.Add(1) > 1 {
			data, etag = newData, `"new"`
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, newData) || full.Load() < 2 || ranges.Load() == 0 || s.ResumeSafe {
		t.Fatalf("fresh download failed: full=%d ranges=%d state=%+v err=%v", full.Load(), ranges.Load(), s, err)
	}
}

func TestRejectedRangesFallBackToFullDownload(t *testing.T) {
	data := bytes.Repeat([]byte("data"), 128<<10)
	for _, code := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusPreconditionFailed, http.StatusNotImplemented} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var full, ranges atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					ranges.Add(1)
					w.WriteHeader(code)
					return
				}
				full.Add(1)
				w.Header().Set("ETag", `"stable"`)
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				_, _ = w.Write(data)
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 1)
			id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
			s := waitFor(t, m, id, Done)
			got, err := os.ReadFile(s.FilePath)
			if err != nil || !bytes.Equal(got, data) || ranges.Load() == 0 || full.Load() < 2 {
				t.Fatalf("fallback failed: full=%d ranges=%d err=%v", full.Load(), ranges.Load(), err)
			}
		})
	}
}

func TestRangeFallbackCancelsOtherInFlightRequests(t *testing.T) {
	data := bytes.Repeat([]byte("data"), 128<<10)
	var ranges atomic.Int64
	blocked, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") != "" {
			if ranges.Add(1) == 1 {
				close(blocked)
				select {
				case <-r.Context().Done():
					close(stopped)
				case <-release:
				}
				return
			}
			<-blocked
			// Ignore Range to force fallback while the other request is blocked.
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	defer close(release)
	m := NewManager(t.TempDir(), 1, 2)
	id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request was not canceled")
	}
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("fallback file mismatch: %v", err)
	}
}

func TestDifferentRangeRedirectTargetFallsBackToFullDownload(t *testing.T) {
	first := bytes.Repeat([]byte("first"), 100_000)
	other := bytes.Repeat([]byte("other"), 100_000)
	var full, ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			target := "/first"
			if r.Header.Get("Range") != "" {
				ranges.Add(1)
				target = "/other"
			} else {
				full.Add(1)
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		data := first
		if r.URL.Path == "/other" {
			data = other
		}
		w.Header().Set("ETag", `"same"`)
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL + "/start", Name: "file.bin"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, first) || full.Load() < 2 || ranges.Load() == 0 {
		t.Fatalf("redirect fallback failed: full=%d ranges=%d err=%v", full.Load(), ranges.Load(), err)
	}
}

func TestDifferentRangeRedirectTargetWithChecksum(t *testing.T) {
	first := bytes.Repeat([]byte("first"), 100_000)
	for _, mismatch := range []bool{false, true} {
		t.Run(strconv.FormatBool(mismatch), func(t *testing.T) {
			var full, ranges atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					target := "/first"
					if r.Header.Get("Range") != "" {
						ranges.Add(1)
						target = "/other"
					} else {
						full.Add(1)
					}
					http.Redirect(w, r, target, http.StatusFound)
					return
				}
				data := first
				if mismatch && r.URL.Path == "/other" {
					data = bytes.Repeat([]byte("other"), 100_000)
				}
				w.Header().Set("ETag", `"same"`)
				http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
			}))
			defer srv.Close()
			dir := t.TempDir()
			m := NewManager(dir, 1, 1)
			sum := sha256.Sum256(first)
			id := m.Add(Request{URL: srv.URL + "/start", Name: "file.bin", SHA256: hex.EncodeToString(sum[:])})
			state := Done
			if mismatch {
				state = ErrorS
			}
			s := waitFor(t, m, id, state)
			if ranges.Load() == 0 || full.Load() != 1 {
				t.Fatalf("unexpected requests: full=%d ranges=%d", full.Load(), ranges.Load())
			}
			if mismatch {
				if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
					t.Fatalf("mismatched file was published: %v", err)
				}
			} else if got, err := os.ReadFile(s.FilePath); err != nil || !bytes.Equal(got, first) {
				t.Fatalf("valid file mismatch: %v", err)
			}
		})
	}
}

func TestRetriesTemporaryServerResponses(t *testing.T) {
	data := bytes.Repeat([]byte("network!"), (512<<10)/8)
	var probes, ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("Range") == "" {
			if probes.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		} else if ranges.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file mismatch: %v", err)
	}
	if probes.Load() < 2 || ranges.Load() < 2 {
		t.Fatalf("requests probe=%d range=%d", probes.Load(), ranges.Load())
	}
}

func TestLongRetryAfterSharedAcrossTasks(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	for i := 0; i < 2; i++ {
		task := &Task{cancel: make(chan struct{})}
		req, cancel, err := task.newRequest(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.doWithRetry(task, req)
		cancel()
		if err == nil {
			t.Fatal("expected rate-limit error")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("shared cooldown allowed %d requests", got)
	}
	if delay := retryAfter("3600", time.Now()); delay != time.Hour {
		t.Fatalf("retry-after lost server deadline: %v", delay)
	}
}

func TestStalledSingleStreamRetries(t *testing.T) {
	data := []byte("the complete file")
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if requests.Add(1) == 1 {
			_, _ = w.Write(data[:4])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 1)
	m.bodyIdleTimeout = 50 * time.Millisecond
	id := m.Add(Request{URL: srv.URL, Name: "file.bin"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, data) || requests.Load() < 2 {
		t.Fatalf("retry failed: data=%q requests=%d err=%v", got, requests.Load(), err)
	}
}

func TestIdleReaderIgnoresTimeBetweenReads(t *testing.T) {
	canceled := atomic.Bool{}
	r := idleReader{body: bytes.NewReader([]byte("ab")), timeout: 20 * time.Millisecond, cancel: func() { canceled.Store(true) }}
	var b [1]byte
	if _, err := r.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond) // disk writes and rate limiting happen here
	if _, err := r.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	if canceled.Load() {
		t.Fatal("idle timeout ran between reads")
	}
}

func TestCancelDuringRetryAfter(t *testing.T) {
	received := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		received <- struct{}{}
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL})
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("request not received")
	}
	m.Cancel(id)
	waitFor(t, m, id, Canceled)
}

func TestPermanentHTTPErrorIsNotRetried(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{url: srv.URL, cancel: make(chan struct{})}
	req, cancel, err := task.newRequest(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, err = m.doWithRetry(task, req); err == nil || requests.Load() != 1 {
		t.Fatalf("expected one failed request, got %d: %v", requests.Load(), err)
	}
}

// A server that ignores or misreports a range must never write that response
// into another worker's portion of the output file.
func TestDownloadRangeRejectsInvalidResponses(t *testing.T) {
	cases := []struct {
		name, contentRange, body string
		status                   int
	}{
		{"ignored range", "", "abcdef", http.StatusOK},
		{"wrong range", "bytes 0-2/6", "abc", http.StatusPartialContent},
		{"short body", "bytes 3-5/6", "de", http.StatusPartialContent},
		{"long body", "bytes 3-5/6", "defg", http.StatusPartialContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=3-5" || r.Header.Get("If-Range") != `"version-1"` {
					t.Errorf("unexpected range headers: %v", r.Header)
				}
				if tc.contentRange != "" {
					w.Header().Set("Content-Range", tc.contentRange)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			out, err := os.CreateTemp(t.TempDir(), "range")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			if err := out.Truncate(6); err != nil {
				t.Fatal(err)
			}
			task := &Task{url: srv.URL, cancel: make(chan struct{})}
			m := NewManager(t.TempDir(), 1, 1)
			if _, err := m.downloadRange(task, out, 3, 5, 6, `"version-1"`); err == nil {
				t.Fatal("invalid response accepted")
			}
			data := make([]byte, 6)
			if _, err := out.ReadAt(data, 0); err != nil {
				t.Fatal(err)
			}
			info, err := out.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data[:3], make([]byte, 3)) || info.Size() != 6 {
				t.Fatalf("response overwrote another range: %v", data)
			}
		})
	}
}

// Dynamic scheduling splits big chunks further, so the server sees more
// range requests than there are connections (IDM's FindMaxFreeChunk).
func TestDynamicChunkScheduling(t *testing.T) {
	data := make([]byte, 9<<20)
	_, _ = rand.Read(data)
	var ranges int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			atomic.AddInt64(&ranges, 1)
		}
		http.ServeContent(w, r, "f.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 2) // only two connections
	id := m.Add(Request{URL: srv.URL + "/big.bin", Name: "big.bin"})
	s := waitFor(t, m, id, Done)
	if s.Total != int64(len(data)) {
		t.Errorf("total %d", s.Total)
	}
	// Static splitting would make exactly two requests; dynamic splitting
	// carves the halves further as workers free up.
	if got := atomic.LoadInt64(&ranges); got <= 2 {
		t.Errorf("range requests %d, want more than the connection count", got)
	}
}

func TestUnknownLengthRecordsActualSize(t *testing.T) {
	data := []byte("streamed file contents")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	id := m.Add(Request{URL: srv.URL, Name: "stream.bin"})
	s := waitFor(t, m, id, Done)
	if s.Total != int64(len(data)) || s.Done != int64(len(data)) {
		t.Fatalf("incorrect completed size: %+v", s)
	}
	if err := m.saveTask(m.tasks[0]); err != nil {
		t.Fatal(err)
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	snapshots := restored.Snapshots()
	if len(snapshots) != 1 || snapshots[0].State != Done || snapshots[0].Total != int64(len(data)) || snapshots[0].Done != int64(len(data)) {
		t.Fatalf("incorrect restored size: %+v", snapshots)
	}
}

func TestSingleStreamReportsActiveSegment(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			data := []byte("streamed content")
			started, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if known {
					w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data[:4])
				w.(http.Flusher).Flush()
				close(started)
				<-release
				_, _ = w.Write(data[4:])
			}))
			defer srv.Close()
			defer close(release)
			m := NewManager(t.TempDir(), 1, 1)
			id := m.Add(Request{URL: srv.URL, Name: "stream.bin"})
			<-started
			deadline := time.Now().Add(5 * time.Second)
			for {
				s := m.Snapshots()[0]
				if len(s.Segments) == 1 && s.Segments[0].Active && s.Segments[0].Downloaded == 4 {
					if !known && s.Segments[0].End != -1 {
						t.Fatalf("unknown stream has end: %+v", s.Segments[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("active stream not reported: %+v", s)
				}
				time.Sleep(10 * time.Millisecond)
			}
			release <- struct{}{}
			s := waitFor(t, m, id, Done)
			if len(s.Segments) != 1 || s.Segments[0].Active || s.Segments[0].End != int64(len(data)-1) {
				t.Fatalf("completed stream not reported: %+v", s.Segments)
			}
		})
	}
}

type noAcceptRangesWriter struct{ http.ResponseWriter }

func (w noAcceptRangesWriter) WriteHeader(status int) {
	w.Header().Del("Accept-Ranges")
	w.ResponseWriter.WriteHeader(status)
}

func TestRangeWithoutAcceptRangesHeader(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16)
	var ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		http.ServeContent(noAcceptRangesWriter{w}, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 2)
	id := m.Add(Request{URL: srv.URL + "/file.bin", Name: "file.bin"})
	s := waitFor(t, m, id, Done)
	if ranges.Load() == 0 || !s.SupportsRanges || !s.ResumeSafe {
		t.Fatalf("range support not detected: requests=%d snapshot=%+v", ranges.Load(), s)
	}
	got, err := os.ReadFile(filepath.Join(dir, "file.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded content mismatch: %v", err)
	}
}

func TestCancelRunningTask(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		_, _ = w.Write(make([]byte, 4096))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/slow", Name: "slow.bin"})
	time.Sleep(100 * time.Millisecond)
	m.Cancel(id)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.Snapshots() {
			if s.ID == id && s.State == Canceled {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never canceled")
}

func TestMoveRunningTaskToNightKeepsIdentity(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL, Name: "file.bin", OpenFolder: true})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	if !m.MoveToNight(id) {
		t.Fatal("could not move active task")
	}
	deadline := time.After(5 * time.Second)
	for {
		snaps := m.Snapshots()
		if len(snaps) == 1 && snaps[0].ID == id && snaps[0].Night && snaps[0].Held && snaps[0].State == Queued {
			if !snaps[0].OpenFolder {
				t.Fatal("completion option lost")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("task not held in night queue: %+v", snaps)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRateLimitWaitCanBeCanceled(t *testing.T) {
	var limiter paceLimiter
	limiter.setRate(1)
	canceled := make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := limiter.acquire(128<<10, canceled); done <- err }()
	close(canceled)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("rate limiter did not respond to cancellation")
	}
}

func TestRateLimitChangeWakesWaitingReads(t *testing.T) {
	var limiter paceLimiter
	limiter.setRate(1)
	result := make(chan readAllowance, 1)
	go func() { a, _ := limiter.acquire(128<<10, make(chan struct{})); result <- a }()
	limiter.setRate(0)
	select {
	case a := <-result:
		if a.bytes != 128<<10 || a.limited {
			t.Fatalf("old limit still applied: %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("disabling the limit did not wake a waiting read")
	}
}

func TestRateLimiterReturnsUnusedAllowance(t *testing.T) {
	var limiter paceLimiter
	limiter.setRate(1024)
	limiter.mu.Lock()
	limiter.last = time.Now().Add(-time.Second)
	limiter.mu.Unlock()
	cancel := make(chan struct{})
	first, err := limiter.acquire(128<<10, cancel)
	if err != nil || first.bytes != 256 {
		t.Fatalf("unexpected first allowance: %+v, %v", first, err)
	}
	result := make(chan readAllowance, 1)
	go func() { a, _ := limiter.acquire(128<<10, cancel); result <- a }()
	limiter.refund(first, 0)
	select {
	case second := <-result:
		if second.bytes != 256 {
			t.Fatalf("unused allowance not returned: %+v", second)
		}
	case <-time.After(time.Second):
		t.Fatal("unused allowance did not wake a waiting read")
	}
}

func TestSpeedSamplerCombinesTransfersAndClearsOnStall(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 2)
	task := &Task{}
	observed := make(chan int64, 4)
	m.OnChange(func() { observed <- task.speed.Load() })
	stop, done := make(chan struct{}), make(chan struct{})
	go m.sampleSpeed(task, stop, done)
	defer func() { close(stop); <-done }()
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("speed sampler did not start")
	}
	var writers sync.WaitGroup
	for _, n := range []int64{1024, 2048} {
		writers.Add(1)
		go func(n int64) { defer writers.Done(); task.transferred.Add(n) }(n)
	}
	writers.Wait()
	select {
	case speed := <-observed:
		if speed < 4096 {
			t.Fatalf("speed did not include both transfers: %d", speed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no speed update")
	}
	select {
	case speed := <-observed:
		if speed != 0 {
			t.Fatalf("stalled speed = %d", speed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no stall update")
	}
}

func TestQueuedDownloadCanBeCanceled(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Path
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	defer close(release)
	m := NewManager(t.TempDir(), 1, 1)
	first := m.Add(Request{URL: srv.URL + "/first", Name: "first.bin"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first download did not start")
	}
	second := m.Add(Request{URL: srv.URL + "/second", Name: "second.bin"})
	for _, s := range m.Snapshots() {
		if s.ID == second && s.State != Queued {
			t.Fatalf("second task state %s, want queued", s.State)
		}
	}
	m.Cancel(second)
	waitFor(t, m, second, Canceled)
	select {
	case path := <-started:
		t.Fatalf("queued download unexpectedly started: %s", path)
	default:
	}
	m.Cancel(first)
}

func TestQueuedTasksStartInOrderAfterCancellation(t *testing.T) {
	started := make(chan string, 4)
	releaseFirst, releaseSecond := make(chan struct{}, 1), make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Path
		switch r.URL.Path {
		case "/first":
			<-releaseFirst
		case "/second":
			<-releaseSecond
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	defer close(releaseFirst)
	defer close(releaseSecond)
	m := NewManager(t.TempDir(), 1, 1)
	readStarted := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("started %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	m.Add(Request{URL: srv.URL + "/first", Name: "first"})
	readStarted("/first")
	m.Add(Request{URL: srv.URL + "/second", Name: "second"})
	third := m.Add(Request{URL: srv.URL + "/third", Name: "third"})
	fourth := m.Add(Request{URL: srv.URL + "/fourth", Name: "fourth"})
	m.Cancel(third)
	waitFor(t, m, third, Canceled)
	releaseFirst <- struct{}{}
	readStarted("/second")
	releaseSecond <- struct{}{}
	readStarted("/fourth")
	waitFor(t, m, fourth, Done)
}

func TestResumeReusesOnlyValidatedCompletedRanges(t *testing.T) {
	data := bytes.Repeat([]byte("resume-data"), (6<<20)/len("resume-data")+1)[:6<<20]
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			var mu sync.Mutex
			var ranges []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"current"`)
				if value := r.Header.Get("Range"); value != "" {
					mu.Lock()
					ranges = append(ranges, value)
					mu.Unlock()
				}
				http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
			}))
			defer srv.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "file.bin")
			part := make([]byte, len(data))
			copy(part[:1<<20], data[:1<<20])
			if err := os.WriteFile(path+".part", part, 0o600); err != nil {
				t.Fatal(err)
			}
			m := NewManager(dir, 1, 2)
			id := m.AddPaused(Request{URL: srv.URL, Name: "file.bin"})
			task := m.tasks[0]
			task.filePath = path
			task.completed = []chunkSpan{{start: 0, end: 1<<20 - 1}}
			task.resumeTotal = int64(len(data))
			task.resumeValidator = `"current"`
			resource, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			task.resumeResource = resourceFingerprint(resource)
			if changed {
				task.resumeValidator = `"old"`
			}
			if !m.Resume(id) {
				t.Fatal("resume failed")
			}
			waitFor(t, m, id, Done)
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("file mismatch: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			seenFirst := false
			for _, value := range ranges {
				if strings.HasPrefix(value, "bytes=0-") {
					seenFirst = true
				}
			}
			if seenFirst != changed {
				t.Fatalf("unexpected reuse after resource change=%v: %v", changed, ranges)
			}
		})
	}
}

func TestAddDedupesFileNames(t *testing.T) {
	data := []byte("x")
	srv := testServer(t, data)
	dir := t.TempDir()
	m := NewManager(dir, 2, 1)
	a := m.Add(Request{URL: srv.URL + "/a", Name: "same.bin"})
	b := m.Add(Request{URL: srv.URL + "/b", Name: "same.bin"})
	waitFor(t, m, a, Done)
	sb := waitFor(t, m, b, Done)
	var sa Snapshot
	for _, s := range m.Snapshots() {
		if s.ID == a {
			sa = s
		}
	}
	// The .part claim decides who keeps the name; the two must differ.
	if sa.Name == sb.Name {
		t.Errorf("both tasks named %q", sa.Name)
	}
	for _, s := range []Snapshot{sa, sb} {
		if _, err := os.Stat(filepath.Join(dir, s.Name)); err != nil {
			t.Errorf("missing file %q: %v", s.Name, err)
		}
	}
}

func TestExistingDestinationIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "same.bin")
	if err := os.WriteFile(old, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, []byte("new"))
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL, Name: "same.bin"})
	s := waitFor(t, m, id, Done)
	if s.Name != "same (1).bin" {
		t.Fatalf("name %q", s.Name)
	}
	got, err := os.ReadFile(old)
	if err != nil || string(got) != "existing" {
		t.Fatalf("existing file changed: %q, %v", got, err)
	}
}

func TestPublishDoesNotReplaceLateDestination(t *testing.T) {
	dir := t.TempDir()
	part, dest := filepath.Join(dir, "file.part"), filepath.Join(dir, "file")
	if err := os.WriteFile(part, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishFile(part, dest); err == nil {
		t.Fatal("publishing replaced an existing destination")
	}
	for path, want := range map[string]string{part: "new", dest: "existing"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, %v", path, got, err)
		}
	}
}

func TestSuppliedFilenameCannotEscapeDirectory(t *testing.T) {
	dir := t.TempDir()
	srv := testServer(t, []byte("data"))
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL, Name: `../outside.bin`})
	s := waitFor(t, m, id, Done)
	if filepath.Dir(s.FilePath) != dir || strings.ContainsAny(s.Name, `/\\`) {
		t.Fatalf("unsafe file path %q", s.FilePath)
	}
}
