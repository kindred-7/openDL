package download

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"openDL/i18n"
	"openDL/pathrec"
)

func (m *Manager) downloadDASH(t *Task) error {
	original, err := url.Parse(t.sourceURL())
	if err != nil {
		return err
	}
	data, base, err := m.readHLSPlaylist(t, original, original, original.String())
	if err != nil {
		return err
	}
	manifest, err := parseDASHManifest(data, base)
	if err != nil {
		return err
	}
	video, audio, err := m.selectAvailableDASHTracks(t, original, base, manifest)
	if err != nil {
		return err
	}
	if manifest.Dynamic {
		if video != nil && audio != nil {
			return m.downloadDASHLiveDual(t, original, base, video, audio)
		}
		track := video
		if track == nil {
			track = audio
		}
		liveTrack := newDualLiveTrack(base, track.Segments)
		liveTrack.dashSelected = track
		return m.downloadDASHLiveSingle(t, original, base, liveTrack)
	}
	if video == nil || audio == nil {
		// Reuse the selected snapshot instead of fetching a potentially changed
		// manifest a second time. Keep the existing single-track checkpoint path.
		track := video
		if track == nil {
			track = audio
		}
		t.metaMu.Lock()
		expected := t.expectedSHA256
		t.metaMu.Unlock()
		expected, err = NormalizeSHA256(expected)
		if err != nil {
			return err
		}
		err = m.downloadResolvedMedia(t, original, base, track.Segments, false, expected)
	} else {
		err = m.downloadDualMedia(t, original, base, base, video.Segments, audio.Segments)
	}
	if manifest.PeriodCount > 1 && errors.Is(err, errMP4InitIncompatible) {
		return m.downloadDASHSeparatePeriods(t, original, base, base, video, audio, manifest.PeriodCount)
	}
	return err
}

// dualTrackTransfer 把两个已打开的轨道文件填满；点播传完整分片，直播逐批
// 追加。
type dualTrackTransfer func(video, audio *os.File) error

func (m *Manager) downloadDualMedia(t *Task, original, videoBase, audioBase *url.URL, video, audio []hlsSegment) error {
	return m.downloadDualMediaWith(t, func(videoFile, audioFile *os.File) error {
		if err := m.downloadDASHTrack(t, videoFile, original, videoBase, video, &t.dashVideo); err != nil {
			return err
		}
		return m.downloadDASHTrack(t, audioFile, original, audioBase, audio, &t.dashAudio)
	})
}

