package download

import (
	"strings"
	"testing"
	"time"
)

func TestUpdateSourcePreservesOnlySameAddressProgress(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{id: "1234567890abcdef", url: "https://example.test/a", directory: t.TempDir(), conns: 1, created: time.Now(), cancel: make(chan struct{}),
		completed: []chunkSpan{{start: 0, end: 99}}, resumeTotal: 200, resumeValidator: `"v1"`}
	task.state.Store(ErrorS)
	task.needsAuth.Store(true)
	task.done.Store(100)
	task.total.Store(200)
	task.resetSegments(task.completed)
	m.tasks = append(m.tasks, task)
	changed, err := m.UpdateSource(task.id, task.url, map[string]string{"Cookie": "session=new"})
	if err != nil || changed || len(task.completed) != 1 || task.done.Load() != 100 || task.needsAuth.Load() {
		t.Fatalf("same address lost progress: changed=%v err=%v", changed, err)
	}
	changed, err = m.UpdateSource(task.id, "https://example.test/b", nil)
	if err != nil || !changed || len(task.completed) != 0 || task.done.Load() != 0 || task.total.Load() != 0 || len(task.segmentSnapshots()) != 0 {
		t.Fatalf("changed address retained old progress: changed=%v err=%v", changed, err)
	}
	task.state.Store(Running)
	if _, err := m.UpdateSource(task.id, "https://example.test/c", nil); err == nil {
		t.Fatal("running task accepted source change")
	}
}

func TestUpdateCredentialsPreservesCompletedPublication(t *testing.T) {
	for _, scenario := range []string{"credentials", "matching-checksum", "checksum", "address"} {
		t.Run(scenario, func(t *testing.T) {
			m := NewManager(t.TempDir(), 1, 1)
			hash := strings.Repeat("a", 64)
			task := &Task{id: "1234567890abcdef", url: "https://example.test/a", directory: t.TempDir(), conns: 1,
				created: time.Now(), cancel: make(chan struct{}), publishSHA256: hash, publishSize: 123}
			task.state.Store(ErrorS)
			m.tasks = append(m.tasks, task)
			address, expected := task.url, ""
			if scenario == "address" {
				address = "https://example.test/b"
			}
			if scenario == "checksum" {
				expected = strings.Repeat("b", 64)
			} else if scenario == "matching-checksum" {
				expected = hash
			}
			_, err := m.UpdateSourceWithSHA256(task.id, address, map[string]string{"Cookie": "new-session"}, expected, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "credentials" || scenario == "matching-checksum" {
				if task.publishSHA256 != hash || task.publishSize != 123 {
					t.Fatal("credentials update discarded completed file")
				}
			} else if task.publishSHA256 != "" || task.publishSize != 0 {
				t.Fatal("changed source retained old publication")
			}
		})
	}
}

func TestUpdateSourceRejectsInvalidHeaders(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 1)
	if _, err := m.UpdateSource("missing", "https://example.test/a", map[string]string{"Bad Header": "value"}); err == nil {
		t.Fatal("invalid header accepted")
	}
}
