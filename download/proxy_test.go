package download

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyConfigurationRejectsInvalidEndpointsAndSecrets(t *testing.T) {
	for _, address := range []string{"ftp://proxy.test", "http://user:secret@proxy.test", "http://proxy.test/path", "http://proxy.test?token=secret", "http://proxy.test:0", "http://proxy.test:65536", "http://proxy.test:", "http://a..test", "http://proxy.test/#secret"} {
		_, err := NormalizeProxyConfig(ProxyConfig{Mode: "manual", Address: address})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe endpoint handling: address=%q err=%v", address, err)
		}
	}
	config, err := NormalizeProxyConfig(ProxyConfig{Mode: "manual", Address: "socks5://[2001:db8::1]", Bypass: "EXAMPLE.test; *.internal.test, 192.168.1.8/24, [2001:db8::2]:443"})
	if err != nil || config.Address != "socks5://[2001:db8::1]:1080" || config.Bypass != "example.test,.internal.test,192.168.1.0/24,[2001:db8::2]:443" {
		t.Fatalf("normalization=%+v err=%v", config, err)
	}
	for _, bypass := range []string{"https://example.test/path", "example.test:wrong", "*.192.168.1.1", "192.168.1.0/99", "example.*", "[2001:db8::1]:0"} {
		if _, err := NormalizeProxyConfig(ProxyConfig{Mode: "manual", Address: "proxy.test:8080", Bypass: bypass}); err == nil {
			t.Fatalf("invalid bypass accepted: %q", bypass)
		}
	}
	data, err := json.Marshal(ProxyConfig{Mode: "manual", Address: "http://proxy.test:80", Username: "name", Password: "private-value"})
	if err != nil || bytes.Contains(data, []byte("private-value")) || bytes.Contains(data, []byte("password")) {
		t.Fatalf("password serialized: %s err=%v", data, err)
	}
}

func TestManualProxyBypassAndImmutableTransport(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	p := &proxyTransport{base: base, current: base}
	config := ProxyConfig{Mode: "manual", Address: "http://proxy.example.test:8080", Bypass: "example.test,.internal.test,10.0.0.0/8,service.test:443", Username: "proxy-user", Password: "proxy-pass"}
	if err := p.set(config); err != nil {
		t.Fatal(err)
	}
	first := p.current.(*http.Transport)
	for _, item := range []struct {
		url    string
		direct bool
	}{
		{"https://example.test/file", true}, {"https://cdn.example.test/file", true},
		{"https://sub.internal.test/file", true}, {"https://internal.test/file", false},
		{"http://10.2.3.4/file", true}, {"https://service.test/file", true},
		{"http://service.test/file", false}, {"http://127.0.0.1/file", true},
		{"https://download.test/file", false},
	} {
		req, _ := http.NewRequest(http.MethodGet, item.url, nil)
		proxy, err := first.Proxy(req)
		if err != nil || (proxy == nil) != item.direct {
			t.Fatalf("proxy selection for %s: proxy=%v err=%v", item.url, proxy, err)
		}
		if proxy != nil {
			password, _ := proxy.User.Password()
			if proxy.User.Username() != "proxy-user" || password != "proxy-pass" {
				t.Fatal("proxy credentials were lost")
			}
		}
	}
	if err := p.set(ProxyConfig{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if p.current == first || p.current.(*http.Transport).Proxy != nil || first.Proxy == nil {
		t.Fatal("updating the proxy mutated the earlier transport")
	}
	if err := p.set(ProxyConfig{Mode: "manual", Address: "not valid"}); err == nil || p.current.(*http.Transport).Proxy != nil {
		t.Fatal("invalid configuration replaced the active direct route")
	}
	p.CloseIdleConnections()
}

func TestHTTPProxyDownloadSeparatesOriginAndProxyAuthentication(t *testing.T) {
	body := bytes.Repeat([]byte("download"), 200)
	var requests, incorrect atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-pass"))
		if req.URL.Host != "download.example.test" || req.Header.Get("Proxy-Authorization") != want || req.Header.Get("Authorization") != "Bearer origin-session" {
			incorrect.Add(1)
		}
		w.Header().Set("ETag", `"stable"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	manager := NewManager(t.TempDir(), 1, 1)
	if err := manager.SetProxyConfig(ProxyConfig{Mode: "manual", Address: proxy.URL, Username: "proxy-user", Password: "proxy-pass"}); err != nil {
		t.Fatal(err)
	}
	id := manager.Add(Request{URL: "http://download.example.test/file.bin", Headers: map[string]string{"Authorization": "Bearer origin-session", "Proxy-Authorization": "must-not-be-forwarded"}})
	snapshot := waitFor(t, manager, id, Done)
	got, err := os.ReadFile(snapshot.FilePath)
	if err != nil || !bytes.Equal(got, body) || incorrect.Load() != 0 || requests.Load() == 0 {
		t.Fatalf("proxy download: requests=%d incorrect=%d err=%v", requests.Load(), incorrect.Load(), err)
	}
}

func TestProxyAuthenticationFailureDoesNotRetryOrFallBack(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var calls atomic.Int64
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
				w.WriteHeader(http.StatusProxyAuthRequired)
			}))
			defer proxy.Close()
			m := NewManager(t.TempDir(), 1, 1)
			if err := m.SetProxyConfig(ProxyConfig{Mode: "manual", Address: proxy.URL}); err != nil {
				t.Fatal(err)
			}
			task := &Task{cancel: make(chan struct{})}
			req, _ := http.NewRequest(http.MethodGet, scheme+"://remote.example.test/file", nil)
			_, err := m.doWithRetry(task, req)
			var auth *proxyAuthenticationError
			if !errors.As(err, &auth) || transientError(err) || calls.Load() != 1 {
				t.Fatalf("proxy auth error=%v requests=%d", err, calls.Load())
			}
		})
	}
}

func TestBlockedProxyCanOnlyBeClearedByValidConfiguration(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 1)
	m.BlockProxy("saved credentials unavailable")
	req, _ := http.NewRequest(http.MethodGet, "https://download.example.test/file", nil)
	_, err := m.proxy.RoundTrip(req)
	var configError *proxyConfigurationError
	if !errors.As(err, &configError) || transientError(err) {
		t.Fatalf("configuration failure: %v", err)
	}
	if err := m.SetProxyConfig(ProxyConfig{Mode: "manual"}); err == nil {
		t.Fatal("invalid configuration was accepted")
	}
	_, err = m.proxy.RoundTrip(req)
	if !errors.As(err, &configError) {
		t.Fatal("invalid update removed proxy block")
	}
	if err := m.SetProxyConfig(ProxyConfig{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if m.proxy.blocked != nil {
		t.Fatal("valid update did not remove proxy block")
	}
}

func TestProxyHeaderNotForwardedToDirectOrigin(t *testing.T) {
	var seen string
	base := originRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Get("Proxy-Authorization")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	p := &proxyTransport{base: base, current: base}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/file", nil)
	req.Header.Set("Proxy-Authorization", "Basic private-proxy-value")
	resp, err := p.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen != "" || req.Header.Get("Proxy-Authorization") != "Basic private-proxy-value" {
		t.Fatal("proxy header leaked or input request was mutated")
	}
}
