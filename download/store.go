package download

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"openDL/i18n"
)

// Each task has its own journal. Credentials are deliberately not written;
// a browser-authenticated download may need a fresh link/session after restart.
type storedTask struct {
	Version             int                    `json:"version"`
	ID                  string                 `json:"id"`
	Request             Request                `json:"request"`
	State               State                  `json:"state"`
	Held                bool                   `json:"held,omitempty"`
	FilePath            string                 `json:"file_path,omitempty"`
	Created             time.Time              `json:"created"`
	Finished            time.Time              `json:"finished,omitempty"`
	Elapsed             time.Duration          `json:"elapsed,omitempty"`
	BrowserOrigin       bool                   `json:"browser_origin,omitempty"`
	NightSince          time.Time              `json:"night_since,omitempty"`
	Total               int64                  `json:"total"`
	Validator           string                 `json:"validator,omitempty"`
	ResourceFingerprint string                 `json:"resource_fingerprint,omitempty"`
	Chunks              []storedChunk          `json:"chunks,omitempty"`
	HLSPlaylist         string                 `json:"hls_playlist,omitempty"`
	HLSFormat           string                 `json:"hls_format,omitempty"`
	HLSCompleted        []hlsCheckpoint        `json:"hls_completed,omitempty"`
	HLSSplice           *dashSpliceState       `json:"hls_splice,omitempty"`
	DASHVideo           dashTrackCheckpoint    `json:"dash_video,omitempty"`
	DASHAudio           dashTrackCheckpoint    `json:"dash_audio,omitempty"`
	DASHPeriods         []dashPeriodCheckpoint `json:"dash_periods,omitempty"`
	Error               string                 `json:"error,omitempty"`
	CredentialsOmitted  bool                   `json:"credentials_omitted,omitempty"`
	VerifiedSHA256      string                 `json:"verified_sha256,omitempty"`
	SignatureVerified   bool                   `json:"signature_verified,omitempty"`
	RemoteValidator     string                 `json:"remote_validator,omitempty"`
	RemoteResource      string                 `json:"remote_resource,omitempty"`
	SyncOriginalSHA256  string                 `json:"sync_original_sha256,omitempty"`
	FailedSHA256        string                 `json:"failed_sha256,omitempty"`
	PublishSHA256       string                 `json:"publish_sha256,omitempty"`
	PublishSize         int64                  `json:"publish_size,omitempty"`
	ClientRequestID     string                 `json:"client_request_id,omitempty"`
	SubmissionSHA256    string                 `json:"submission_sha256,omitempty"`
}

type storedChunk struct {
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	SHA256 string `json:"sha256"`
}

