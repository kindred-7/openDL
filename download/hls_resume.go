package download

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"openDL/i18n"
)

// HLS checkpoints store no segment URL or credentials. End is exclusive.
type hlsCheckpoint struct {
	End           int64  `json:"end"`
	SkippedInit   bool   `json:"skipped_init,omitempty"` // validated initialization omitted from output
	SHA256        string `json:"sha256"`
	Resource      string `json:"resource"`
	ResourceTotal int64  `json:"resource_total"`
	Validator     string `json:"validator,omitempty"`
	URL           string `json:"url,omitempty"`      // legacy records only; stripped on load/save
	LiveKey       string `json:"live_key,omitempty"` // one-way identity, never the signed URL
}

// Live playlists use this to reject initialization switches that cannot be
// reconciled safely with a rolling window.
func hasRepeatedInitialization(segments []hlsSegment) bool {
	for i := 1; i < len(segments); i++ {
		if segments[i].mp4 && segments[i].init {
			return true
		}
	}
	return false
}

// Reconstruct timing for an unspliced prefix when its saved state described a
// longer prefix. Shifted data cannot recover the original shift this way and
// must be restarted by the caller instead.
func rebuildMP4Prefix(state *hlsMP4State, out *os.File, segments []hlsSegment, records []hlsCheckpoint) error {
	if state == nil || len(records) == 0 || len(records) > len(segments) {
		return i18n.NewError("MP4 续传前缀无效")
	}
	start := records[0].End
	for i := 1; i < len(records); i++ {
		if records[i].SkippedInit || segments[i].init || segments[i].period != 0 || records[i].End <= start {
			return i18n.NewError("MP4 续传时间轴无法安全恢复")
		}
		if err := state.accept(out, start, records[i].End, false); err != nil {
			return err
		}
		start = records[i].End
	}
	return nil
}

func hlsLegacyLiveKey(address string) string {
	if address == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(address))
	return "u:" + hex.EncodeToString(sum[:])
}

func hlsLiveKey(segment hlsSegment) string {
	if !segment.hasLiveSequence {
		return hlsUnsequencedLiveKey(segment)
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s:%d:%d:%d", len(segment.url), segment.url, segment.start, segment.length, segment.liveSequence)
	return "q:" + hex.EncodeToString(h.Sum(nil))
}

func hlsUnsequencedLiveKey(segment hlsSegment) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s:%d:%d", len(segment.url), segment.url, segment.start, segment.length)
	return "s:" + hex.EncodeToString(h.Sum(nil))
}

func sanitizeHLSCheckpoint(record hlsCheckpoint) hlsCheckpoint {
	if record.URL != "" && record.LiveKey == "" {
		record.LiveKey = hlsLegacyLiveKey(record.URL)
	}
	record.URL = ""
	if len(record.LiveKey) != 66 || (record.LiveKey[:2] != "s:" && record.LiveKey[:2] != "u:" && record.LiveKey[:2] != "q:") || !validFingerprint(record.LiveKey[2:]) {
		record.LiveKey = ""
	}
	return record
}

func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// stableSegmentAddress 去掉随时间轮换的查询与片段部分，保留资源身份；签名
// 地址轮换不改变分片计划。
func stableSegmentAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.RawQuery, u.ForceQuery, u.Fragment = "", false, ""
	return u.String()
}

