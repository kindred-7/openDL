package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDigestChallengeParsingAndSelection(t *testing.T) {
	values := []string{`Basic realm="simple", Digest realm = "a,b", nonce="n", qop="auth,auth-int", algorithm=SHA-256`, `Digest realm="r", nonce="n", qop="unsupported", algorithm=MD5`}
	challenges := digestChallenges(values)
	if len(challenges) != 1 || challenges[0].params["realm"] != "a,b" || challenges[0].algorithm != "SHA-256" {
		t.Fatalf("unexpected challenges: %+v", challenges)
	}
}

func TestDigestRejectsEmptyQualityOfProtection(t *testing.T) {
	for _, value := range []string{
		`Digest realm="r", nonce="n", qop=""`,
		`Digest realm="r", nonce="n", qop="unsupported"`,
		"Digest realm=\"r\", nonce=bad\x01value, qop=\"auth\"",
	} {
		if got := digestChallenges([]string{value}); len(got) != 0 {
			t.Fatalf("invalid challenge accepted: %q", value)
		}
	}
}

func TestDigestAuthIntResponseIntegrity(t *testing.T) {
	for _, mode := range []string{"valid", "invalid", "absent", "trailer"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("authenticated file")
			digest := func(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Digest ") {
					w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n", qop="auth-int", algorithm=SHA-256`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				fields := make(map[string]string)
				for _, part := range authParts(strings.TrimPrefix(auth, "Digest ")) {
					key, value, ok := strings.Cut(part, "=")
					if ok {
						fields[strings.TrimSpace(key)], _ = digestUnquote(value)
					}
				}
				a1 := digest("user:r:password")
				requestA2 := digest("GET:" + r.URL.RequestURI() + ":" + digest(""))
				requestHash := digest(fmt.Sprintf("%s:n:%s:%s:auth-int:%s", a1, fields["nc"], fields["cnonce"], requestA2))
				if fields["qop"] != "auth-int" || fields["response"] != requestHash {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				var responseInfo string
				if mode != "absent" {
					responseA2 := digest(":" + r.URL.RequestURI() + ":" + digest(string(data)))
					responseHash := digest(fmt.Sprintf("%s:n:%s:%s:auth-int:%s", a1, fields["nc"], fields["cnonce"], responseA2))
					if mode == "invalid" {
						responseHash = strings.Repeat("0", 64)
					}
					responseInfo = fmt.Sprintf(`qop=auth-int, nc=%s, cnonce="%s", rspauth="%s"`, fields["nc"], fields["cnonce"], responseHash)
					if mode == "trailer" {
						w.Header().Set("Trailer", "Authentication-Info")
					} else {
						w.Header().Set("Authentication-Info", responseInfo)
					}
				}
				_, _ = w.Write(data)
				if mode == "trailer" {
					w.Header().Set("Authentication-Info", responseInfo)
				}
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 1)
			header := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:password"))
			id := m.Add(Request{URL: srv.URL + "/file", Headers: map[string]string{"Authorization": header}})
			want := Done
			if mode == "invalid" {
				want = ErrorS
			}
			snap := waitFor(t, m, id, want)
			if mode == "invalid" {
				if !strings.Contains(snap.Err, "完整性") {
					t.Fatalf("integrity error not reported: %s", snap.Err)
				}
				if _, err := os.Stat(snap.FilePath); !os.IsNotExist(err) {
					t.Fatalf("invalid response was published: %v", err)
				}
			} else {
				got, err := os.ReadFile(snap.FilePath)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("download mismatch: %v", err)
				}
			}
		})
	}
}

func TestDigestRetriesStaleNonceOnce(t *testing.T) {
	for _, neverAccept := range []bool{false, true} {
		t.Run(fmt.Sprintf("always-stale=%t", neverAccept), func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := requests.Add(1)
				if count == 1 {
					w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="first", qop="auth"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if count == 2 || neverAccept {
					w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="fresh", qop="auth", stale=true`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if !strings.Contains(r.Header.Get("Authorization"), `nonce="fresh"`) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_, _ = w.Write([]byte("ok"))
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 1)
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			req.SetBasicAuth("user", "password")
			resp, err := m.doWithRetry(&Task{}, req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if (err != nil) != neverAccept || requests.Load() != 3 {
				t.Fatalf("stale retry: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestDigestAuthenticationForRangedDownload(t *testing.T) {
	data := bytes.Repeat([]byte("digest-data!"), 65536)
	var challenges, authenticated atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			challenges.Add(1)
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="fixed-nonce", qop="auth", algorithm=SHA-256`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fields := make(map[string]string)
		for _, part := range authParts(strings.TrimPrefix(auth, "Digest ")) {
			key, value, ok := strings.Cut(part, "=")
			if ok {
				fields[strings.TrimSpace(key)], _ = digestUnquote(value)
			}
		}
		digest := func(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
		a1 := digest("user:test:password")
		a2 := digest("GET:" + r.URL.RequestURI())
		want := digest(fmt.Sprintf("%s:fixed-nonce:%s:%s:auth:%s", a1, fields["nc"], fields["cnonce"], a2))
		if fields["username"] != "user" || fields["uri"] != r.URL.RequestURI() || fields["qop"] != "auth" || fields["response"] != want {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		authenticated.Add(1)
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "file.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 2)
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:password"))
	id := m.Add(Request{URL: srv.URL + "/file.bin?part=1", Headers: map[string]string{"Authorization": header}, Connections: 2})
	snap := waitFor(t, m, id, Done)
	got, err := os.ReadFile(snap.FilePath)
	if err != nil || !bytes.Equal(got, data) || challenges.Load() < 1 || authenticated.Load() < 2 {
		t.Fatalf("digest download: challenges=%d authenticated=%d err=%v", challenges.Load(), authenticated.Load(), err)
	}
}

func TestDigestDoesNotSendCredentialsToCrossOriginChallenge(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="other", nonce="n", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	m := NewManager(t.TempDir(), 1, 1)
	req, _ := http.NewRequest(http.MethodGet, source.URL, nil)
	req.SetBasicAuth("user", "password")
	_, err := m.doWithRetry(&Task{}, req)
	if err == nil || leaked.Load() {
		t.Fatalf("cross-origin challenge: error=%v leaked=%v", err, leaked.Load())
	}
}

func TestDigestCacheReusesChallengeAndRefreshesStaleNonce(t *testing.T) {
	var nonce atomic.Int64
	var challenges, otherDigest atomic.Int64
	nonce.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/other" {
			if strings.HasPrefix(auth, "Digest ") {
				otherDigest.Add(1)
			}
			_, _ = w.Write([]byte("other"))
			return
		}
		current := fmt.Sprintf("nonce-%d", nonce.Load())
		if !strings.HasPrefix(auth, "Digest ") || !strings.Contains(auth, `nonce="`+current+`"`) {
			challenges.Add(1)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="r", nonce="%s", qop="auth", stale=true`, current))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	task := &Task{}
	request := func(path string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("user", "password")
		resp, err := m.doWithRetry(task, req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	request("/file?x=1")
	request("/file?x=1")
	if challenges.Load() != 1 {
		t.Fatalf("challenge not reused: %d", challenges.Load())
	}
	request("/other")
	if otherDigest.Load() != 0 {
		t.Fatal("cached authentication was sent to another resource")
	}
	nonce.Store(2)
	request("/file?x=1")
	request("/file?x=1")
	if challenges.Load() != 2 {
		t.Fatalf("stale challenge not refreshed: %d", challenges.Load())
	}
}
