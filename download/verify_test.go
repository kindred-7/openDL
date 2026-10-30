package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublisherChecksumControlsPublication(t *testing.T) {
	for _, size := range []int{1024, 512 << 10} {
		data := bytes.Repeat([]byte("x"), size)
		sum := sha256.Sum256(data)
		for _, match := range []bool{true, false} {
			t.Run(fmtCase(size, match), func(t *testing.T) {
				srv := testServer(t, data)
				dir := t.TempDir()
				m := NewManager(dir, 1, 2)
				expected := hex.EncodeToString(sum[:])
				if !match {
					expected = strings.Repeat("0", 64)
				}
				id := m.Add(Request{URL: srv.URL, Name: "file.bin", SHA256: expected})
				deadline := time.Now().Add(30 * time.Second)
				var snap Snapshot
				for time.Now().Before(deadline) {
					for _, s := range m.Snapshots() {
						if s.ID == id {
							snap = s
						}
					}
					if snap.State == Done || snap.State == ErrorS {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if match {
					if snap.State != Done || snap.VerifiedSHA256 != expected {
						t.Fatalf("verification failed: %+v", snap)
					}
					got, err := os.ReadFile(filepath.Join(dir, "file.bin"))
					if err != nil || !bytes.Equal(got, data) {
						t.Fatalf("published file mismatch: %v", err)
					}
				} else {
					if snap.State != ErrorS || !strings.Contains(snap.Err, "SHA-256") {
						t.Fatalf("mismatch not reported: %+v", snap)
					}
					if _, err := os.Stat(filepath.Join(dir, "file.bin")); !os.IsNotExist(err) {
						t.Fatalf("mismatched file was published: %v", err)
					}
					if _, err := os.Stat(filepath.Join(dir, "file.bin.part")); err != nil {
						t.Fatalf("partial file missing: %v", err)
					}
				}
			})
		}
	}
}

func TestChecksumFailureRetriesWithoutReusingFailedRanges(t *testing.T) {
	good := bytes.Repeat([]byte("good"), 128<<10)
	bad := bytes.Repeat([]byte("bad!"), 128<<10)
	var fixed atomic.Bool
	var ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := bad
		if fixed.Load() {
			data = good
		}
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		w.Header().Set("ETag", `"same"`)
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 2)
	if err := m.EnablePersistence(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(good)
	id := m.Add(Request{URL: srv.URL, Name: "file.bin", SHA256: hex.EncodeToString(sum[:])})
	waitFor(t, m, id, ErrorS)
	previous := ranges.Load()
	fixed.Store(true)
	if !m.Resume(id) {
		t.Fatal("could not retry failed task")
	}
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, good) || ranges.Load() <= previous {
		t.Fatalf("retry did not download a fresh file: ranges=%d previous=%d err=%v", ranges.Load(), previous, err)
	}
}

func TestCorrectingChecksumRetainsCompletedRanges(t *testing.T) {
	data := bytes.Repeat([]byte("data"), 128<<10)
	var ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		w.Header().Set("ETag", `"stable"`)
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 2)
	if err := m.EnablePersistence(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id := m.Add(Request{URL: srv.URL, Name: "file.bin", SHA256: strings.Repeat("0", 64)})
	waitFor(t, m, id, ErrorS)
	previous := ranges.Load()
	sum := sha256.Sum256(data)
	if _, err := m.UpdateSourceWithSHA256(id, srv.URL, nil, hex.EncodeToString(sum[:]), "", ""); err != nil {
		t.Fatal(err)
	}
	if !m.Resume(id) {
		t.Fatal("could not retry after correcting checksum")
	}
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, data) || ranges.Load() != previous {
		t.Fatalf("completed ranges were not reused: ranges=%d previous=%d err=%v", ranges.Load(), previous, err)
	}
}

func fmtCase(size int, match bool) string {
	if size < 256<<10 {
		if match {
			return "single-match"
		}
		return "single-mismatch"
	}
	if match {
		return "split-match"
	}
	return "split-mismatch"
}

func TestNormalizeSHA256(t *testing.T) {
	valid := strings.Repeat("A", 64)
	if got, err := NormalizeSHA256(" " + valid + " "); err != nil || got != strings.ToLower(valid) {
		t.Fatalf("unexpected hash %q: %v", got, err)
	}
	if _, err := NormalizeSHA256(strings.Repeat("z", 64)); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestResumePublicationUsesOnlyMatchingLocalFile(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "modified"}[corrupt], func(t *testing.T) {
			data := []byte("complete download")
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(data)
			}))
			defer srv.Close()
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "file.bin")
			partial := data
			if corrupt {
				partial = []byte("modified download")
			}
			if err := os.WriteFile(path+".part", partial, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			id := newID()
			task := &Task{id: id, url: srv.URL, directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), filePath: path,
				publishSHA256: hex.EncodeToString(sum[:]), publishSize: int64(len(data))}
			task.state.Store(ErrorS)
			m.tasks = append(m.tasks, task)
			if err := m.saveTask(task); err != nil {
				t.Fatal(err)
			}
			if !m.Resume(id) {
				t.Fatal("cannot resume publication")
			}
			waitFor(t, m, id, Done)
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("unexpected output: %q, %v", got, err)
			}
			if (!corrupt && requests.Load() != 0) || (corrupt && requests.Load() == 0) {
				t.Fatalf("unexpected server requests: %d", requests.Load())
			}
		})
	}
}

func TestRestoredPublicationDoesNotRequireOmittedCredentials(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "modified"}[corrupt], func(t *testing.T) {
			data := []byte("saved download")
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(data)
			}))
			defer srv.Close()
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "file.bin")
			partial := data
			if corrupt {
				partial = []byte("changed bytes!")
			}
			if err := os.WriteFile(path+".part", partial, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			id := newID()
			task := &Task{id: id, url: srv.URL, directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), filePath: path,
				headers: map[string]string{"Cookie": "session=secret"}, publishSHA256: hex.EncodeToString(sum[:]), publishSize: int64(len(data))}
			task.state.Store(ErrorS)
			m.tasks = append(m.tasks, task)
			if err := m.saveTask(task); err != nil {
				t.Fatal(err)
			}
			restored := NewManager(dir, 1, 1)
			if err := restored.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			if !restored.Resume(id) {
				t.Fatal("publication could not be resumed without credentials")
			}
			want := Done
			if corrupt {
				want = ErrorS
			}
			snap := waitFor(t, restored, id, want)
			if requests.Load() != 0 {
				t.Fatal("network request made without credentials")
			}
			if corrupt && !strings.Contains(snap.Err, "登录信息") {
				t.Fatalf("missing credential error: %s", snap.Err)
			}
			if !corrupt {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("unexpected published file: %v", err)
				}
			}
		})
	}
}
