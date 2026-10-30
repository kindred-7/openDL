package download

// 直播独立音轨：跟随视频与音频清单的滚动窗口，把新分片追加到各自轨道文
// 件，两轨都出现结束标签后混流发布；普通取消保留临时文件。直播窗口滚动，历史分片无
// 法回头校验，轨道按分片身份去重。

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"openDL/i18n"
)

// downloadDualMediaLive 直播双轨录制。
func (m *Manager) downloadDualMediaLive(t *Task, original, videoBase, audioBase *url.URL, video, audio []hlsSegment) error {
	return m.downloadDualMediaWith(t, func(videoFile, audioFile *os.File) error {
		return m.recordDualLive(t, original, videoBase, audioBase, video, audio, videoFile, audioFile)
	})
}

// dualLiveTrack 跟随一条直播清单的滚动窗口。
type dualLiveTrack struct {
	base           *url.URL
	dashSelected   *dashTrack // fixed representation for the current recording
	seen           map[string]bool
	order          []string
	ranges         map[string]hlsRangeState
	rangeOrder     []string
	pending        []hlsSegment
	ended          bool
	mp4            *hlsMP4State
	initialization *hlsSegment
}

func newDualLiveTrack(base *url.URL, window []hlsSegment) *dualLiveTrack {
	track := &dualLiveTrack{base: base, seen: map[string]bool{}, ranges: map[string]hlsRangeState{}, mp4: &hlsMP4State{}, initialization: hlsWindowInitialization(window)}
	track.absorb(window)
	return track
}

// absorb 记录窗口内尚未录制的分片；媒体序号可区分复用同一地址的新分片。
func (track *dualLiveTrack) absorb(window []hlsSegment) {
	for _, segment := range window {
		key := hlsLiveKey(segment)
		if track.seen[key] {
			continue
		}
		if len(track.order) >= 16384 {
			delete(track.seen, track.order[0])
			track.order = track.order[1:]
		}
		track.seen[key] = true
		track.order = append(track.order, key)
		track.pending = append(track.pending, segment)
	}
}

// recordDualLive 双轨跟随直播清单录加分片；两轨都结束后返回，由调
// 用方混流发布。
func (m *Manager) recordDualLive(t *Task, original, videoBase, audioBase *url.URL, video, audio []hlsSegment, videoFile, audioFile *os.File) error {
	wake := t.beginLive()
	m.notify()
	defer func() { t.endLive(); m.notify() }()
	tracks := []*dualLiveTrack{newDualLiveTrack(videoBase, video), newDualLiveTrack(audioBase, audio)}
	files := []*os.File{videoFile, audioFile}
	for {
		for i, track := range tracks {
			if err := m.appendDualTrack(t, original, track, files[i]); err != nil {
				return err
			}
		}
		if t.finishLive.Load() || (tracks[0].ended && tracks[1].ended) {
			return nil
		}
		select {
		case <-t.cancel:
			return contextCanceled
		case <-wake:
			return nil
		case <-time.After(3 * time.Second):
		}
		if t.finishLive.Load() {
			return nil
		}
		for _, track := range tracks {
			if track.ended {
				continue
			}
			data, base, err := m.readHLSPlaylistWithWake(t, original, track.base, track.base.String(), wake)
			select {
			case <-t.cancel:
				return contextCanceled
			default:
			}
			// A refresh can finish after the stop request. Discard any newly
			// queued window and publish only the last complete pair of batches.
			if t.finishLive.Load() {
				return nil
			}
			if err != nil {
				// 清单瞬时不可读，下一轮再试。
				continue
			}
			track.base = base
			segments, ended, parseErr := parseHLSWindow(data, base)
			if parseErr != nil {
				if errors.Is(parseErr, errHLSLiveInitSwitch) {
					return parseErr
				}
				if !ended {
					continue
				}
				return parseErr
			}
			if hasRepeatedInitialization(segments) || hlsLiveInitializationChanged(track.initialization, segments) {
				return errHLSLiveInitSwitch
			}
			track.absorb(segments)
			track.ended = track.ended || ended
		}
	}
}

// appendDualTrack 把待录分片顺序追加到轨道文件；临时读取错误有限重试，
// 失败与停止都丢弃未完成的分片尾部，避免混流读到半截分片。
func (m *Manager) appendDualTrack(t *Task, original *url.URL, track *dualLiveTrack, out *os.File) error {
	if len(track.pending) == 0 {
		return nil
	}
	progress := t.startSegment(chunkSpan{start: 0, end: -1})
	defer t.removeSegment(progress)
	for len(track.pending) > 0 {
		segment := track.pending[0]
		_, rangeSeen := track.ranges[segment.url]
		start, err := out.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		aggregate := t.done.Load()
		rollback := func() {
			_ = out.Truncate(start)
			_, _ = out.Seek(start, io.SeekStart)
			t.done.Store(aggregate)
			progress.done.Store(start)
		}
		var record hlsCheckpoint
		var lastErr error
		for attempt := 0; ; attempt++ {
			record = hlsCheckpoint{}
			lastErr = m.downloadHLSSegment(t, out, progress, original, track.base, segment, track.ranges, &record)
			if lastErr == nil {
				break
			}
			var readErr *responseReadError
			if !errors.As(lastErr, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
				break
			}
			if err := waitRetry(t.cancel, time.Duration(250*(1<<attempt))*time.Millisecond); err != nil {
				lastErr = err
				break
			}
			rollback()
		}
		if lastErr != nil {
			rollback()
			return lastErr
		}
		if segment.length > 0 && !rangeSeen {
			if _, ok := track.ranges[segment.url]; ok {
				track.rangeOrder = append(track.rangeOrder, segment.url)
				if len(track.rangeOrder) > 16384 {
					delete(track.ranges, track.rangeOrder[0])
					track.rangeOrder = track.rangeOrder[1:]
				}
			}
		}
		end, err := out.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if end <= start {
			return i18n.NewError("直播轨道分片长度无效")
		}
		if segment.mp4 {
			if segment.init && len(track.mp4.tracks) > 0 {
				// 重复初始化段：一致时跳过写入并缝合时间轴。
				if _, err := reuseInitialization(track.mp4, out, start, end); err != nil {
					rollback()
					return err
				}
				rollback()
				track.pending = track.pending[1:]
				continue
			}
			if !segment.init && (track.mp4.pendingShift || track.mp4.shiftActive) {
				if err := track.mp4.shiftTfdt(out, start, end); err != nil {
					return fmt.Errorf(i18n.T("HLS MP4 分片无效：%w"), err)
				}
			}
			if err := track.mp4.accept(out, start, end, segment.init); err != nil {
				return fmt.Errorf(i18n.T("HLS MP4 分片无效：%w"), err)
			}
			if segment.init {
				captureInitialization(track.mp4, out, start, end)
			}
		}
		if err := out.Sync(); err != nil {
			return fmt.Errorf(i18n.T("无法同步 HLS 分片：%w"), err)
		}
		track.pending = track.pending[1:]
	}
	return nil
}
