package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddRecordedSavesBeforeAcknowledgingQueuedTask(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "tasks")
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	blocker := m.enqueueLocked(&Task{cancel: make(chan struct{})})
	m.mu.Unlock()
	defer m.releaseTicket(blocker)
	id, err := m.AddRecorded(Request{URL: "https://example.com/file"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Remove(id)
	data, err := os.ReadFile(filepath.Join(store, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec storedTask
	if err := json.Unmarshal(data, &rec); err != nil || rec.ID != id || rec.State != Queued {
		t.Fatalf("unexpected task journal: %+v, %v", rec, err)
	}
}

func TestAddRecordedDoesNotStartOnJournalFailure(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "tasks")
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	if id, err := m.AddRecorded(Request{URL: "https://example.com/file"}); err == nil || id != "" {
		t.Fatalf("task accepted despite journal failure: id=%q err=%v", id, err)
	}
	if got := len(m.Snapshots()); got != 0 {
		t.Fatalf("failed request created %d tasks", got)
	}
}

func TestAddRecordedRestoresHeldNightTask(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	id, err := m.AddRecorded(Request{URL: "https://example.com/file", Queue: "night"})
	if err != nil {
		t.Fatal(err)
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	snaps := restored.Snapshots()
	if len(snaps) != 1 || snaps[0].ID != id || !snaps[0].Night || !snaps[0].Held || snaps[0].NightSince.IsZero() || snaps[0].State != Queued {
		t.Fatalf("night task was not restored: %+v", snaps)
	}
}

func TestRecordedSubmissionIsIdempotentAcrossConcurrencyAndRestart(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	req := Request{URL: "https://user:password@example.test/file", Queue: "night", ClientRequestID: "submission-1", Headers: map[string]string{"Cookie": "session=secret"}}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errorsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); id, err := m.AddRecorded(req); ids <- id; errorsSeen <- err }()
	}
	wg.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original string
	for id := range ids {
		if original == "" {
			original = id
		}
		if id != original {
			t.Fatal("duplicate task IDs returned")
		}
	}
	if len(m.Snapshots()) != 1 {
		t.Fatal("duplicate tasks were added")
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	req.Headers = map[string]string{"Cookie": "session=refreshed"}
	req.URL = "https://user:new-password@example.test/file"
	req.Title = "updated page title"
	id, err := restored.AddRecorded(req)
	if err != nil || id != original || len(restored.Snapshots()) != 1 {
		t.Fatalf("restart duplicate: id=%q err=%v", id, err)
	}
	req.URL = "https://example.test/different"
	if _, err := restored.AddRecorded(req); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("conflicting request accepted: %v", err)
	}
}

func TestLegacyCompletedUnknownLength(t *testing.T) {
	for _, hasPublishRecord := range []bool{false, true} {
		t.Run(strconv.FormatBool(hasPublishRecord), func(t *testing.T) {
			dir, store := t.TempDir(), t.TempDir()
			id := newID()
			rec := storedTask{Version: 1, ID: id, Request: Request{URL: "https://example.com/file", Directory: dir}, State: Done, Total: -1}
			if hasPublishRecord {
				rec.PublishSHA256 = strings.Repeat("a", 64)
				rec.PublishSize = 123
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store, id+".json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			s := m.Snapshots()[0]
			wantTotal, wantDone := int64(-1), int64(0)
			if hasPublishRecord {
				wantTotal, wantDone = 123, 123
			}
			if s.Total != wantTotal || s.Done != wantDone {
				t.Fatalf("unexpected restored size: %+v", s)
			}
		})
	}
}

func TestPersistedRangesAreValidatedBeforeResume(t *testing.T) {
	// Exercise restoration below the old 4 MiB splitting threshold as well.
	data := bytes.Repeat([]byte("download"), (2<<20)/8)
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "modified"}[corrupt], func(t *testing.T) {
			var mu sync.Mutex
			var ranges []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				if value := r.Header.Get("Range"); value != "" {
					mu.Lock()
					ranges = append(ranges, value)
					mu.Unlock()
				}
				http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
			}))
			defer srv.Close()
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 2)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			id := newID()
			task := &Task{id: id, url: srv.URL, nameHint: "file.bin", directory: dir, conns: 2, created: time.Now(), cancel: make(chan struct{})}
			task.state.Store(Queued)
			task.held.Store(true)
			m.tasks = append(m.tasks, task)
			path := filepath.Join(dir, "file.bin")
			part := make([]byte, len(data))
			copy(part[:1<<20], data[:1<<20])
			if err := os.WriteFile(path+".part", part, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path + ".part")
			if err != nil {
				t.Fatal(err)
			}
			chunk := chunkSpan{start: 0, end: 1<<20 - 1}
			chunk.digest, err = chunkDigest(f, chunk)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			task.checkpointMu.Lock()
			task.metaMu.Lock()
			task.filePath = path
			task.metaMu.Unlock()
			task.completed = []chunkSpan{chunk}
			task.resumeTotal, task.resumeValidator = int64(len(data)), `"v1"`
			resource, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			task.resumeResource = resourceFingerprint(resource)
			err = m.saveTaskLocked(task)
			task.checkpointMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				f, err := os.OpenFile(path+".part", os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteAt([]byte("x"), 0)
				_ = f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			restored := NewManager(dir, 1, 2)
			if err := restored.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			if !restored.Resume(id) {
				t.Fatal("restored task could not resume")
			}
			waitFor(t, restored, id, Done)
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
			if seenFirst != corrupt {
				t.Fatalf("unexpected ranges for modified=%v: %v", corrupt, ranges)
			}
		})
	}
}

