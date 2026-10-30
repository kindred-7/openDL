package download

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseHLSTSRejectsUnsupportedPlaylists(t *testing.T) {
	base, _ := url.Parse("https://example.test/a/list.m3u8")
	valid := "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:5,\nfirst.ts\n#EXTINF:5,\n../second.ts\n#EXT-X-ENDLIST\n"
	got, err := parseHLSTS([]byte(valid), base)
	want := []hlsSegment{{url: "https://example.test/a/first.ts"}, {url: "https://example.test/second.ts"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("segments=%v err=%v", got, err)
	}
	for _, body := range []string{
		"#EXTM3U\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-BYTERANGE:188\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=10\nchild.m3u8\n",
	} {
		if _, err := parseHLSTS([]byte(body), base); err == nil {
			t.Fatalf("unsupported playlist accepted: %s", body)
		}
	}
}

func TestParseHLSTSAcceptsTimelineMetadata(t *testing.T) {
	base, _ := url.Parse("https://example.test/list.m3u8")
	metadata := "#EXTM3U\n#EXT-X-ALLOW-CACHE:YES\n#EXT-X-DISCONTINUITY-SEQUENCE:2\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2026-01-01T00:00:00Z\n" +
		"#EXT-X-DATERANGE:ID=\"event\",START-DATE=\"2026-01-01T00:00:00Z\"\n" +
		"#EXTINF:1,\npart.ts\n#EXT-X-ENDLIST\n"
	got, err := parseHLSTS([]byte(metadata), base)
	if err != nil || !reflect.DeepEqual(got, []hlsSegment{{url: "https://example.test/part.ts"}}) {
		t.Fatalf("metadata playlist: segments=%v err=%v", got, err)
	}

}

func TestHLSResumeValidatesSavedSegments(t *testing.T) {
	for _, scenario := range []string{"intact", "corrupt", "changed", "no-validator"} {
		t.Run(scenario, func(t *testing.T) {
			first := make([]byte, 188)
			first[0], first[1] = 0x47, 1
			second := make([]byte, 188)
			second[0], second[1] = 0x47, 2
			var full, probes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/list.m3u8":
					fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n#EXT-X-ENDLIST\n")
				case "/a.ts":
					if r.Header.Get("Range") == "" {
						full.Add(1)
					} else {
						probes.Add(1)
					}
					if scenario != "no-validator" {
						etag := `"v1"`
						if scenario == "changed" {
							etag = `"v2"`
						}
						w.Header().Set("ETag", etag)
					}
					http.ServeContent(w, r, "a.ts", time.Time{}, bytes.NewReader(first))
				case "/b.ts":
					http.ServeContent(w, r, "b.ts", time.Time{}, bytes.NewReader(second))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			dir, store := t.TempDir(), t.TempDir()
			m := NewManager(dir, 1, 1)
			if err := m.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			id := newID()
			task := &Task{id: id, url: srv.URL + "/list.m3u8", directory: dir, conns: 1, mediaMode: "hls-ts", created: time.Now(), cancel: make(chan struct{})}
			task.state.Store(Queued)
			task.held.Store(true)
			m.tasks = append(m.tasks, task)
			path := filepath.Join(dir, "video.ts")
			if err := os.WriteFile(path+".part", first, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path + ".part")
			if err != nil {
				t.Fatal(err)
			}
			digest, err := chunkDigest(f, chunkSpan{start: 0, end: 187})
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			resource, _ := url.Parse(srv.URL + "/a.ts")
			segments := []hlsSegment{{url: resource.String()}, {url: srv.URL + "/b.ts"}}
			validator := `"v1"`
			if scenario == "no-validator" {
				validator = ""
			}
			task.metaMu.Lock()
			task.filePath = path
			task.metaMu.Unlock()
			task.checkpointMu.Lock()
			task.hlsPlaylist = hlsPlaylistFingerprint(segments)
			task.hlsCompleted = []hlsCheckpoint{{End: 188, SHA256: digest, Resource: resourceFingerprint(resource), ResourceTotal: 188, Validator: validator}}
			err = m.saveTaskLocked(task)
			task.checkpointMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt" {
				modified := append([]byte(nil), first...)
				modified[10] = 99
				if err := os.WriteFile(path+".part", modified, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			restored := NewManager(dir, 1, 1)
			if err := restored.EnablePersistence(store); err != nil {
				t.Fatal(err)
			}
			if !restored.Resume(id) {
				t.Fatal("restored HLS task could not resume")
			}
			waitFor(t, restored, id, Done)
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, append(first, second...)) {
				t.Fatalf("output mismatch: %v", err)
			}
			if scenario == "intact" {
				if full.Load() != 0 || probes.Load() != 1 {
					t.Fatalf("intact segment not reused: full=%d probes=%d", full.Load(), probes.Load())
				}
			} else if full.Load() != 1 {
				t.Fatalf("unsafe segment was not downloaded again: full=%d", full.Load())
			}
		})
	}
}

func TestHLSByteRangesDownloadAndValidate(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	data := append(append([]byte{}, packet...), packet...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:188@0\nfile.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:188\nfile.ts\n#EXT-X-ENDLIST\n")
		case "/file.ts":
			w.Header().Set("ETag", `"stable"`)
			http.ServeContent(w, r, "file.ts", time.Unix(1700000000, 0), bytes.NewReader(data))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("range output mismatch: %v", err)
	}
}

func TestHLSByteRangesRejectChangedOrUnverifiableResource(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	data := bytes.Repeat(packet, 2)
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/list.m3u8" {
					fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:188@0\nfile.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:188\nfile.ts\n#EXT-X-ENDLIST\n")
					return
				}
				count := requests.Add(1)
				if changed {
					etag := `"first"`
					if count > 1 {
						etag = `"second"`
					}
					w.Header().Set("ETag", etag)
				}
				http.ServeContent(w, r, "file.ts", time.Time{}, bytes.NewReader(data))
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 1)
			id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
			s := waitFor(t, m, id, ErrorS)
			if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
				t.Fatalf("unsafe output was published: %v", err)
			}
			if changed && requests.Load() < 2 {
				t.Fatal("changed resource was not requested")
			}
			if !changed && requests.Load() != 1 {
				t.Fatal("unverifiable resource was requested again")
			}
		})
	}
}

