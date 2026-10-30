package download

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http/httpguts"

	"openDL/i18n"
)

// UpdateSource replaces a stopped task's address and request headers. A new
// URL cannot be assumed to identify the same resource even when ETags match;
// its old checkpoints are discarded before another transfer can start.
// Call from a worker because the journal may require disk synchronization.
func (m *Manager) UpdateSource(id, address string, headers map[string]string) (bool, error) {
	return m.updateSource(id, address, headers, nil, nil, nil)
}

// UpdateSourceWithSHA256 also updates the optional publisher checksum and the
// Ed25519 detached signature covering the SHA-256 digest.
func (m *Manager) UpdateSourceWithSHA256(id, address string, headers map[string]string, expected, signatureKey, signatureText string) (bool, error) {
	value, err := NormalizeSHA256(expected)
	if err != nil {
		return false, err
	}
	key, err := NormalizeSignatureKey(signatureKey)
	if err != nil {
		return false, err
	}
	detached, err := NormalizeDetachedSignature(signatureText)
	if err != nil {
		return false, err
	}
	return m.updateSource(id, address, headers, &value, &key, &detached)
}

func (m *Manager) updateSource(id, address string, headers map[string]string, expected, signatureKey, signatureText *string) (bool, error) {
	address = strings.TrimSpace(address)
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false, fmt.Errorf(i18n.T("请输入有效的 HTTP 或 HTTPS 下载地址"))
	}
	for name, value := range headers {
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return false, fmt.Errorf(i18n.T("请求头格式无效：%s"), name)
		}
	}
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return false, ErrUpdateInProgress
	}
	var t *Task
	for _, candidate := range m.tasks {
		if candidate.id == id {
			t = candidate
			break
		}
	}
	if t == nil {
		m.mu.Unlock()
		return false, fmt.Errorf(i18n.T("任务不存在"))
	}
	state := t.state.Load().(State)
	if t.editing || t.verifying.Load() || state == Done || state == Running || (state == Queued && !t.held.Load()) {
		m.mu.Unlock()
		return false, fmt.Errorf(i18n.T("请先暂停任务，再更新下载信息"))
	}
	t.editing = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); t.editing = false; m.mu.Unlock() }()

	t.checkpointMu.Lock()
	defer t.checkpointMu.Unlock()
	if t.removed.Load() {
		return false, fmt.Errorf(i18n.T("任务已删除"))
	}
	t.metaMu.Lock()
	oldURL, oldHeaders, oldExpected, oldVerified := t.url, t.headers, t.expectedSHA256, t.verifiedSHA256
	oldSignatureText, oldSignatureKey := t.signature, t.signatureKey
	oldFilePath := t.filePath
	oldSignatureVerified := t.signatureVerified.Load()
	oldAuthProtocol := t.authProtocol
	t.authProtocol = ""
	oldPublishHash, oldPublishSize := t.publishSHA256, t.publishSize
	changed := oldURL != address
	t.url, t.headers = address, cloneHeaders(headers)
	if expected != nil {
		t.expectedSHA256 = *expected
	}
	if signatureKey != nil {
		t.signatureKey = *signatureKey
	}
	if signatureText != nil {
		t.signature = *signatureText
	}
	if changed || (expected != nil && *expected != oldExpected) || (signatureText != nil && *signatureText != oldSignatureText) || (signatureKey != nil && *signatureKey != oldSignatureKey) {
		t.verifiedSHA256 = ""
		t.signatureVerified.Store(false)
	}
	// A new URL may identify different content. A new checksum invalidates
	// publication only when it conflicts with the saved complete file.
	if changed || (expected != nil && *expected != "" && *expected != t.publishSHA256) {
		t.publishSHA256, t.publishSize = "", 0
	}
	t.metaMu.Unlock()
	t.clearDigest()
	oldCompleted, oldTotal, oldValidator, oldResource := t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource
	oldHLSPlaylist, oldHLSCompleted := t.hlsPlaylist, t.hlsCompleted
	oldHLSSplice := t.hlsSplice
	oldHLSFormat := t.hlsFormat
	oldDASHVideo, oldDASHAudio := t.dashVideo, t.dashAudio
	oldDASHPeriods := t.dashPeriods
	oldRemoteValidator, oldRemoteResource := t.remoteValidator, t.remoteResource
	oldAuth, oldError := t.needsAuth.Load(), t.errText()
	if changed {
		t.remoteValidator, t.remoteResource = "", ""
		t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource = nil, 0, "", ""
		t.hlsPlaylist, t.hlsCompleted = "", nil
		t.hlsSplice = nil
		t.hlsFormat = ""
		t.dashVideo, t.dashAudio = dashTrackCheckpoint{}, dashTrackCheckpoint{}
		t.dashPeriods = [8]dashPeriodCheckpoint{}
	}
	t.needsAuth.Store(false)
	t.errMu.Lock()
	t.err = ""
	t.errMu.Unlock()
	if err := m.saveTaskLocked(t); err != nil {
		t.metaMu.Lock()
		t.url, t.headers, t.expectedSHA256, t.verifiedSHA256 = oldURL, oldHeaders, oldExpected, oldVerified
		t.signature, t.signatureKey = oldSignatureText, oldSignatureKey
		t.signatureVerified.Store(oldSignatureVerified)
		t.authProtocol = oldAuthProtocol
		t.publishSHA256, t.publishSize = oldPublishHash, oldPublishSize
		t.metaMu.Unlock()
		t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource = oldCompleted, oldTotal, oldValidator, oldResource
		t.hlsPlaylist, t.hlsCompleted = oldHLSPlaylist, oldHLSCompleted
		t.hlsSplice = oldHLSSplice
		t.hlsFormat = oldHLSFormat
		t.dashVideo, t.dashAudio = oldDASHVideo, oldDASHAudio
		t.dashPeriods = oldDASHPeriods
		t.needsAuth.Store(oldAuth)
		t.errMu.Lock()
		t.err = oldError
		t.errMu.Unlock()
		t.remoteValidator, t.remoteResource = oldRemoteValidator, oldRemoteResource
		return false, fmt.Errorf(i18n.T("无法保存下载信息：%w"), err)
	}
	if changed {
		if (t.mediaMode == "dash-fmp4" || t.mediaMode == "hls-ts") && oldFilePath != "" {
			_ = os.Remove(extendedPath(oldFilePath + ".part." + t.id + ".video"))
			_ = os.Remove(extendedPath(oldFilePath + ".part." + t.id + ".audio"))
			for i, period := range oldDASHPeriods {
				if period.Video.Playlist != "" {
					removeOwnedTrack(dashPeriodPath(oldFilePath, t.id, i, "video"))
				}
				if period.Audio.Playlist != "" {
					removeOwnedTrack(dashPeriodPath(oldFilePath, t.id, i, "audio"))
				}
			}
		}
		t.done.Store(0)
		t.total.Store(0)
		t.rangeSupport.Store(0)
		t.resumeSafe.Store(false)
		t.resetSegments(nil)
	}
	if t.removed.Load() {
		return false, fmt.Errorf(i18n.T("任务已删除"))
	}
	go m.notify()
	return changed, nil
}
