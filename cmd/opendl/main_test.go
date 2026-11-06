package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"openDL/download"
)

func serveFile(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=\"report.pdf\"")
		w.Header().Set("Content-Length", "2048")
		_, _ = w.Write(make([]byte, 2048))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitDone(t *testing.T, m *download.Manager, id string) download.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.Snapshots() {
			if s.ID == id && s.State == download.Done {
				return s
			}
			if s.ID == id && s.State == download.ErrorS {
				t.Fatalf("task failed: %s", s.Err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timeout")
	return download.Snapshot{}
}

// The theme persists between light (default) and dark.
func TestThemeToggle(t *testing.T) {
	themeOverride = filepath.Join(t.TempDir(), "theme")
	if loadTheme() {
		t.Fatal("default theme must be light")
	}
	saveTheme(true)
	if loadTheme() != true {
		t.Fatal("dark not set or not persisted")
	}
	saveTheme(false)
	if loadTheme() != false {
		t.Fatal("light not restored")
	}
}