func TestHLSByteRangesRejectMissingLaterValidator(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/list.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:188@0\nfile.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:188@188\nfile.ts\n#EXT-X-ENDLIST\n")
			return
		}
		if requests.Add(1) == 1 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Range", "bytes 0-187/376")
		} else {
			// Simulate a server that ignores If-Range and omits its validator.
			w.Header().Set("Content-Range", "bytes 188-375/376")
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(packet)
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, ErrorS)
	if requests.Load() != 2 {
		t.Fatalf("expected both range requests, got %d", requests.Load())
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatalf("unverified ranges were published: %v", err)
	}
}

func TestHLSRetriesInterruptedSegmentWithoutRepeatingEarlierSegments(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	var firstRequests, secondRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n#EXT-X-ENDLIST\n")
		case "/a.ts":
			firstRequests.Add(1)
			_, _ = w.Write(packet)
		case "/b.ts":
			if secondRequests.Add(1) == 1 {
				w.Header().Set("Content-Length", "376")
			}
			_, _ = w.Write(packet)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, bytes.Repeat(packet, 2)) {
		t.Fatalf("retry output mismatch: %v", err)
	}
	if firstRequests.Load() != 1 || secondRequests.Load() != 2 {
		t.Fatalf("unexpected requests: first=%d second=%d", firstRequests.Load(), secondRequests.Load())
	}
}

func TestHLSTSTransferPublishesConcatenatedPackets(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	data := bytes.Repeat(packet, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n#EXT-X-ENDLIST\n")
		case "/a.ts", "/b.ts":
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, 1, 1)
	id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, append(append([]byte{}, data...), data...)) || strings.ToLower(filepath.Ext(s.FilePath)) != ".ts" {
		t.Fatalf("incorrect HLS output: size=%d error=%v", len(got), err)
	}
}

func TestHLSTSRejectsNonTransportStream(t *testing.T) {
	var out bytes.Buffer
	w := &tsWriter{w: &out}
	if _, err := w.Write([]byte("<html>")); err == nil || out.Len() != 0 {
		t.Fatal("non-TS response was written")
	}
}

func TestHLSSegmentDoesNotLeakCrossOriginCookie(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	seen := make(chan string, 1)
	segment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Cookie")
		_, _ = w.Write(packet)
	}))
	defer segment.Close()
	playlist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:1,\n%s/segment.ts\n#EXT-X-ENDLIST\n", segment.URL)
	}))
	defer playlist.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: playlist.URL + "/list.m3u8", MediaMode: "hls-ts", Headers: map[string]string{"Cookie": "session=secret"}})
	waitFor(t, m, id, Done)
	if value := <-seen; value != "" {
		t.Fatal("cookie leaked to a different origin")
	}
}

func TestHLSMasterSelectsCompatibleMuxedVariant(t *testing.T) {
	packet := make([]byte, 188)
	packet[0] = 0x47
	var external, unsupported, selected atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"ext\",NAME=\"Audio\",URI=\"audio.m3u8\"\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=4000,AUDIO=\"ext\"\nexternal.m3u8\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=3000,CODECS=\"avc1,mp4a\"\nunsupported.m3u8\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=1000\nselected.m3u8\n")
		case "/external.m3u8":
			external.Add(1)
		case "/unsupported.m3u8":
			unsupported.Add(1)
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key\"\n#EXTINF:1,\na.m4s\n#EXT-X-ENDLIST\n")
		case "/selected.m3u8":
			selected.Add(1)
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\nfile.ts\n#EXT-X-ENDLIST\n")
		case "/file.ts":
			_, _ = w.Write(packet)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/master.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, packet) || external.Load() != 0 || unsupported.Load() != 1 || selected.Load() != 1 {
		t.Fatalf("unexpected variant selection: external=%d unsupported=%d selected=%d err=%v", external.Load(), unsupported.Load(), selected.Load(), err)
	}
}

func TestHLSMasterRejectsMissingAudioGroupAndCycles(t *testing.T) {
	base, _ := url.Parse("https://example.test/master.m3u8")
	_, master, err := parseHLSMaster([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,AUDIO=\"missing\"\nvariant.m3u8\n"), base)
	if !master || err == nil {
		t.Fatal("missing audio group was accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\n/master.m3u8\n")
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/master.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, ErrorS)
	if !strings.Contains(s.Err, "循环") {
		t.Fatalf("cycle not reported: %s", s.Err)
	}
}
