// Package api is the loopback control channel between the native messaging
// host (and any local tool) and the desktop download manager. It binds only
// 127.0.0.1 and requires a per-install token written to the user's config
// directory, which the host reads.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"openDL/i18n"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"openDL/download"
	"openDL/pathrec"

	"golang.org/x/net/http/httpguts"
)

// DefaultPort is the fixed loopback port the native host connects to.
const DefaultPort = 37913

// Server serves the download manager over loopback HTTP.
type Server struct {
	Mgr       *download.Manager
	Token     string
	Prompt    func(download.Request) bool
	OpenTask  func(string, bool) error
	promptMu  sync.Mutex
	promptIDs map[string][32]byte
	ln        net.Listener
	theme     atomic.Pointer[Theme]
}

// Theme contains only public presentation tokens for the browser extension.
type Theme struct {
	Light  ThemeColors `json:"light"`
	Dark   ThemeColors `json:"dark"`
	Active string      `json:"active,omitempty"`
}

type ThemeColors struct {
	Primary   string `json:"primary"`
	OnPrimary string `json:"onPrimary"`
	Selection string `json:"selection"`
	Focus     string `json:"focus"`
}

// SetTheme publishes an immutable snapshot to HTTP handlers.
func (s *Server) SetTheme(theme Theme) { s.theme.Store(&theme) }

// NewToken creates the per-install token and stores it where the native
// host reads it.
func NewToken() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "opendl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	f, err := os.CreateTemp(dir, ".api-token-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write([]byte(token)); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(name, filepath.Join(dir, "api-token")); err != nil {
		return "", err
	}
	return token, nil
}

// TokenPath is where the token file lives.
func TokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "api-token"), nil
}

// Start binds 127.0.0.1:DefaultPort and serves in the background.
func (s *Server) Start() error {
	if s.Token == "" {
		return errors.New(i18n.T("本地接口缺少访问令牌"))
	}
	if s.Mgr == nil {
		return errors.New(i18n.T("下载引擎未就绪"))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(DefaultPort))
	if err != nil {
		return err
	}
	s.ln = ln
	go func() { _ = http.Serve(ln, s) }()
	return nil
}

// StartWithNewToken reserves the port before replacing the token file. A
// second instance therefore cannot invalidate a running instance's token.
func (s *Server) StartWithNewToken() error {
	if s.Mgr == nil {
		return errors.New(i18n.T("下载引擎未就绪"))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(DefaultPort))
	if err != nil {
		return err
	}
	token, err := NewToken()
	if err != nil {
		_ = ln.Close()
		return err
	}
	s.Token, s.ln = token, ln
	go func() { _ = http.Serve(ln, s) }()
	return nil
}