func hlsPlaylistFingerprint(segments []hlsSegment) string {
	h := sha256.New()
	multiPeriod := false
	for _, segment := range segments {
		if segment.period != 0 {
			multiPeriod = true
			break
		}
	}
	if len(segments) > 0 && segments[0].mp4 {
		_, _ = h.Write([]byte("fmp4\n"))
	}
	for _, segment := range segments {
		if multiPeriod {
			// Plan identity must change when the same resources are assigned to
			// a different period or change between initialization and media.
			_, _ = fmt.Fprintf(h, "period:%d:init:%t\n", segment.period, segment.init)
		}
		if segment.dashTimescale != 0 {
			_, _ = fmt.Fprintf(h, "dash-time:%d:%d:%d\n", segment.dashTimescale, segment.dashOffset, segment.dashPeriodDuration)
		}
		if segment.hlsDiscontinuity != 0 {
			_, _ = fmt.Fprintf(h, "hls-discontinuity:%d\n", segment.hlsDiscontinuity)
		}
		address := stableSegmentAddress(segment.url)
		_, _ = fmt.Fprintf(h, "%d:%s:%d:%d\n", len(address), address, segment.start, segment.length)
		if segment.keyURL != "" {
			// A key URI and IV affect the plaintext even when the segment URL
			// stays unchanged. Keep the legacy hash for unencrypted streams.
			keyAddress := stableSegmentAddress(segment.keyURL)
			_, _ = fmt.Fprintf(h, "key:%d:%s:iv:%x\n", len(keyAddress), keyAddress, segment.iv)
		}
		if segment.indexResource != "" {
			_, _ = fmt.Fprintf(h, "index:%s:%s:%d\n", segment.indexValidator, segment.indexResource, segment.indexTotal)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hlsRemoteUnchanged compares a saved strong validator, size and final URL.
// Network and authentication errors are returned, so prior data is preserved.
func (m *Manager) hlsRemoteUnchanged(t *Task, original, page *url.URL, segment hlsSegment, record hlsCheckpoint) (bool, error) {
	if record.Validator == "" {
		return false, nil
	}
	req, cancel, err := hlsRequest(t, original, page, segment.url)
	if err != nil {
		return false, err
	}
	defer cancel()
	start := segment.start
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start))
	req.Header.Set("If-Range", record.Validator)
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return false, fmt.Errorf(i18n.T("验证 HLS 分片失败（HTTP %d）"), resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return false, nil
	}
	if resp.Request == nil || resp.Request.URL == nil || resourceFingerprint(resp.Request.URL) != record.Resource || strongValidator(resp.Header) != record.Validator {
		return false, nil
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.ContentLength == record.ResourceTotal, nil
	case http.StatusPartialContent:
		want := fmt.Sprintf("bytes %d-%d/%s", start, start, strconv.FormatInt(record.ResourceTotal, 10))
		return strings.TrimSpace(resp.Header.Get("Content-Range")) == want && (resp.ContentLength == -1 || resp.ContentLength == 1), nil
	default:
		return false, nil
	}
}

// validatedHLSPrefix verifies the saved bytes and, unless a publisher hash
// protects the final output, revalidates each remote segment before reuse.
func (m *Manager) validatedHLSPrefix(t *Task, out *os.File, original, page *url.URL, segments []hlsSegment, records []hlsCheckpoint, expected string) (int, int64, map[string]hlsRangeState, error) {
	ranges := make(map[string]hlsRangeState)
	info, err := out.Stat()
	if err != nil {
		return 0, 0, nil, err
	}
	var offset int64
	count := 0
	mp4 := &hlsMP4State{}
	for i, record := range records {
		if i >= len(segments) || record.End < offset || (!record.SkippedInit && record.End == offset) || (!segments[i].mp4 && record.End%188 != 0) || record.End > info.Size() || !validFingerprint(record.SHA256) || !validFingerprint(record.Resource) {
			break
		}
		// A stable segment validator does not prove that an independently
		// hosted AES key stayed the same. Re-download from the first encrypted
		// segment, including when the final output has a publisher checksum.
		if segments[i].keyURL != "" {
			break
		}
		if record.SkippedInit {
			// The bytes were discarded after compatibility checking. A strong
			// remote validator is required even when an output hash is present.
			if i == 0 || !segments[i].mp4 || !segments[i].init || record.End != offset || record.ResourceTotal <= 0 || segments[i].length > 0 && (segments[i].start > record.ResourceTotal || segments[i].length > record.ResourceTotal-segments[i].start) {
				break
			}
			unchanged, err := m.hlsRemoteUnchanged(t, original, page, segments[i], record)
			if err != nil {
				return 0, 0, nil, err
			}
			if !unchanged {
				break
			}
			if segments[i].length > 0 {
				prior, exists := ranges[segments[i].url]
				if exists && (prior.total != record.ResourceTotal || prior.validator != record.Validator || prior.resource != record.Resource) {
					break
				}
				ranges[segments[i].url] = hlsRangeState{validator: record.Validator, total: record.ResourceTotal, resource: record.Resource}
			}
			count++
			continue
		}
		length := record.End - offset
		if segments[i].length > 0 {
			if length != segments[i].length || record.ResourceTotal <= 0 || segments[i].start > record.ResourceTotal-length {
				break
			}
		} else if record.ResourceTotal != length {
			break
		}
		select {
		case <-t.cancel:
			return 0, 0, nil, contextCanceled
		default:
		}
		digest, err := chunkDigest(out, chunkSpan{start: offset, end: record.End - 1})
		if err != nil || digest != record.SHA256 {
			break
		}
		// 重复初始化段（文件中段的初始化段）由复用判定处理，这里只做字节
		// 校验；其余分片继续走容器校验。
		if segments[i].mp4 && !(segments[i].init && offset > 0) && mp4.accept(out, offset, record.End, segments[i].init) != nil {
			break
		}
		if expected == "" {
			unchanged, err := m.hlsRemoteUnchanged(t, original, page, segments[i], record)
			if err != nil {
				return 0, 0, nil, err
			}
			if !unchanged {
				break
			}
		}
		if segments[i].length > 0 {
			prior, exists := ranges[segments[i].url]
			if exists && (prior.total != record.ResourceTotal || prior.validator != record.Validator || (expected == "" && prior.resource != record.Resource)) {
				break
			}
			ranges[segments[i].url] = hlsRangeState{validator: record.Validator, total: record.ResourceTotal, resource: record.Resource}
		}
		offset = record.End
		count++
	}
	// Only the verified prefix may survive a mismatch.
	return count, offset, ranges, nil
}
