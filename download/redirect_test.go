package download

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRedirectStripsCrossOriginCredentials(t *testing.T) {
	received := make(chan http.Header, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/file", http.StatusFound)
	}))
	defer source.Close()
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{url: source.URL + "/private?token=secret", cancel: make(chan struct{}), headers: map[string]string{
		"Authorization": "Bearer secret", "Cookie": "session=secret", "X-Api-Key": "secret", "User-Agent": "test-agent",
	}}
	req, cancel, err := task.newRequest(task.url)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("If-Range", `"v1"`)
	resp, err := m.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := <-received
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if h.Get(name) != "" {
			t.Fatalf("%s leaked to another origin", name)
		}
	}
	if h.Get("Range") != "bytes=0-9" || h.Get("If-Range") != `"v1"` || h.Get("User-Agent") != "test-agent" {
		t.Fatalf("download headers not preserved: %v", h)
	}
	if h.Get("Referer") != source.URL+"/" || strings.Contains(h.Get("Referer"), "secret") {
		t.Fatalf("unsafe referer: %q", h.Get("Referer"))
	}
}

func TestRedirectTargetSharesCooldown(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer target.Close()
	redirect := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/file", http.StatusFound)
		}))
	}
	first, second := redirect(), redirect()
	defer first.Close()
	defer second.Close()
	m := NewManager(t.TempDir(), 1, 1)
	for _, address := range []string{first.URL, target.URL, second.URL} {
		task := &Task{cancel: make(chan struct{})}
		req, cancel, err := task.newRequest(address)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.doWithRetry(task, req)
		cancel()
		if err == nil {
			t.Fatalf("expected cooldown error for %s", address)
		}
	}
	if got := targetRequests.Load(); got != 1 {
		t.Fatalf("target received %d requests despite cooldown", got)
	}
}

func TestSameOriginRedirectKeepsCredentials(t *testing.T) {
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/file", http.StatusFound)
			return
		}
		received <- r.Header.Get("Authorization")
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{url: srv.URL + "/start", cancel: make(chan struct{}), headers: map[string]string{"Authorization": "Bearer secret"}}
	req, cancel, err := task.newRequest(task.url)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	resp, err := m.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-received; got != "Bearer secret" {
		t.Fatalf("same-origin authorization lost: %q", got)
	}
}
