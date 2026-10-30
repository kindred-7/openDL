package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestStrongValidatorSelection(t *testing.T) {
	date := time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, etag string
		modified   time.Time
		date       string
		want       string
	}{
		{"strong etag", `"v1"`, date.Add(-time.Hour), date.Format(http.TimeFormat), `"v1"`},
		{"weak etag", `W/"v1"`, date.Add(-time.Hour), date.Format(http.TimeFormat), ""},
		{"malformed etag", `v1`, date.Add(-time.Hour), date.Format(http.TimeFormat), ""},
		{"old date", "", date.Add(-2 * time.Minute), date.Format(http.TimeFormat), date.Add(-2 * time.Minute).Format(http.TimeFormat)},
		{"recent date", "", date.Add(-10 * time.Second), date.Format(http.TimeFormat), ""},
		{"missing date", "", date.Add(-time.Hour), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			if tc.etag != "" {
				h.Set("ETag", tc.etag)
			}
			h.Set("Last-Modified", tc.modified.Format(http.TimeFormat))
			if tc.date != "" {
				h.Set("Date", tc.date)
			}
			if got := strongValidator(h); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnvalidatedResourceUsesSingleStreamUnlessChecksumProvided(t *testing.T) {
	data := bytes.Repeat([]byte("payload!"), (512<<10)/8)
	sum := sha256.Sum256(data)
	for _, useChecksum := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-checksum", true: "with-checksum"}[useChecksum], func(t *testing.T) {
			var ranges atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Accept-Ranges", "bytes")
				if r.Header.Get("Range") != "" {
					ranges.Add(1)
				}
				http.ServeContent(w, r, "file.bin", time.Time{}, bytes.NewReader(data))
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 2)
			req := Request{URL: srv.URL, Name: "file.bin"}
			if useChecksum {
				req.SHA256 = hex.EncodeToString(sum[:])
			}
			id := m.Add(req)
			snap := waitFor(t, m, id, Done)
			got, err := os.ReadFile(snap.FilePath)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("file mismatch: %v", err)
			}
			if (ranges.Load() > 0) != useChecksum || snap.ResumeSafe {
				t.Fatalf("unexpected range/resume state: ranges=%d safe=%v", ranges.Load(), snap.ResumeSafe)
			}
		})
	}
}