// Close stops the listener.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-OpenDL-Token")), []byte(s.Token)) != 1 {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/ping":
		writeJSON(w, map[string]any{"ok": true, "theme": s.theme.Load(), "capabilities": []string{"scheduled-queue", "idempotent-submission", "media-hls", "media-dash", "media-quality", "media-audio-language", "finish-live", "task-control", "download-prompt", "browser-tasks", "browser-tasks-v2"}})
	case r.Method == http.MethodPost && (r.URL.Path == "/api/download" || r.URL.Path == "/api/prompt"):
		var req download.Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, i18n.T("下载请求格式无效"), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, i18n.T("下载请求格式无效"), http.StatusBadRequest)
			return
		}
		req.URL = strings.TrimSpace(req.URL)
		u, err := url.Parse(req.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ftp" && u.Scheme != "ftps") {
			http.Error(w, i18n.T("请输入有效的 HTTP、HTTPS、FTP 或 FTPS 下载地址"), http.StatusBadRequest)
			return
		}
		for name, value := range req.Headers {
			if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
				http.Error(w, fmt.Sprintf(i18n.T("请求头格式无效：%s"), name), http.StatusBadRequest)
				return
			}
		}
		req.SHA256, err = download.NormalizeSHA256(req.SHA256)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.SyncMinutes != 0 {
			http.Error(w, i18n.T("周期同步功能已移除"), http.StatusBadRequest)
			return
		}
		if req.MediaMode != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			http.Error(w, i18n.T("不支持的媒体下载模式"), http.StatusBadRequest)
			return
		}
		req.MediaQuality, err = download.NormalizeMediaQuality(req.MediaQuality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.MediaQuality != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			http.Error(w, i18n.T("画质选择仅对媒体下载有效"), http.StatusBadRequest)
			return
		}
		req.AudioLanguage, err = download.NormalizeAudioLanguage(req.AudioLanguage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.AudioLanguage != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			http.Error(w, i18n.T("音轨语言仅对媒体下载有效"), http.StatusBadRequest)
			return
		}
		if !download.ValidQueueID(req.Queue) {
			http.Error(w, i18n.T("不支持的下载队列"), http.StatusBadRequest)
			return
		}
		if !download.ValidClientRequestID(req.ClientRequestID) {
			http.Error(w, i18n.T("下载请求编号格式无效"), http.StatusBadRequest)
			return
		}
		// The prompt endpoint is the browser integration's entry point. Older
		// native hosts do not send the source header, so retain their origin
		// when the user confirms the download in the desktop dialog.
		req.BrowserOrigin = r.URL.Path == "/api/prompt" || r.Header.Get("X-OpenDL-Source") == "browser"
		if r.URL.Path == "/api/prompt" {
			if s.Prompt == nil {
				http.Error(w, "prompt unavailable", http.StatusServiceUnavailable)
				return
			}
			if req.ClientRequestID == "" {
				var id [16]byte
				if _, err := rand.Read(id[:]); err != nil {
					http.Error(w, "request unavailable", http.StatusServiceUnavailable)
					return
				}
				req.ClientRequestID = hex.EncodeToString(id[:])
			}
			encoded, _ := json.Marshal(req)
			digest := sha256.Sum256(encoded)
			s.promptMu.Lock()
			if s.promptIDs == nil {
				s.promptIDs = make(map[string][32]byte)
			}
			previous, exists := s.promptIDs[req.ClientRequestID]
			if exists && previous != digest {
				s.promptMu.Unlock()
				http.Error(w, "request conflict", http.StatusConflict)
				return
			}
			if !exists {
				if len(s.promptIDs) >= 4096 || !s.Prompt(req) {
					s.promptMu.Unlock()
					http.Error(w, "prompt queue unavailable", http.StatusTooManyRequests)
					return
				}
				s.promptIDs[req.ClientRequestID] = digest
			}
			s.promptMu.Unlock()
			writeJSON(w, map[string]any{"id": req.ClientRequestID})
			return
		}
		id, err := s.Mgr.AddRecorded(req)
		if err != nil {
			if errors.Is(err, download.ErrUpdateInProgress) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "update_in_progress", "accepted": false})
				return
			}
			if errors.Is(err, download.ErrRequestIDConflict) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "journal_write_failed", "accepted": false})
			return
		}
		writeJSON(w, map[string]any{"id": id})
	case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
		writeJSON(w, s.Mgr.Snapshots())
	case r.Method == http.MethodGet && (r.URL.Path == "/api/browser-tasks" || r.URL.Path == "/api/browser-tasks-v2"):
		type task struct {
			ID, Name           string
			State              download.State
			Total, Done, Speed int64
			Held               bool
		}
		tasks := make([]task, 0)
		for _, snap := range s.Mgr.Snapshots() {
			if snap.BrowserOrigin {
				name := snap.Name
				if name == "" {
					if candidate := pathrec.FromURLPath(snap.URL); candidate != "" {
						name = pathrec.SanitizeFilename(candidate)
					}
				}
				tasks = append(tasks, task{snap.ID, name, snap.State, snap.Total, snap.Done, snap.Speed, snap.Held})
			}
		}
		if r.URL.Path == "/api/browser-tasks" {
			if len(tasks) > 200 {
				tasks = tasks[len(tasks)-200:]
			}
			writeJSON(w, tasks)
			return
		}
		before := r.URL.Query().Get("before")
		end := len(tasks)
		if before != "" {
			end = -1
			for i := range tasks {
				if tasks[i].ID == before {
					end = i
					break
				}
			}
			if end < 0 {
				http.Error(w, "task cursor expired", http.StatusConflict)
				return
			}
		}
		start := max(0, end-100)
		next := ""
		if start > 0 {
			next = tasks[start].ID
		}
		writeJSON(w, map[string]any{"tasks": tasks[start:end], "next": next})
	case r.Method == http.MethodPost && r.URL.Path == "/api/browser-task-open":
		var body struct {
			ID     string `json:"id"`
			Folder bool   `json:"folder"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.ID == "" || s.OpenTask == nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		allowed := false
		for _, snap := range s.Mgr.Snapshots() {
			if snap.ID == body.ID && snap.BrowserOrigin && snap.State == download.Done && snap.FilePath != "" {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "task unavailable", http.StatusNotFound)
			return
		}
		if err := s.OpenTask(body.ID, body.Folder); err != nil {
			http.Error(w, "cannot open file", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/cancel":
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Mgr.Cancel(body.ID)
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/resume":
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": s.Mgr.Resume(body.ID)})
	case r.Method == http.MethodPost && r.URL.Path == "/api/finish-live":
		var body struct {
			ID string `json:"id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
			http.Error(w, i18n.T("任务编号无效"), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, i18n.T("任务编号无效"), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": s.Mgr.FinishLive(strings.TrimSpace(body.ID))})
	case r.Method == http.MethodPost && r.URL.Path == "/api/pause":
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Mgr.Cancel(body.ID)
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/start-all":
		writeJSON(w, map[string]any{"count": s.Mgr.ResumeAll()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/pause-all":
		writeJSON(w, map[string]any{"count": s.Mgr.PauseAll()})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

var errClosed = errors.New("closed")
