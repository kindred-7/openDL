package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic container fixtures exercise assembly and bounds, not codec decoding.
func hlsMP4TestBox(kind string, parts ...[]byte) []byte {
	data := make([]byte, 8)
	copy(data[4:], kind)
	for _, part := range parts {
		data = append(data, part...)
	}
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
	return data
}

func hlsMP4TestWords(values ...uint32) []byte {
	data := make([]byte, 4*len(values))
	for i, value := range values {
		binary.BigEndian.PutUint32(data[4*i:], value)
	}
	return data
}

func hlsMP4TestInit() []byte {
	mvhd, tkhd := make([]byte, 100), make([]byte, 84)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(tkhd[12:], 1)
	sample := make([]byte, 8)
	binary.BigEndian.PutUint16(sample[6:], 1)
	stsd := hlsMP4TestBox("stsd", hlsMP4TestWords(0, 1), hlsMP4TestBox("raw ", sample))
	stbl := hlsMP4TestBox("stbl", stsd,
		hlsMP4TestBox("stts", hlsMP4TestWords(0, 0)),
		hlsMP4TestBox("stsc", hlsMP4TestWords(0, 0)),
		hlsMP4TestBox("stco", hlsMP4TestWords(0, 0)),
		hlsMP4TestBox("stsz", hlsMP4TestWords(0, 0, 0)))
	dref := hlsMP4TestBox("dref", hlsMP4TestWords(0, 1), hlsMP4TestBox("url ", hlsMP4TestWords(1)))
	mdia := hlsMP4TestBox("mdia", hlsMP4TestBox("minf", hlsMP4TestBox("dinf", dref), stbl))
	moov := hlsMP4TestBox("moov", hlsMP4TestBox("mvhd", mvhd),
		hlsMP4TestBox("trak", hlsMP4TestBox("tkhd", tkhd), mdia),
		hlsMP4TestBox("mvex", hlsMP4TestBox("trex", hlsMP4TestWords(0, 1, 1, 1000, 4, 0))))
	return append(hlsMP4TestBox("ftyp", []byte("iso6"), hlsMP4TestWords(1), []byte("iso6mp41")), moov...)
}

func hlsMP4TestFragment(sequence uint32) []byte {
	traf := hlsMP4TestBox("traf", hlsMP4TestBox("tfhd", hlsMP4TestWords(0x020000, 1)),
		hlsMP4TestBox("tfdt", hlsMP4TestWords(0, (sequence-1)*1000)),
		hlsMP4TestBox("trun", hlsMP4TestWords(1, 1, 0)))
	moof := hlsMP4TestBox("moof", hlsMP4TestBox("mfhd", hlsMP4TestWords(0, sequence)), traf)
	pos := bytes.Index(moof, []byte("trun"))
	binary.BigEndian.PutUint32(moof[pos+12:], uint32(len(moof)+8))
	return append(moof, hlsMP4TestBox("mdat", []byte{byte(sequence), 2, 3, 4})...)
}

func TestParseHLSMP4InitializationAndRanges(t *testing.T) {
	base, _ := url.Parse("https://example.test/media/list.m3u8")
	text := "#EXTM3U\n#EXT-X-MAP:URI=\"file.mp4\",BYTERANGE=\"64@0\"\n" +
		"#EXTINF:1,\n#EXT-X-BYTERANGE:80@64\nfile.mp4\n" +
		"#EXT-X-MAP:URI=\"file.mp4\",BYTERANGE=\"64@0\"\n" +
		"#EXTINF:1,\n#EXT-X-BYTERANGE:80\nfile.mp4\n#EXT-X-ENDLIST\n"
	got, err := parseHLSTS([]byte(text), base)
	if err != nil || len(got) != 3 {
		t.Fatalf("segments=%v err=%v", got, err)
	}
	if !got[0].init || !got[0].mp4 || got[0].start != 0 || got[0].length != 64 || got[2].start != 144 || got[2].init || !got[2].mp4 {
		t.Fatalf("incorrect initialization or range: %+v", got)
	}
	for _, invalid := range []string{
		"#EXT-X-MAP:URI=\"init.mp4\"\n",
		"#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"64\"\n#EXTINF:1,\na.m4s\n",
		"#EXT-X-MAP:URI=\"file.mp4\",BYTERANGE=\"64@0\"\n#EXTINF:1,\n#EXT-X-BYTERANGE:80\nfile.mp4\n",
		"#EXT-X-MAP:URI=\"a.mp4\"\n#EXTINF:1,\na.m4s\n#EXT-X-MAP:URI=\"b.mp4\"\n#EXTINF:1,\nb.m4s\n",
		"#EXTINF:1,\na.ts\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\na.m4s\n",
		"#EXT-X-MAP:URI=\"file.mp4\"\n#EXTINF:1,\n#EXT-X-BYTERANGE:80@64\nfile.mp4\n",
		"#EXT-X-MAP:URI=\"file:///init.mp4\"\n#EXTINF:1,\na.m4s\n",
	} {
		if _, err := parseHLSTS([]byte("#EXTM3U\n"+invalid+"#EXT-X-ENDLIST\n"), base); err == nil {
			t.Fatalf("unsafe map accepted: %s", invalid)
		}
	}
}

