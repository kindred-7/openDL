package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"openDL/i18n"
)

const maxSyncMinutes = 7 * 24 * 60

func NormalizeSyncMinutes(value int) (int, error) {
	if value != 0 && (value < 5 || value > maxSyncMinutes) {
		return 0, i18n.NewError("同步周期需要在 5 分钟到 7 天之间")
	}
	return value, nil
}

// Conditional probes also support weak ETags, independently of range reuse.
func syncValidator(header http.Header) string {
	if etag := strings.TrimSpace(header.Get("ETag")); etag != "" {
		if strongValidator(http.Header{"Etag": []string{strings.TrimPrefix(etag, "W/")}}) != "" {
			return etag
		}
		// An unusable ETag is not a conditional validator. Last-Modified may
		// still be usable for change detection (unlike If-Range resumption).
	}
	modified := strings.TrimSpace(header.Get("Last-Modified"))
	if _, err := http.ParseTime(modified); err == nil {
		return modified
	}
	return ""
}

func (m *Manager) RunDueSync() {
	now := time.Now()
	type syncProbe struct {
		task     *Task
		revision uint64
	}
	var due []syncProbe
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return
	}
	for _, t := range m.tasks {
		if t.syncMinutes <= 0 || t.syncProbing || t.removed.Load() || t.editing || t.verifying.Load() || t.state.Load().(State) != Done {
			continue
		}
		if t.nextSync.IsZero() {
			t.nextSync = now.Add(time.Duration(t.syncMinutes) * time.Minute)
			continue
		}
		if now.Before(t.nextSync) {
			continue
		}
		t.nextSync = now.Add(time.Duration(t.syncMinutes) * time.Minute)
		select {
		case <-t.cancel:
			t.cancel = make(chan struct{})
			t.cancelOnce = sync.Once{}
		default:
		}
		t.syncProbing = true
		due = append(due, syncProbe{task: t, revision: t.syncRevision})
	}
	m.mu.Unlock()
	for _, probe := range due {
		go func(t *Task, revision uint64) {
			defer func() { m.mu.Lock(); t.syncProbing = false; m.mu.Unlock() }()
			previousError := t.errText()
			err := m.syncTask(t, revision)
			m.mu.Lock()
			t.errMu.Lock()
			current := !t.removed.Load() && t.syncMinutes > 0 && t.syncRevision == revision && t.state.Load().(State) == Done
			changed := false
			if current && err != nil {
				t.err = err.Error()
				changed = true
			} else if current && previousError != "" && t.err == previousError {
				t.err = ""
				changed = true
			}
			t.errMu.Unlock()
			m.mu.Unlock()
			if changed {
				m.notify()
			}
		}(probe.task, probe.revision)
	}
}

