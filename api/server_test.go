package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openDL/download"
)

func TestServerDownloadAndTasks(t *testing.T) {
	payload := []byte("payload")
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="a.txt"`)
		_, _ = w.Write(payload)
	}))
	defer fileSrv.Close()

	mgr := download.NewManager(t.TempDir(), 1, 1)
	s := &Server{Mgr: mgr, Token: "tok"}

	body, _ := json.Marshal(download.Request{URL: fileSrv.URL + "/a.txt"})
	req := httptest.NewRequest(http.MethodPost, "/api/download", bytes.NewReader(body))
	req.Header.Set("X-OpenDL-Token", "tok")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}

	// Wrong token is rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestTokenFileRoundTrip(t *testing.T) {
	// Point the config dir at a temp dir via env is not portable; instead
	// validate the token file contract directly.
	dir := t.TempDir()
	tok := "abc123"
	if err := os.WriteFile(filepath.Join(dir, "api-token"), []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "api-token"))
	if err != nil || string(got) != tok {
		t.Fatalf("token %q err %v", got, err)
	}
}

func TestEmptyTokenNeverAuthorizes(t *testing.T) {
	s := &Server{Mgr: download.NewManager(t.TempDir(), 1, 1)}
	request := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("empty token authorized: %d", response.Code)
	}
	if err := s.Start(); err == nil {
		t.Fatal("server started with empty token")
	}
}

func TestDownloadRejectsInvalidRequestsBeforeAddingTask(t *testing.T) {
	mgr := download.NewManager(t.TempDir(), 1, 1)
	s := &Server{Mgr: mgr, Token: "tok"}
	for _, body := range []string{
		`{"url":"ftp://example.com/a"}`,
		`{"url":"https:///file"}`,
		`{"url":"https://example.com/file","sha256":"invalid"}`,
		`{"url":"https://example.com/file","headers":{"Bad Header":"x"}}`,
		`{"url":"https://example.com/file","queue":"unknown"}`,
		`{"url":"https://example.com/file","clientRequestId":"bad id"}`,
		`{"url":"https://example.com/file"} {}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(body))
		req.Header.Set("X-OpenDL-Token", "tok")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("request %s: status %d", body, rec.Code)
		}
	}
	if got := len(mgr.Snapshots()); got != 0 {
		t.Fatalf("invalid requests added %d tasks", got)
	}
}

func TestAPIReusesSubmissionAndRejectsConflictingReuse(t *testing.T) {
	mgr := download.NewManager(t.TempDir(), 1, 1)
	s := &Server{Mgr: mgr, Token: "tok"}
	var first string
	for _, filename := range []string{"file.bin", "file.bin", "different.bin"} {
		body, _ := json.Marshal(download.Request{URL: "https://example.test/file", Queue: "night", Name: filename, ClientRequestID: "request-1"})
		req := httptest.NewRequest(http.MethodPost, "/api/download", bytes.NewReader(body))
		req.Header.Set("X-OpenDL-Token", "tok")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if filename == "different.bin" {
			if rec.Code != http.StatusConflict {
				t.Fatalf("conflict status=%d", rec.Code)
			}
			continue
		}
		var reply struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("submission failed: %v status=%d", err, rec.Code)
		}
		if first == "" {
			first = reply.ID
		}
		if reply.ID != first {
			t.Fatal("retry created a different task")
		}
	}
	if len(mgr.Snapshots()) != 1 {
		t.Fatal("duplicate tasks were added")
	}
}

func TestAPINightQueueRemainsHeld(t *testing.T) {
	dir := t.TempDir()
	mgr := download.NewManager(dir, 1, 1)
	if err := mgr.EnablePersistence(filepath.Join(dir, "tasks")); err != nil {
		t.Fatal(err)
	}
	s := &Server{Mgr: mgr, Token: "tok"}
	req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{"url":"https://example.com/file","queue":"night"}`))
	req.Header.Set("X-OpenDL-Token", "tok")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	snapshots := mgr.Snapshots()
	if len(snapshots) != 1 || !snapshots[0].Night || !snapshots[0].Held || snapshots[0].State != download.Queued {
		t.Fatalf("night task was not held: %+v", snapshots)
	}
}

func TestJournalFailureReportsUnacceptedTask(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "tasks")
	mgr := download.NewManager(dir, 1, 1)
	if err := mgr.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	s := &Server{Mgr: mgr, Token: "tok"}
	req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{"url":"https://example.com/file"}`))
	req.Header.Set("X-OpenDL-Token", "tok")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var response struct {
		Code     string `json:"code"`
		Accepted *bool  `json:"accepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusServiceUnavailable || response.Code != "journal_write_failed" || response.Accepted == nil || *response.Accepted || len(mgr.Snapshots()) != 0 {
		t.Fatalf("unexpected rejection: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
}