func TestHLSMP4RejectsUnsafeContainers(t *testing.T) {
	init, fragment := hlsMP4TestInit(), hlsMP4TestFragment(1)
	for _, scenario := range []string{"valid", "html", "truncated", "absolute", "implicit-single-track", "track", "offset", "time", "zero-size", "external", "encrypted"} {
		t.Run(scenario, func(t *testing.T) {
			header, media := bytes.Clone(init), bytes.Clone(fragment)
			switch scenario {
			case "html":
				media = []byte("<html>expired session</html>")
			case "truncated":
				media = media[:len(media)-1]
			case "absolute":
				binary.BigEndian.PutUint32(media[bytes.Index(media, []byte("tfhd"))+4:], 1)
			case "implicit-single-track":
				binary.BigEndian.PutUint32(media[bytes.Index(media, []byte("tfhd"))+4:], 0)
			case "track":
				binary.BigEndian.PutUint32(media[bytes.Index(media, []byte("tfhd"))+8:], 2)
			case "offset":
				binary.BigEndian.PutUint32(media[bytes.Index(media, []byte("trun"))+12:], 8)
			case "time":
				copy(media[bytes.Index(media, []byte("tfdt")):], "free")
			case "zero-size":
				binary.BigEndian.PutUint32(media[bytes.Index(media, []byte("mdat"))-4:], 0)
			case "external":
				binary.BigEndian.PutUint32(header[bytes.Index(header, []byte("url "))+4:], 0)
			case "encrypted":
				copy(header[bytes.Index(header, []byte("raw ")):], "encv")
			}
			all := append(header, media...)
			state := &hlsMP4State{}
			err := state.accept(bytes.NewReader(all), 0, int64(len(header)), true)
			if err == nil {
				err = state.accept(bytes.NewReader(all), int64(len(header)), int64(len(all)), false)
			}
			wantValid := scenario == "valid" || scenario == "implicit-single-track"
			if (err == nil) != wantValid {
				t.Fatalf("valid=%v err=%v", wantValid, err)
			}
		})
	}
}

