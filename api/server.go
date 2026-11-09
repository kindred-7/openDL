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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openDL/download"
	"openDL/pathrec"

	"golang.org/x/net/http/httpguts"
)

// DefaultPort is the fixed loopback port the native host connects to.
const DefaultPort = 37913

// promptIDCapacity caps the prompt dedupe table. Reaching the cap evicts the
// oldest entries instead of refusing new prompts.
const promptIDCapacity = 4096

// Server serves the download manager over loopback HTTP.
type Server struct {
	Mgr         *download.Manager
	Token       string
	Prompt      func(download.Request) bool
	OpenTask    func(string, bool) error
	InstallExit func(string) bool
	Replay      LinkReplay
	promptMu    sync.Mutex
	promptIDs   map[string][32]byte
	promptOrder []string // insertion order of promptIDs, drained oldest-first
	ln          net.Listener
	theme       atomic.Pointer[Theme]
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
	Brand     string `json:"brand,omitempty"`
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
	// The token file is now JSON {port, token} so readers learn the bound
	// port; a legacy plain-text file keeps its old meaning in ReadEndpoint.
	data, err := json.Marshal(struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}{DefaultPort, token})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".api-token-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
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

// Endpoint is the loopback port and per-install token recorded in the token
// file, so local tools connect to the port the desktop app actually bound.
type Endpoint struct {
	Port  int
	Token string
}