func TestCredentialsAreNotPersisted(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	id := newID()
	task := &Task{id: id, url: "https://user:secret@example.test/file", headers: map[string]string{"Cookie": "session=secret", "Authorization": "Bearer secret"}, directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{})}
	task.state.Store(Queued)
	task.held.Store(true)
	m.tasks = append(m.tasks, task)
	if err := m.saveTask(task); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret")) {
		t.Fatal("credentials written to journal")
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	if restored.Resume(id) {
		t.Fatal("task resumed without its credentials")
	}
}

func TestRedownloadDoesNotBypassOmittedCredentials(t *testing.T) {
	for _, state := range []State{Done, ErrorS} {
		t.Run(string(state), func(t *testing.T) {
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			id := newID()
			task := &Task{id: id, url: "https://example.test/file", headers: map[string]string{"Cookie": "session=secret"}, directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{})}
			task.state.Store(state)
			if err := m.saveTask(task); err != nil {
				t.Fatal(err)
			}
			restored := NewManager(dir, 1, 1)
			if err := restored.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			if newID, err := restored.Redownload(id); err == nil || newID != "" {
				t.Fatalf("redownload bypassed missing credentials: id=%q err=%v", newID, err)
			}
			snapshots := restored.Snapshots()
			if len(snapshots) != 1 || snapshots[0].ID != id {
				t.Fatalf("original task was removed: %+v", snapshots)
			}
		})
	}
}

func TestCompletionOptionSurvivesTaskRestore(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	id := newID()
	task := &Task{id: id, url: "https://example.test/file", directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), openFolder: true}
	task.state.Store(Queued)
	task.held.Store(true)
	m.tasks = append(m.tasks, task)
	if err := m.saveTask(task); err != nil {
		t.Fatal(err)
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	req, ok := restored.Request(id)
	if !ok || !req.OpenFolder {
		t.Fatal("completion action was not restored")
	}
	restored.tasks[0].state.Store(Done)
	if !restored.ClaimOpenFolder(id) || restored.ClaimOpenFolder(id) {
		t.Fatal("completion action was not claimed exactly once")
	}
	if err := restored.saveTask(restored.tasks[0]); err != nil {
		t.Fatal(err)
	}
	again := NewManager(dir, 1, 1)
	if err := again.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	if again.ClaimOpenFolder(id) {
		t.Fatal("old completed task reopened its folder")
	}
}

func TestNightQueuePersistsAndMovesWithoutReplacingTask(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	id := newID()
	task := &Task{id: id, url: "https://example.test/file", directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), openFolder: true}
	task.state.Store(Queued)
	task.held.Store(true)
	task.night.Store(true)
	m.tasks = append(m.tasks, task)
	if err := m.saveTask(task); err != nil {
		t.Fatal(err)
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	snaps := restored.Snapshots()
	if len(snaps) != 1 || snaps[0].ID != id || !snaps[0].Night || !snaps[0].Held || !snaps[0].OpenFolder {
		t.Fatalf("night task not restored: %+v", snaps)
	}
	if !restored.MoveToMain(id, false) {
		t.Fatal("could not move task")
	}
	snaps = restored.Snapshots()
	if len(snaps) != 1 || snaps[0].ID != id || snaps[0].Night || !snaps[0].Held {
		t.Fatalf("task identity changed on move: %+v", snaps)
	}
}

func TestRecoverPublicationAfterInterruption(t *testing.T) {
	for _, mode := range []string{"matching", "changed", "collision"} {
		t.Run(mode, func(t *testing.T) {
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			data := []byte("published bytes")
			sum := sha256.Sum256(data)
			path := filepath.Join(dir, "file.bin")
			finalData := data
			if mode == "changed" {
				finalData = []byte("different bytes")
			}
			if err := os.WriteFile(path, finalData, 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "collision" {
				if err := os.WriteFile(path+".part", data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			id := newID()
			task := &Task{id: id, url: "https://example.test/file", directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), filePath: path,
				publishSHA256: hex.EncodeToString(sum[:]), publishSize: int64(len(data)), openFolder: true}
			task.state.Store(Running)
			m.tasks = append(m.tasks, task)
			if err := m.saveTask(task); err != nil {
				t.Fatal(err)
			}
			restored := NewManager(dir, 1, 1)
			if err := restored.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var snap Snapshot
			for time.Now().Before(deadline) {
				list := restored.Snapshots()
				if len(list) == 1 {
					snap = list[0]
					if !snap.Verifying {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if mode == "matching" {
				if snap.State != Done || snap.Done != int64(len(data)) || restored.ClaimOpenFolder(id) {
					t.Fatalf("completion not recovered safely: %+v", snap)
				}
			} else if snap.State != ErrorS || snap.Verifying {
				t.Fatalf("unsafe completion accepted: %+v", snap)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, finalData) {
				t.Fatalf("existing file changed: %v", err)
			}
		})
	}
}