func TestHLSMP4DownloadsWholeAndRangedSegments(t *testing.T) {
	init, first, second := hlsMP4TestInit(), hlsMP4TestFragment(1), hlsMP4TestFragment(2)
	all := bytes.Join([][]byte{init, first, second}, nil)
	for _, ranged := range []bool{false, true} {
		t.Run(fmt.Sprint(ranged), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"stable"`)
				switch r.URL.Path {
				case "/list.m3u8":
					if ranged {
						fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MAP:URI=\"file.mp4\",BYTERANGE=\"%d@0\"\n#EXTINF:1,\n#EXT-X-BYTERANGE:%d@%d\nfile.mp4\n#EXTINF:1,\n#EXT-X-BYTERANGE:%d\nfile.mp4\n#EXT-X-ENDLIST\n", len(init), len(first), len(init), len(second))
					} else {
						fmt.Fprint(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\na.m4s\n#EXTINF:1,\nb.m4s\n#EXT-X-ENDLIST\n")
					}
				case "/file.mp4":
					http.ServeContent(w, r, "file.mp4", time.Time{}, bytes.NewReader(all))
				case "/init.mp4":
					_, _ = w.Write(init)
				case "/a.m4s":
					_, _ = w.Write(first)
				case "/b.m4s":
					_, _ = w.Write(second)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			m := NewManager(t.TempDir(), 1, 1)
			id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
			s := waitFor(t, m, id, Done)
			got, err := os.ReadFile(s.FilePath)
			if err != nil || !bytes.Equal(got, all) || filepath.Ext(s.FilePath) != ".mp4" {
				t.Fatalf("incorrect MP4 output: path=%q err=%v", s.FilePath, err)
			}
		})
	}
}

func TestHLSMP4RestoresNonPacketAlignedPrefix(t *testing.T) {
	init, first, second := hlsMP4TestInit(), hlsMP4TestFragment(1), hlsMP4TestFragment(2)
	var reused, probes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		var body []byte
		switch r.URL.Path {
		case "/list.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\na.m4s\n#EXTINF:1,\nb.m4s\n#EXT-X-ENDLIST\n")
			return
		case "/init.mp4":
			body = init
		case "/a.m4s":
			body = first
		case "/b.m4s":
			body = second
		default:
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/b.m4s" {
			if r.Header.Get("Range") != "" {
				probes.Add(1)
			} else {
				reused.Add(1)
			}
		}
		http.ServeContent(w, r, "segment.mp4", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	dir, store := t.TempDir(), t.TempDir()
	m := NewManager(dir, 1, 1)
	if err := m.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "video.mp4")
	prefix := bytes.Join([][]byte{init, first}, nil)
	if len(prefix)%188 == 0 {
		t.Fatal("fixture must not be TS aligned")
	}
	if err := os.WriteFile(path+".part", append(bytes.Clone(prefix), []byte("unfinished fragment")...), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(srv.URL + "/list.m3u8")
	segments, err := parseHLSTS([]byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\na.m4s\n#EXTINF:1,\nb.m4s\n#EXT-X-ENDLIST\n"), base)
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{id: newID(), url: base.String(), filePath: path, mediaMode: "hls-ts", directory: dir, conns: 1, created: time.Now(), cancel: make(chan struct{}), hlsFormat: "fmp4", hlsPlaylist: hlsPlaylistFingerprint(segments)}
	task.state.Store(Canceled)
	var end int64
	for i, data := range [][]byte{init, first} {
		end += int64(len(data))
		hash := sha256.Sum256(data)
		resource, _ := url.Parse(segments[i].url)
		task.hlsCompleted = append(task.hlsCompleted, hlsCheckpoint{End: end, SHA256: hex.EncodeToString(hash[:]), Resource: resourceFingerprint(resource), ResourceTotal: int64(len(data)), Validator: `"stable"`})
	}
	m.tasks = append(m.tasks, task)
	if err := m.saveTaskLocked(task); err != nil {
		t.Fatal(err)
	}
	restored := NewManager(dir, 1, 1)
	if err := restored.EnablePersistence(store); err != nil {
		t.Fatal(err)
	}
	if !restored.Resume(task.id) {
		t.Fatal("MP4 task did not resume")
	}
	s := waitFor(t, restored, task.id, Done)
	got, err := os.ReadFile(s.FilePath)
	if err != nil || !bytes.Equal(got, bytes.Join([][]byte{init, first, second}, nil)) || reused.Load() != 0 || probes.Load() != 2 {
		t.Fatalf("incorrect MP4 resume: redownloads=%d probes=%d err=%v", reused.Load(), probes.Load(), err)
	}
}

func TestHLSMP4DoesNotPublishInvalidMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\na.m4s\n#EXT-X-ENDLIST\n")
		case "/init.mp4":
			_, _ = w.Write(hlsMP4TestInit())
		default:
			fmt.Fprint(w, "<html>login required</html>")
		}
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), 1, 1)
	id := m.Add(Request{URL: srv.URL + "/list.m3u8", MediaMode: "hls-ts"})
	s := waitFor(t, m, id, ErrorS)
	if !strings.Contains(s.Err, "MP4") {
		t.Fatalf("unexpected error: %s", s.Err)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatalf("invalid media was published: %v", err)
	}
}