// ReadEndpoint loads the token file written by NewToken. A legacy plain-text
// token file keeps its meaning: the token alone on DefaultPort.
func ReadEndpoint() (Endpoint, error) {
	path, err := TokenPath()
	if err != nil {
		return Endpoint{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Endpoint{}, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return Endpoint{}, errors.New(i18n.T("本地接口令牌为空"))
	}
	endpoint := Endpoint{Port: DefaultPort, Token: text}
	if strings.HasPrefix(text, "{") {
		var file struct {
			Port  *int   `json:"port"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(text), &file); err != nil {
			return Endpoint{}, err
		}
		if file.Token == "" {
			return Endpoint{}, errors.New(i18n.T("本地接口令牌为空"))
		}
		endpoint.Token = file.Token
		if file.Port != nil {
			if *file.Port < 1 || *file.Port > 65535 {
				return Endpoint{}, errors.New(i18n.T("本地接口端口无效"))
			}
			endpoint.Port = *file.Port
		}
	}
	return endpoint, nil
}

// BaseURL returns the loopback base URL for a port.
func BaseURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// Start binds 127.0.0.1:DefaultPort and serves in the background.
func (s *Server) Start() error {
	if s.Token == "" {
		return errors.New(i18n.T("本地接口缺少访问令牌"))
	}
	if s.Mgr == nil {
		return errors.New(i18n.T("下载引擎未就绪"))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultPort))
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
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultPort))
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
		writeError(w, http.StatusUnauthorized, "unauthorized", i18n.T("访问令牌无效"))
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/installer-exit":
		var request struct {
			Executable string `json:"executable"`
		}
		if err := readJSON(w, r, &request, 32768); err != nil || s.InstallExit == nil || !s.InstallExit(request.Executable) {
			writeError(w, http.StatusConflict, "installer_exit_unavailable", i18n.T("安装器退出请求无法处理"))
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && r.URL.Path == "/api/ping":
		writeJSON(w, map[string]any{"ok": true, "theme": s.theme.Load(), "capabilities": []string{"scheduled-queue", "idempotent-submission", "media-hls", "media-dash", "media-quality", "media-audio-language", "finish-live", "task-control", "download-prompt", "browser-tasks", "browser-tasks-v2", "browser-attachment-replay", "file-size-probe", "browser-link-replay"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/browser-jobs":
		// 浏览器主机长轮询“重新请求原链接”任务；桌面端不主动连接浏览器。
		job, ok := s.Replay.Wait(25 * time.Second)
		jobs := make([]ReplayJob, 0, 1)
		if ok {
			jobs = append(jobs, job)
		}
		writeJSON(w, map[string]any{"jobs": jobs})
	case r.Method == http.MethodPost && r.URL.Path == "/api/browser-job-result":
		var result ReplayResult
		if err := readJSON(w, r, &result, 32<<10); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("下载请求格式无效"))
			return
		}
		s.Replay.Complete(result)
		writeJSON(w, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && (r.URL.Path == "/api/download" || r.URL.Path == "/api/prompt" || r.URL.Path == "/api/probe"):
		var req download.Request
		if err := readJSON(w, r, &req, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("下载请求格式无效"))
			return
		}
		if req.ExpectedBytes < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("下载请求格式无效"))
			return
		}
		if r.URL.Path != "/api/prompt" || (req.FileSize != nil && *req.FileSize < 0) {
			req.FileSize = nil
		}
		req.URL = strings.TrimSpace(req.URL)
		u, err := url.Parse(req.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ftp" && u.Scheme != "ftps") {
			writeError(w, http.StatusBadRequest, "invalid_url", i18n.T("请输入有效的 HTTP、HTTPS、FTP 或 FTPS 下载地址"))
			return
		}
		for name, value := range req.Headers {
			if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
				writeError(w, http.StatusBadRequest, "invalid_header", fmt.Sprintf(i18n.T("请求头格式无效：%s"), name))
				return
			}
		}
		req.SHA256, err = download.NormalizeSHA256(req.SHA256)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_sha256", err.Error())
			return
		}
		if req.SyncMinutes != 0 {
			writeError(w, http.StatusBadRequest, "sync_removed", i18n.T("周期同步功能已移除"))
			return
		}
		if req.MediaMode != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			writeError(w, http.StatusBadRequest, "unsupported_media_mode", i18n.T("不支持的媒体下载模式"))
			return
		}
		req.MediaQuality, err = download.NormalizeMediaQuality(req.MediaQuality)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_media_quality", err.Error())
			return
		}
		if req.MediaQuality != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			writeError(w, http.StatusBadRequest, "media_quality_without_media", i18n.T("画质选择仅对媒体下载有效"))
			return
		}
		req.AudioLanguage, err = download.NormalizeAudioLanguage(req.AudioLanguage)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_audio_language", err.Error())
			return
		}
		if req.AudioLanguage != "" && req.MediaMode != "hls-ts" && req.MediaMode != "dash-fmp4" {
			writeError(w, http.StatusBadRequest, "audio_language_without_media", i18n.T("音轨语言仅对媒体下载有效"))
			return
		}
		if !download.ValidQueueID(req.Queue) {
			writeError(w, http.StatusBadRequest, "unsupported_queue", i18n.T("不支持的下载队列"))
			return
		}
		if !download.ValidClientRequestID(req.ClientRequestID) {
			writeError(w, http.StatusBadRequest, "invalid_request_id", i18n.T("下载请求编号格式无效"))
			return
		}
		if r.URL.Path == "/api/probe" {
			writeJSON(w, map[string]any{"fileSize": s.Mgr.ProbeFileSize(r.Context(), req)})
			return
		}
		// The prompt endpoint is the browser integration's entry point. Older
		// native hosts do not send the source header, so retain their origin
		// when the user confirms the download in the desktop dialog.
		req.BrowserOrigin = r.URL.Path == "/api/prompt" || r.Header.Get("X-OpenDL-Source") == "browser"
		if r.URL.Path == "/api/prompt" {
			if s.Prompt == nil {
				writeError(w, http.StatusServiceUnavailable, "prompt_unavailable", i18n.T("桌面确认弹窗未就绪"))
				return
			}
			if req.ClientRequestID == "" {
				var id [16]byte
				if _, err := rand.Read(id[:]); err != nil {
					writeError(w, http.StatusServiceUnavailable, "request_unavailable", i18n.T("无法生成请求编号"))
					return
				}
				req.ClientRequestID = hex.EncodeToString(id[:])
			}
			identity := req
			identity.FileSize = nil // The display hint can become available between retries.
			encoded, _ := json.Marshal(identity)
			digest := sha256.Sum256(encoded)
			s.promptMu.Lock()
			if s.promptIDs == nil {
				s.promptIDs = make(map[string][32]byte)
			}
			previous, exists := s.promptIDs[req.ClientRequestID]
			if exists && previous != digest {
				s.promptMu.Unlock()
				writeError(w, http.StatusConflict, "request_conflict", i18n.T("请求编号已用于其他下载内容"))
				return
			}
			if !exists {
				// A grow-only dedupe table eventually fills the cap with stale
				// ids and leaks memory. At capacity evict the oldest entries
				// instead of refusing the new prompt.
				for len(s.promptIDs) >= promptIDCapacity {
					oldest := s.promptOrder[0]
					s.promptOrder = s.promptOrder[1:]
					delete(s.promptIDs, oldest)
				}
				if !s.Prompt(req) {
					s.promptMu.Unlock()
					writeError(w, http.StatusTooManyRequests, "prompt_queue_unavailable", i18n.T("下载确认队列暂不可用"))
					return
				}
				s.promptIDs[req.ClientRequestID] = digest
				s.promptOrder = append(s.promptOrder, req.ClientRequestID)
			}
			s.promptMu.Unlock()
			writeJSON(w, map[string]any{"id": req.ClientRequestID})
			return
		}
		id, err := s.Mgr.AddRecorded(req)
		if err != nil {
			if errors.Is(err, download.ErrUpdateInProgress) {
				writeError(w, http.StatusServiceUnavailable, "update_in_progress", i18n.T("应用正在准备更新，请稍后再操作"), map[string]any{"accepted": false})
				return
			}
			if errors.Is(err, download.ErrRequestIDConflict) {
				writeError(w, http.StatusConflict, "request_id_conflict", err.Error())
				return
			}
			writeError(w, http.StatusServiceUnavailable, "journal_write_failed", i18n.T("无法保存下载任务，请检查应用日志和存储空间"), map[string]any{"accepted": false})
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
				writeError(w, http.StatusConflict, "task_cursor_expired", i18n.T("任务游标已失效"))
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
		if err := readJSON(w, r, &body, 4096); err != nil || body.ID == "" || s.OpenTask == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("任务请求格式无效"))
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
			writeError(w, http.StatusNotFound, "task_unavailable", i18n.T("任务不可用或未完成"))
			return
		}
		if err := s.OpenTask(body.ID, body.Folder); err != nil {
			writeError(w, http.StatusConflict, "open_failed", i18n.T("无法打开文件"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/cancel":
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(w, r, &body, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("请求格式无效"))
			return
		}
		s.Mgr.Cancel(body.ID)
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/resume":
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(w, r, &body, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("请求格式无效"))
			return
		}
		writeJSON(w, map[string]any{"ok": s.Mgr.Resume(body.ID)})
	case r.Method == http.MethodPost && r.URL.Path == "/api/finish-live":
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(w, r, &body, 4096); err != nil || strings.TrimSpace(body.ID) == "" {
			writeError(w, http.StatusBadRequest, "invalid_task_id", i18n.T("任务编号无效"))
			return
		}
		writeJSON(w, map[string]any{"ok": s.Mgr.FinishLive(strings.TrimSpace(body.ID))})
	case r.Method == http.MethodPost && r.URL.Path == "/api/pause":
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(w, r, &body, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", i18n.T("请求格式无效"))
			return
		}
		s.Mgr.Cancel(body.ID)
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/start-all":
		writeJSON(w, map[string]any{"count": s.Mgr.ResumeAll()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/pause-all":
		writeJSON(w, map[string]any{"count": s.Mgr.PauseAll()})
	default:
		writeError(w, http.StatusNotFound, "not_found", i18n.T("接口不存在"))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON reads a request body under one size limit and uses a second
// Decode to reject trailing content.
func readJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New(i18n.T("请求包含多余的尾随内容"))
	}
	return nil
}

// writeError returns the uniform {code, reason} error body. The code drives
// extension-side localization while the reason is for development diagnosis
// and fallback display; extra fields (such as accepted) keep compatibility.
func writeError(w http.ResponseWriter, status int, code, reason string, extra ...map[string]any) {
	body := map[string]any{"code": code, "reason": reason}
	for _, fields := range extra {
		for key, value := range fields {
			body[key] = value
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
