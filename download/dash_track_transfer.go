package download

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"openDL/i18n"
)

// downloadDASHTrack transfers one representation to an already-open private
// file. The caller initializes task progress and runs tracks sequentially.
// Offsets in state refer to this file; t.done includes all completed tracks.
// An output checksum describes the eventual mux, not either input, so cached
// track segments always require independent remote validator checks.
func (m *Manager) downloadDASHTrack(t *Task, out *os.File, original, base *url.URL, segments []hlsSegment, state *dashTrackCheckpoint) error {
	if out == nil || original == nil || base == nil || state == nil || len(segments) == 0 || (segments[0].mp4 && !segments[0].init) {
		return i18n.NewError("DASH 轨道分片计划无效")
	}
	fingerprint := hlsPlaylistFingerprint(segments)
	t.checkpointMu.Lock()
	saved := cloneDASHCheckpoint(*state)
	t.checkpointMu.Unlock()
	// 指纹语义变更后，旧版已拼接内容不能继续沿用。
	if saved.Splice != nil && saved.Splice.FingerprintVersion != mp4FingerprintVersion {
		saved = dashTrackCheckpoint{}
	}
	count, offset := 0, int64(0)
	ranges := make(map[string]hlsRangeState)
	// 缝合状态进检查点后多周期轨道可续传；旧检查点没有缝合状态时，多周期
	// 轨道仍从头拼接，避免丢失平移基准。
	if saved.Playlist == fingerprint && (saved.Splice != nil || segments[len(segments)-1].period == 0) {
		var err error
		count, offset, ranges, err = m.validatedHLSPrefix(t, out, original, base, segments, saved.Completed, "")
		if err != nil {
			return fmt.Errorf(i18n.T("无法验证 DASH 轨道续传：%w"), err)
		}
		// 缝合状态对应完整校验的前缀；部分校验的前缀含缝合内容时整体重拼。
		if count < len(saved.Completed) && saved.Splice != nil && (saved.Splice.Period > 0 || saved.Splice.ShiftActive || saved.Splice.PendingShift) {
			count, offset = 0, 0
			ranges = make(map[string]hlsRangeState)
			saved.Splice = nil
		}
		if count < len(saved.Completed) {
			// The saved timing belonged to the entire old prefix.
			saved.Splice = nil
		}
	}
	t.checkpointMu.Lock()
	state.Playlist = fingerprint
	state.Completed = append([]hlsCheckpoint(nil), saved.Completed[:count]...)
	state.Splice = saved.Splice
	if count == 0 {
		state.Splice = nil
	}
	err := m.saveTaskLocked(t)
	t.checkpointMu.Unlock()
	if err != nil {
		return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
	}
	if err := out.Truncate(offset); err != nil {
		return err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	t.done.Add(offset)
	progress := t.startSegment(chunkSpan{start: 0, end: -1})
	progress.done.Store(offset)
	defer t.removeSegment(progress)
	mp4 := &hlsMP4State{}
	if count > 0 && segments[0].mp4 {
		if err := mp4.accept(out, 0, saved.Completed[0].End, true); err != nil {
			return fmt.Errorf(i18n.T("DASH 初始化段无效：%w"), err)
		}
		// 恢复的初始化段同样登记指纹，重复初始化段才能继续复用。
		captureInitialization(mp4, out, 0, saved.Completed[0].End)
		if saved.Splice != nil {
			mp4.restoreSplice(saved.Splice)
		} else if err := rebuildMP4Prefix(mp4, out, segments, saved.Completed[:count]); err != nil {
			return fmt.Errorf(i18n.T("DASH 续传时间轴无效：%w"), err)
		}
	}
	for _, segment := range segments[count:] {
		start := offset
		aggregate := t.done.Load()
		previous, existed := ranges[segment.url]
		var record hlsCheckpoint
		for attempt := 0; ; attempt++ {
			record = hlsCheckpoint{}
			err = m.downloadHLSSegment(t, out, progress, original, base, segment, ranges, &record)
			if err == nil {
				break
			}
			var readErr *responseReadError
			if !errors.As(err, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
				return err
			}
			if err := waitRetry(t.cancel, time.Duration(250*(1<<attempt))*time.Millisecond); err != nil {
				return err
			}
			if err := out.Truncate(start); err != nil {
				return err
			}
			if err := out.Sync(); err != nil {
				return fmt.Errorf(i18n.T("无法同步 DASH 轨道分片：%w"), err)
			}
			if _, err := out.Seek(start, io.SeekStart); err != nil {
				return err
			}
			t.done.Store(aggregate)
			progress.done.Store(start)
			if existed {
				ranges[segment.url] = previous
			} else {
				delete(ranges, segment.url)
			}
		}
		offset, err = out.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if offset <= start {
			return i18n.NewError("DASH 轨道分片长度无效")
		}
		if segment.mp4 && segment.init && len(mp4.tracks) > 0 {
			// 重复初始化段：字节一致或结构一致时跳过写入并缝合时间轴；打包时
			// 间戳等附带差异不影响结构指纹。
			if _, err := reuseInitialization(mp4, out, start, offset); err != nil {
				return fmt.Errorf(i18n.T("无法校验 DASH 轨道分片：%w"), err)
			}
			record.SHA256, err = chunkDigest(out, chunkSpan{start: start, end: offset - 1})
			if err != nil {
				return err
			}
			mp4.pendingShift = true
			if err := out.Truncate(start); err != nil {
				return err
			}
			if _, err := out.Seek(start, io.SeekStart); err != nil {
				return err
			}
			t.done.Store(aggregate)
			progress.done.Store(start)
			offset = start
			record.End, record.SkippedInit = start, true
			t.checkpointMu.Lock()
			state.Completed = append(state.Completed, record)
			state.Splice = mp4.spliceState()
			err = m.saveTaskLocked(t)
			t.checkpointMu.Unlock()
			if err != nil {
				return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
			}
			continue
		}
		if segment.mp4 && !segment.init {
			mp4.advancePeriod(segment.period)
			if mp4.pendingShift || mp4.shiftActive {
				if err := mp4.shiftTfdt(out, start, offset); err != nil {
					return fmt.Errorf(i18n.T("DASH MP4 分片无效：%w"), err)
				}
			}
		}
		if segment.mp4 {
			if err := mp4.accept(out, start, offset, segment.init); err != nil {
				return fmt.Errorf(i18n.T("DASH MP4 分片无效：%w"), err)
			}
			if segment.init {
				captureInitialization(mp4, out, start, offset)
			}
		}
		if err := out.Sync(); err != nil {
			return fmt.Errorf(i18n.T("无法同步 DASH 轨道分片：%w"), err)
		}
		record.End = offset
		record.SHA256, err = chunkDigest(out, chunkSpan{start: start, end: offset - 1})
		if err != nil {
			return fmt.Errorf(i18n.T("无法校验 DASH 轨道分片：%w"), err)
		}
		t.checkpointMu.Lock()
		state.Completed = append(state.Completed, record)
		state.Splice = mp4.spliceState()
		err = m.saveTaskLocked(t)
		t.checkpointMu.Unlock()
		if err != nil {
			return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
		}
	}
	return nil
}