func (m *Manager) syncTask(t *Task, revision uint64) error {
	t.metaMu.Lock()
	address := t.url
	t.metaMu.Unlock()
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		return i18n.NewError("周期同步仅支持普通 HTTP 或 HTTPS 文件")
	}
	t.checkpointMu.Lock()
	validator, resource := t.remoteValidator, t.remoteResource
	t.checkpointMu.Unlock()
	if validator == "" {
		return i18n.NewError("服务器未提供可用于同步的资源版本")
	}
	if t.needsAuth.Load() && !m.canSystemAuthenticate(t) {
		return i18n.NewError("同步需要重新获取登录信息")
	}
	req, cancel, err := t.newRequest(address)
	if err != nil {
		return err
	}
	defer cancel()
	ctx, timeout := context.WithTimeout(req.Context(), 30*time.Second)
	defer timeout()
	req = req.WithContext(ctx)
	req.Method = http.MethodHead
	req.Header.Del("Range")
	req.Header.Del("If-Range")
	req.Header.Del("If-Match")
	req.Header.Del("If-Unmodified-Since")
	req.Header.Del("If-None-Match")
	req.Header.Del("If-Modified-Since")
	if strings.HasPrefix(validator, "\"") || strings.HasPrefix(validator, "W/\"") {
		req.Header.Set("If-None-Match", validator)
	} else {
		req.Header.Set("If-Modified-Since", validator)
	}
	resp, err := m.doWithRetry(t, req)
	var status *httpStatusError
	fallback := err != nil && errors.As(err, &status) && (status.code == http.StatusMethodNotAllowed || status.code == http.StatusNotImplemented)
	if err == nil && resp.StatusCode == http.StatusOK && syncValidator(resp.Header) == "" {
		// Some servers return validators on GET but omit them on HEAD.
		resp.Body.Close()
		fallback = true
	}
	if fallback {
		// doWithRetry closes non-success responses and returns the status as
		// an error, so HEAD fallback must inspect that error, not resp.
		req = req.Clone(ctx)
		req.Method = http.MethodGet
		req.Header.Set("Range", "bytes=0-0")
		resp, err = m.doWithRetry(t, req)
	}
	if err != nil {
		return fmt.Errorf(i18n.T("周期同步探测失败：%w"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if resp.Request == nil || resp.Request.URL == nil {
			return i18n.NewError("无法确认同步资源的最终地址")
		}
		// A conditional validator belongs to the previously recorded final
		// resource; an equal validator at a new redirect target proves nothing.
		if resourceFingerprint(resp.Request.URL) != resource {
			return m.beginSync(t, revision)
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf(i18n.T("周期同步探测失败（HTTP %d）"), resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return i18n.NewError("周期同步探测使用了不支持的内容编码")
	}
	if resp.StatusCode == http.StatusPartialContent {
		value := strings.TrimSpace(resp.Header.Get("Content-Range"))
		const prefix = "bytes 0-0/"
		if !strings.HasPrefix(value, prefix) {
			return i18n.NewError("周期同步范围响应无效")
		}
		sizeText := strings.TrimPrefix(value, prefix)
		size, parseErr := strconv.ParseInt(sizeText, 10, 64)
		if parseErr != nil || size < 1 || strconv.FormatInt(size, 10) != sizeText || (resp.ContentLength >= 0 && resp.ContentLength != 1) {
			return i18n.NewError("周期同步范围响应无效")
		}
		body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
		if _, err = m.readExactMediaBody(t, body, 1, i18n.NewError("周期同步范围响应无效")); err != nil {
			return fmt.Errorf(i18n.T("周期同步探测失败：%w"), err)
		}
	} else if fallback {
		if digestBody, ok := resp.Body.(*digestIntegrityBody); ok && digestBody.state.qop == "auth-int" {
			return i18n.NewError("Digest auth-int 同步探测需要可读取的范围响应")
		}
	}
	next := syncValidator(resp.Header)
	if next == "" || resp.Request == nil || resp.Request.URL == nil {
		return i18n.NewError("服务器未提供可用于同步的资源版本")
	}
	if next == validator && resourceFingerprint(resp.Request.URL) == resource {
		return nil
	}
	return m.beginSync(t, revision)
}


// SyncNow 立即探测一次资源变化并返回结果说明；与周期探测同一套语义。
func (m *Manager) SyncNow(id string) (string, error) {
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return "", ErrUpdateInProgress
	}
	var t *Task
	for _, candidate := range m.tasks {
		if candidate.id == id && !candidate.removed.Load() {
			t = candidate
			break
		}
	}
	if t == nil {
		m.mu.Unlock()
		return "", i18n.NewError("任务不存在")
	}
	if t.syncProbing || t.editing || t.verifying.Load() || t.state.Load().(State) != Done {
		m.mu.Unlock()
		return "", i18n.NewError("任务正在更新或校验，请稍后重试")
	}
	if t.syncMinutes <= 0 {
		// 无周期的任务不进入自动替换链路，避免把“未启用”误报成“未变化”。
		m.mu.Unlock()
		return "", i18n.NewError("请先设置同步周期")
	}
	select {
	case <-t.cancel:
		t.cancel = make(chan struct{})
		t.cancelOnce = sync.Once{}
	default:
	}
	t.syncProbing = true
	revision := t.syncRevision
	m.mu.Unlock()
	defer func() { m.mu.Lock(); t.syncProbing = false; m.mu.Unlock() }()

	previousError := t.errText()
	before := t.state.Load().(State)
	err := m.syncTask(t, revision)
	m.mu.Lock()
	t.errMu.Lock()
	current := !t.removed.Load() && t.syncRevision == revision && t.state.Load().(State) == Done
	changed := false
	if current && err != nil {
		t.err = err.Error()
		changed = true
	} else if current && previousError != "" && t.err == previousError {
		t.err = ""
		changed = true
	}
	t.errMu.Unlock()
	m.mu.Unlock()
	if changed {
		m.notify()
	}
	if err != nil {
		return "", err
	}
	if before == Done && t.state.Load().(State) != Done {
		return trSyncStarted, nil
	}
	return trSyncUnchanged, nil
}

// 手动同步的结果说明；经调用方翻译。
var (
	trSyncStarted   = "检测到资源变化，已开始更新"
	trSyncUnchanged = "资源未变化"
)

// Queue a fresh .part transfer while keeping the published file intact.
func (m *Manager) beginSync(t *Task, revision uint64) error {
	m.mu.Lock()
	if m.updateStarting || t.removed.Load() || t.syncMinutes == 0 || t.syncRevision != revision || t.state.Load().(State) != Done || t.editing || t.verifying.Load() {
		m.mu.Unlock()
		return nil
	}
	t.editing = true
	m.mu.Unlock()
	ownsEditing := true
	defer func() {
		if ownsEditing {
			m.mu.Lock()
			t.editing = false
			m.mu.Unlock()
		}
	}()
	t.checkpointMu.Lock()
	t.metaMu.Lock()
	path, hash := t.filePath, t.publishSHA256
	t.metaMu.Unlock()
	if path == "" || !validFingerprint(hash) {
		t.checkpointMu.Unlock()
		return i18n.NewError("同步缺少已发布文件摘要，请重新下载")
	}
	if err := matchesOriginal(path, hash, t.cancel); err != nil {
		t.checkpointMu.Unlock()
		return err
	}
	f, err := os.OpenFile(extendedPath(path+".part"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.checkpointMu.Unlock()
		return err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(extendedPath(path + ".part"))
		t.checkpointMu.Unlock()
		return err
	}
	t.metaMu.Lock()
	oldHash, oldSize, oldFinished := t.publishSHA256, t.publishSize, t.finished
	oldVerified := t.verifiedSHA256
	oldSignatureVerified := t.signatureVerified.Load()
	t.signatureVerified.Store(false)
	t.syncOriginalSHA256 = hash
	t.publishSHA256, t.publishSize, t.finished, t.verifiedSHA256 = "", 0, time.Time{}, ""
	t.metaMu.Unlock()
	oldChunks, oldTotal, oldValidator, oldResource := t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource
	oldDone, oldTotalValue := t.done.Load(), t.total.Load()
	t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource = nil, 0, "", ""
	t.done.Store(0)
	t.total.Store(0)
	t.state.Store(Canceled)
	err = m.saveTaskLocked(t)
	if err != nil {
		t.signatureVerified.Store(oldSignatureVerified)
		t.metaMu.Lock()
		t.publishSHA256, t.publishSize, t.finished, t.verifiedSHA256 = oldHash, oldSize, oldFinished, oldVerified
		t.syncOriginalSHA256 = ""
		t.metaMu.Unlock()
		t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource = oldChunks, oldTotal, oldValidator, oldResource
		t.done.Store(oldDone)
		t.total.Store(oldTotalValue)
		t.state.Store(Done)
		_ = os.Remove(extendedPath(path + ".part"))
	}
	t.checkpointMu.Unlock()
	if err != nil {
		return err
	}
	m.mu.Lock()
	if t.removed.Load() {
		m.mu.Unlock()
		return i18n.NewError("任务已删除")
	}
	t.cancel = make(chan struct{})
	t.cancelOnce = sync.Once{}
	t.held.Store(false)
	t.errMu.Lock()
	t.err = ""
	t.errMu.Unlock()
	t.state.Store(Queued)
	t.editing = false
	ownsEditing = false
	ticket := m.enqueueLocked(t)
	m.mu.Unlock()
	go m.run(t, ticket)
	return nil
}

func matchesOriginal(path, expected string, cancel <-chan struct{}) error {
	info, err := os.Lstat(extendedPath(path))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return i18n.NewError("原文件已被修改，取消自动替换")
	}
	f, err := os.Open(extendedPath(path))
	if err != nil {
		return err
	}
	defer f.Close()
	actual, _, err := digestFile(f, cancel)
	if err != nil {
		return err
	}
	current, err := os.Lstat(extendedPath(path))
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || actual != expected {
		return i18n.NewError("原文件已被修改，取消自动替换")
	}
	return nil
}

func (m *Manager) SetSyncInterval(id string, minutes int) error {
	if _, err := NormalizeSyncMinutes(minutes); err != nil {
		return err
	}
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return ErrUpdateInProgress
	}
	var task *Task
	for _, t := range m.tasks {
		if t.id == id {
			task = t
			break
		}
	}
	if task == nil {
		m.mu.Unlock()
		return i18n.NewError("任务不存在")
	}
	if task.editing || task.verifying.Load() {
		m.mu.Unlock()
		return i18n.NewError("任务正在更新或校验，请稍后重试")
	}
	if minutes > 0 && (task.mediaMode != "" || (!strings.HasPrefix(task.sourceURL(), "http://") && !strings.HasPrefix(task.sourceURL(), "https://"))) {
		m.mu.Unlock()
		return i18n.NewError("周期同步仅支持普通 HTTP 或 HTTPS 文件")
	}
	task.editing = true
	task.syncRevision++
	task.metaMu.Lock()
	oldMinutes := task.syncMinutes
	task.syncMinutes = minutes
	task.metaMu.Unlock()
	oldNext := task.nextSync
	task.nextSync = time.Now().Add(time.Duration(minutes) * time.Minute)
	m.mu.Unlock()
	err := m.saveTask(task)
	m.mu.Lock()
	if err != nil {
		task.metaMu.Lock()
		task.syncMinutes = oldMinutes
		task.metaMu.Unlock()
		task.nextSync = oldNext
	}
	task.editing = false
	m.mu.Unlock()
	m.notify()
	return err
}
