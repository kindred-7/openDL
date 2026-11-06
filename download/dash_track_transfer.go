package download

import (
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
	rest := segments[count:]
	journal := &mediaJournal{m: m, t: t}
	defer journal.flushLog()
	// commitDownloaded 在分片字节落盘后执行容器校验、时间轴缝合、摘要与检查
	// 点记账。record.SHA256 是写入阶段的增量摘要；时间轴平移改写过的分片退
	// 回 chunkDigest 磁盘回读，摘要才与落盘字节一致。
	commitDownloaded := func(segment hlsSegment, record *hlsCheckpoint, start, aggregate int64) error {
		newOffset, err := out.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if newOffset <= start {
			return i18n.NewError("DASH 轨道分片长度无效")
		}
		if segment.mp4 && segment.init && len(mp4.tracks) > 0 {
			// 重复初始化段：字节一致或结构一致时跳过写入并缝合时间轴；打包时
			// 间戳等附带差异不影响结构指纹。
			if _, err := reuseInitialization(mp4, out, start, newOffset); err != nil {
				return fmt.Errorf(i18n.T("无法校验 DASH 轨道分片：%w"), err)
			}
			mp4.pendingShift = true
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
			offset = start
			record.End, record.SkippedInit = start, true
			t.checkpointMu.Lock()
			state.Completed = append(state.Completed, *record)
			state.Splice = mp4.spliceState()
			t.checkpointMu.Unlock()
			if err := journal.saved(); err != nil {
				return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
			}
			return nil
		}
		if segment.mp4 && !segment.init {
			mp4.advancePeriod(segment.period)
			if mp4.pendingShift || mp4.shiftActive {
				if err := mp4.shiftTfdt(out, start, newOffset); err != nil {
					return fmt.Errorf(i18n.T("DASH MP4 分片无效：%w"), err)
				}
				// 平移改写了落盘字节，增量摘要作废，退回磁盘回读。
				digest, err := chunkDigest(out, chunkSpan{start: start, end: newOffset - 1})
				if err != nil {
					return fmt.Errorf(i18n.T("无法校验 DASH 轨道分片：%w"), err)
				}
				record.SHA256 = digest
			}
		}
		if segment.mp4 {
			if err := mp4.accept(out, start, newOffset, segment.init); err != nil {
				return fmt.Errorf(i18n.T("DASH MP4 分片无效：%w"), err)
			}
			if segment.init {
				captureInitialization(mp4, out, start, newOffset)
			}
		}
		if err := out.Sync(); err != nil {
			return fmt.Errorf(i18n.T("无法同步 DASH 轨道分片：%w"), err)
		}
		record.End = newOffset
		offset = newOffset
		t.checkpointMu.Lock()
		state.Completed = append(state.Completed, *record)
		state.Splice = mp4.spliceState()
		t.checkpointMu.Unlock()
		if err := journal.saved(); err != nil {
			return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
		}
		return nil
	}
	// appendOne 串行下载并提交一个分片（预取超限时的回退路径）。
	appendOne := func(i int) error {
		segment := rest[i]
		start := offset
		aggregate := t.done.Load()
		previous, existed := ranges[segment.url]
		var record hlsCheckpoint
		err := retryWithBackoff(t.cancel, maxRequestAttempts, 250*time.Millisecond, func() error {
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
			return nil
		}, func() error {
			record = hlsCheckpoint{}
			return m.downloadHLSSegment(t, out, progress, original, base, segment, ranges, &record)
		})
		if err != nil {
			return err
		}
		return commitDownloaded(segment, &record, start, aggregate)
	}
	// 点播 DASH 分片同样按有界流水线预取：并发取回、按序提交，fMP4 时间轴
	// 缝合与检查点记账仍严格按分片顺序执行。
	commit := func(i int, f *fetchedSegment) error {
		segment := rest[i]
		start := offset
		aggregate := t.done.Load()
		var target io.Writer = out
		if !segment.mp4 {
			target = &tsWriter{w: out}
		}
		writer := &segmentWriter{writer: target, segment: progress}
		written, err := writeHLSPlaintext(t, writer, f.payload)
		if err != nil {
			return err
		}
		if written != int64(len(f.payload)) {
			return io.ErrShortWrite
		}
		return commitDownloaded(segment, &f.record, start, aggregate)
	}
	if err := m.prefetchMediaSegments(t, original, base, rest, ranges, commit, appendOne); err != nil {
		return err
	}
	if err := journal.flush(); err != nil {
		return fmt.Errorf(i18n.T("无法保存 DASH 轨道进度：%w"), err)
	}
	return nil
}
