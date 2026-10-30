package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"openDL/download"
)

func TestTokenFileRoundTrip(t *testing.T) {
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