func (m *Manager) downloadDualMediaWith(t *Task, transfer dualTrackTransfer) error {
	program, err := m.resolveMediaMuxProgram()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(t.directory, 0o755); err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	if filePath == "" {
		name := t.nameHint
		if name == "" {
			name = pathrec.DeriveFilename(pathrec.Source{URL: t.sourceURL(), Title: t.title})
		}
		name = pathrec.SanitizeFilename(name)
		ext := filepath.Ext(name)
		if strings.EqualFold(ext, ".mpd") || strings.EqualFold(ext, ".m3u8") || strings.EqualFold(ext, ".ts") || strings.EqualFold(ext, ".mp4") {
			name = strings.TrimSuffix(name, ext)
		}
		filePath, _, err = claimPath(t.directory, pathrec.SanitizeFilename(name+".mp4"))
		if err != nil {
			return err
		}
		t.metaMu.Lock()
		t.filePath = filePath
		t.metaMu.Unlock()
		if err = m.saveTask(t); err != nil {
			return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
		}
	}
	if !strings.EqualFold(filepath.Ext(filePath), ".mp4") {
		return i18n.NewError("DASH 媒体格式已改变，请新建任务")
	}
	part := filePath + ".part"
	videoPath, audioPath := part+"."+t.id+".video", part+"."+t.id+".audio"
	videoFile, err := openDASHTrackFile(videoPath, t.dashVideo)
	if err != nil {
		return err
	}
	defer videoFile.Close()
	audioFile, err := openDASHTrackFile(audioPath, t.dashAudio)
	if err != nil {
		return err
	}
	defer audioFile.Close()
	if err = m.prepareOutputLimit(t, videoFile, -1); err != nil {
		return err
	}
	t.done.Store(0)
	t.total.Store(-1)
	t.rangeSupport.Store(0)
	t.resumeSafe.Store(false)
	t.resetSegments(nil)
	if err = transfer(videoFile, audioFile); err != nil {
		return err
	}
	if err = videoFile.Close(); err != nil {
		return err
	}
	if err = audioFile.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		select {
		case <-t.cancel:
			cancel()
		case <-finished:
		}
	}()
	absVideo, err := filepath.Abs(videoPath)
	if err != nil {
		close(finished)
		return err
	}
	absAudio, err := filepath.Abs(audioPath)
	if err != nil {
		close(finished)
		return err
	}
	absDirectory, err := filepath.Abs(t.directory)
	if err != nil {
		close(finished)
		return err
	}
	muxed, err := muxLocalTracks(ctx, program, absVideo, absAudio, absDirectory)
	close(finished)
	if err != nil {
		if ctx.Err() != nil {
			return contextCanceled
		}
		return err
	}
	defer os.Remove(muxed)
	info, err := os.Stat(muxed)
	if err != nil {
		return err
	}
	if err = checkDeclaredFileSize(t.outputLimit.Load(), info.Size()); err != nil {
		return err
	}
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	if err = replaceSyncedFile(muxed, part); err != nil {
		return err
	}
	t.done.Store(info.Size())
	t.total.Store(info.Size())
	if err = m.finishFile(t, part); err != nil {
		return err
	}
	cleanupDualMedia(t)
	return nil
}

// Only remove task-specific track files after the final output is published
// or independently recovered. Failed and canceled tasks keep their inputs.
func cleanupDualMedia(t *Task) {
	if t.mediaMode != "hls-ts" && t.mediaMode != "dash-fmp4" {
		return
	}
	t.metaMu.Lock()
	path := t.filePath
	t.metaMu.Unlock()
	if path == "" {
		return
	}
	t.checkpointMu.Lock()
	video, audio := t.dashVideo.Playlist != "", t.dashAudio.Playlist != ""
	periods := t.dashPeriods
	t.checkpointMu.Unlock()
	if video {
		removeOwnedTrack(path + ".part." + t.id + ".video")
	}
	if audio {
		removeOwnedTrack(path + ".part." + t.id + ".audio")
	}
	for i, period := range periods {
		if period.Video.Playlist != "" {
			removeOwnedTrack(dashPeriodPath(path, t.id, i, "video"))
		}
		if period.Audio.Playlist != "" {
			removeOwnedTrack(dashPeriodPath(path, t.id, i, "audio"))
		}
	}
}

func dashPeriodPath(path, id string, period int, kind string) string {
	return fmt.Sprintf("%s.part.%s.period-%d.%s", path, id, period, kind)
}

func removeOwnedTrack(path string) {
	info, err := os.Lstat(extendedPath(path))
	if err == nil && info.Mode().IsRegular() {
		_ = os.Remove(extendedPath(path))
	}
}

// Task-specific paths prevent tasks with the same file name from sharing
// intermediate content. Existing files are reused only with a saved plan.
func openDASHTrackFile(path string, state dashTrackCheckpoint) (*os.File, error) {
	f, err := os.OpenFile(extendedPath(path), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		return f, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(extendedPath(path))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (state.Playlist == "" && info.Size() != 0) {
		return nil, i18n.NewError("DASH 轨道临时文件已存在且无法确认归属")
	}
	f, err = os.OpenFile(extendedPath(path), os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, i18n.NewError("DASH 轨道临时文件已存在且无法确认归属")
	}
	return f, nil
}