// EnablePersistence loads a private journal before any tasks are added. Active
// downloads are restored paused so the user decides when network work resumes.
func (m *Manager) EnablePersistence(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tasks) != 0 || m.storeDir != "" {
		return fmt.Errorf(i18n.T("下载记录必须在添加任务之前初始化"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	m.storeDir = dir
	m.quota.attach(filepath.Join(dir, "quota.json"), m.notify)
	var problems []string
	var recovery []*Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if len(id) != 16 {
			continue
		}
		if _, err := hex.DecodeString(id); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			problems = append(problems, entry.Name())
			continue
		}
		var rec storedTask
		if json.Unmarshal(data, &rec) != nil || rec.Version != 1 || rec.ID != id {
			problems = append(problems, entry.Name())
			continue
		}
		u, err := url.Parse(rec.Request.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !filepath.IsAbs(rec.Request.Directory) {
			problems = append(problems, entry.Name())
			continue
		}
		if rec.FilePath != "" && (!filepath.IsAbs(rec.FilePath) || filepath.Clean(filepath.Dir(rec.FilePath)) != filepath.Clean(rec.Request.Directory)) {
			problems = append(problems, entry.Name())
			continue
		}
		conns := rec.Request.Connections
		if _, err := NormalizeSyncMinutes(rec.Request.SyncMinutes); err != nil {
			rec.Request.SyncMinutes = 0
		}
		if (conns < 1 && conns != -1) || conns > 128 {
			conns = m.conns
		}
		t := &Task{id: id, url: rec.Request.URL, pageURL: rec.Request.PageURL, title: rec.Request.Title, nameHint: rec.Request.Name, keepExtension: rec.Request.KeepExtension, mediaMode: rec.Request.MediaMode, mediaQuality: rec.Request.MediaQuality, audioLanguage: rec.Request.AudioLanguage, syncMinutes: rec.Request.SyncMinutes, queue: rec.Request.Queue,
			headers: cloneHeaders(rec.Request.Headers), conns: conns, directory: rec.Request.Directory, openFolder: rec.Request.OpenFolder, expectedSHA256: rec.Request.SHA256, verifiedSHA256: rec.VerifiedSHA256, failedSHA256: rec.FailedSHA256, requireSignature: rec.Request.RequireSignature, signature: rec.Request.Signature, signatureKey: rec.Request.SignatureKey,
			filePath: rec.FilePath, created: rec.Created, finished: rec.Finished, cancel: make(chan struct{}), resumeTotal: rec.Total, resumeValidator: rec.Validator, resumeResource: rec.ResourceFingerprint,
			publishSHA256: rec.PublishSHA256, publishSize: rec.PublishSize}
		t.signatureVerified.Store(rec.SignatureVerified && rec.State == Done)
		t.browserOrigin = rec.BrowserOrigin
		t.elapsed.Store(int64(rec.Elapsed))
		if (t.mediaMode == "dash-fmp4" || t.mediaMode == "hls-ts") && rec.State != Done {
			t.dashVideo = restoreDASHCheckpoint(rec.DASHVideo)
			t.dashAudio = restoreDASHCheckpoint(rec.DASHAudio)
			for i, period := range rec.DASHPeriods {
				if i >= len(t.dashPeriods) {
					break
				}
				t.dashPeriods[i] = dashPeriodCheckpoint{Video: restoreDASHCheckpoint(period.Video), Audio: restoreDASHCheckpoint(period.Audio)}
			}
		}
		version, resource := rec.RemoteValidator, rec.RemoteResource
		if version == "" {
			version, resource = rec.Validator, rec.ResourceFingerprint
		}
		if len(version) <= 8192 && !strings.ContainsAny(version, "\r\n") && validFingerprint(resource) {
			header := http.Header{}
			if strings.HasPrefix(version, "\"") || strings.HasPrefix(version, "W/") {
				header.Set("ETag", version)
			} else {
				header.Set("Last-Modified", version)
			}
			if syncValidator(header) == version {
				t.remoteValidator, t.remoteResource = version, resource
			}
		}
		if validFingerprint(rec.SyncOriginalSHA256) && rec.State != Done {
			t.syncOriginalSHA256 = rec.SyncOriginalSHA256
		}
		if rec.ClientRequestID != "" && ValidClientRequestID(rec.ClientRequestID) && validFingerprint(rec.SubmissionSHA256) {
			t.clientRequestID, t.submissionSHA256 = rec.ClientRequestID, rec.SubmissionSHA256
		}
		if t.created.IsZero() {
			t.created = time.Now()
		}
		// Older completed streams could record an unknown total even though
		// the publication journal retained the verified file's actual size.
		if rec.State == Done && rec.Total < 0 && rec.PublishSize >= 0 {
			if digest, err := NormalizeSHA256(rec.PublishSHA256); err == nil && digest != "" {
				rec.Total = rec.PublishSize
			}
		}
		t.total.Store(rec.Total)
		state := rec.State
		if state != Done && state != ErrorS {
			state = Canceled
		}
		if rec.Held && rec.State == Queued {
			state = Queued
			t.held.Store(true)
		}
		if rec.Request.Queue == "night" && state != Done {
			t.nightSince = rec.NightSince
			if t.nightSince.IsZero() {
				// Legacy records lack a queue-entry time; avoid retroactive starts.
				t.nightSince = time.Now()
			}
			t.night.Store(true)
			t.held.Store(true)
			if state != ErrorS {
				state = Queued
			}
		}
		if rec.CredentialsOmitted {
			t.needsAuth.Store(true)
			if state != Done {
				state = ErrorS
				rec.Error = i18n.T("恢复任务需要重新获取登录信息或下载链接")
			}
		}
		t.state.Store(state)
		if state == Done {
			// Completed tasks loaded from a previous session do not reopen folders.
			t.folderOpened.Store(true)
		}
		t.err = rec.Error
		if state != Done && t.filePath != "" {
			if _, err := os.Stat(extendedPath(t.filePath + ".part")); os.IsNotExist(err) {
				t.state.Store(ErrorS)
				t.err = i18n.T("临时文件不存在，请检查已下载文件或重新下载")
			}
		}
		var done int64
		sort.Slice(rec.Chunks, func(i, j int) bool { return rec.Chunks[i].Start < rec.Chunks[j].Start })
		var next int64
		for _, c := range rec.Chunks {
			if c.Start < next || c.End < c.Start || c.End >= rec.Total || len(c.SHA256) != 64 {
				continue
			}
			if _, err := hex.DecodeString(c.SHA256); err != nil {
				continue
			}
			t.completed = append(t.completed, chunkSpan{start: c.Start, end: c.End, digest: c.SHA256})
			done += c.End - c.Start + 1
			next = c.End + 1
		}
		if state == Done {
			done = max(int64(0), rec.Total)
		} else if t.mediaMode == "hls-ts" || t.mediaMode == "dash-fmp4" {
			if validFingerprint(rec.HLSPlaylist) && (rec.HLSFormat == "" || rec.HLSFormat == "fmp4") {
				t.hlsPlaylist = rec.HLSPlaylist
				t.hlsFormat = rec.HLSFormat
				var end int64
				for i, chunk := range rec.HLSCompleted {
					if i >= maxHLSSegments || chunk.End < end || (chunk.End == end && (!chunk.SkippedInit || i == 0)) || (chunk.SkippedInit && (chunk.End != end || rec.HLSFormat != "fmp4")) || (rec.HLSFormat != "fmp4" && chunk.End%188 != 0) || !validFingerprint(chunk.SHA256) || !validFingerprint(chunk.Resource) || chunk.ResourceTotal < 0 {
						break
					}
					t.hlsCompleted = append(t.hlsCompleted, sanitizeHLSCheckpoint(chunk))
					end = chunk.End
				}
				t.hlsSplice = restoreSpliceState(rec.HLSSplice)
				done = end
			}
		}
		t.done.Store(done)
		t.resetSegments(t.completed)
		if state != Done && publicationCandidate(t) {
			t.state.Store(ErrorS)
			t.err = i18n.T("正在检查已发布的文件")
			t.verifying.Store(true)
			recovery = append(recovery, t)
		}
		m.tasks = append(m.tasks, t)
	}
	for _, t := range recovery {
		go m.recoverPublished(t)
	}
	if len(problems) > 0 {
		return fmt.Errorf(i18n.T("部分下载记录无法读取：%s"), strings.Join(problems, ", "))
	}
	return nil
}

func safeStoredHeaders(headers map[string]string) (map[string]string, bool) {
	clean := make(map[string]string, len(headers))
	omitted := false
	for k, v := range headers {
		switch strings.ToLower(k) {
		case "accept", "accept-language", "user-agent", "referer", "origin":
			clean[k] = v
		default:
			omitted = true
		}
	}
	return clean, omitted
}

// saveTaskLocked requires checkpointMu; the snapshot is written and synced to
// a temporary file before it replaces the journal.
func (m *Manager) saveTaskLocked(t *Task) error {
	if m.storeDir == "" || t.removed.Load() {
		return nil
	}
	s := t.snapshotBase()
	t.metaMu.Lock()
	headers, omitted := safeStoredHeaders(t.headers)
	storedURL, pageURL, expected, verified, failed, publishHash, publishSize := t.url, t.pageURL, t.expectedSHA256, t.verifiedSHA256, t.failedSHA256, t.publishSHA256, t.publishSize
	signatureText, signatureKey := t.signature, t.signatureKey
	t.metaMu.Unlock()
	if parsed, err := url.Parse(storedURL); err == nil && parsed.User != nil {
		parsed.User = nil
		storedURL = parsed.String()
		omitted = true
	}
	omitted = omitted || t.needsAuth.Load()
	queue := ""
	if s.Night {
		queue = "night"
	}
	rec := storedTask{Version: 1, ID: t.id, Request: Request{URL: storedURL, PageURL: pageURL, Title: t.title, Name: t.nameHint, KeepExtension: t.keepExtension, MediaMode: t.mediaMode, MediaQuality: t.mediaQuality, AudioLanguage: t.audioLanguage, Headers: headers, Connections: t.conns, Directory: t.directory, OpenFolder: t.openFolder, Queue: queue, SHA256: expected, RequireSignature: s.SignatureRequired, Signature: signatureText, SignatureKey: signatureKey},
		State: s.State, Held: s.Held, FilePath: s.FilePath, Created: s.Created, Finished: s.Finished, NightSince: s.NightSince, Total: t.resumeTotal, Validator: t.resumeValidator, ResourceFingerprint: t.resumeResource, Error: s.Err, CredentialsOmitted: omitted, VerifiedSHA256: verified, FailedSHA256: failed, PublishSHA256: publishHash, PublishSize: publishSize}
	rec.ClientRequestID, rec.SubmissionSHA256 = t.clientRequestID, t.submissionSHA256
	rec.Elapsed = s.Elapsed
	rec.BrowserOrigin = s.BrowserOrigin
	rec.SignatureVerified = s.SignatureVerified
	rec.Request.SyncMinutes = s.SyncMinutes
	rec.RemoteValidator, rec.RemoteResource = t.remoteValidator, t.remoteResource
	t.metaMu.Lock()
	rec.SyncOriginalSHA256 = t.syncOriginalSHA256
	t.metaMu.Unlock()
	if s.State == Done {
		rec.Total = s.Total
	}
	for _, c := range t.completed {
		if c.digest != "" {
			rec.Chunks = append(rec.Chunks, storedChunk{Start: c.start, End: c.end, SHA256: c.digest})
		}
	}
	if t.mediaMode == "hls-ts" || t.mediaMode == "dash-fmp4" {
		rec.HLSPlaylist = t.hlsPlaylist
		rec.HLSFormat = t.hlsFormat
		for _, chunk := range t.hlsCompleted {
			rec.HLSCompleted = append(rec.HLSCompleted, sanitizeHLSCheckpoint(chunk))
		}
		rec.HLSSplice = t.hlsSplice
	}
	if t.mediaMode == "dash-fmp4" || t.mediaMode == "hls-ts" {
		rec.DASHVideo = cloneDASHCheckpoint(t.dashVideo)
		rec.DASHAudio = cloneDASHCheckpoint(t.dashAudio)
		for _, period := range t.dashPeriods {
			rec.DASHPeriods = append(rec.DASHPeriods, dashPeriodCheckpoint{Video: cloneDASHCheckpoint(period.Video), Audio: cloneDASHCheckpoint(period.Audio)})
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.storeDir, ".task-*")
	if err != nil {
		return err
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
		return err
	}
	return os.Rename(name, filepath.Join(m.storeDir, t.id+".json"))
}

func (m *Manager) saveTask(t *Task) error {
	t.checkpointMu.Lock()
	defer t.checkpointMu.Unlock()
	return m.saveTaskLocked(t)
}

func (m *Manager) saveTaskLog(t *Task) {
	if err := m.saveTask(t); err != nil {
		log.Printf(i18n.T("保存下载记录 %s: %v"), t.id, err)
	}
}

func (m *Manager) removeTaskJournal(t *Task) {
	if m.storeDir == "" {
		return
	}
	t.checkpointMu.Lock()
	defer t.checkpointMu.Unlock()
	if err := os.Remove(filepath.Join(m.storeDir, t.id+".json")); err != nil && !os.IsNotExist(err) {
		log.Printf(i18n.T("删除下载记录 %s: %v"), t.id, err)
	}
}

func chunkDigest(f *os.File, c chunkSpan) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(f, c.start, c.end-c.start+1))
	if err != nil {
		return "", err
	}
	if n != c.end-c.start+1 {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
