package download

// DASH 直播录制：跟随动态清单的滚动窗口追加分片。普通取消不会发布；
// 用户可以请求完成当前批次后进行收尾发布。

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openDL/i18n"
	"openDL/pathrec"
)

// downloadDASHLiveDual 直播双轨录制。
func (m *Manager) downloadDASHLiveDual(t *Task, original, manifestURL *url.URL, video, audio *dashTrack) error {
	return m.downloadDualMediaWith(t, func(videoFile, audioFile *os.File) error {
		tracks := []*dualLiveTrack{newDualLiveTrack(manifestURL, video.Segments), newDualLiveTrack(manifestURL, audio.Segments)}
		tracks[0].dashSelected, tracks[1].dashSelected = video, audio
		return m.recordDASHLive(t, original, manifestURL, tracks, []*os.File{videoFile, audioFile})
	})
}

// downloadDASHLiveSingle 直播单轨录制。
func (m *Manager) downloadDASHLiveSingle(t *Task, original, manifestURL *url.URL, track *dualLiveTrack) error {
	name := t.nameHint
	if name == "" {
		name = pathrec.DeriveFilename(pathrec.Source{URL: t.sourceURL(), Title: t.title})
	}
	name = pathrec.SanitizeFilename(name)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mpd", ".m3u8", ".ts", ".mp4":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	if err := os.MkdirAll(t.directory, 0o755); err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	part := filePath + ".part"
	if filePath == "" {
		var err error
		filePath, part, err = claimPath(t.directory, pathrec.SanitizeFilename(name+".mp4"))
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
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if info.Size() != 0 {
		return i18n.NewError("DASH 直播临时文件已有内容，无法安全续录；请新建任务")
	}
	if err := m.prepareOutputLimit(t, out, -1); err != nil {
		return err
	}
	t.done.Store(0)
	t.total.Store(-1)
	t.rangeSupport.Store(0)
	t.resumeSafe.Store(false)
	t.resetSegments(nil)
	if err := m.recordDASHLive(t, original, manifestURL, []*dualLiveTrack{track}, []*os.File{out}); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf(i18n.T("无法同步下载文件：%w"), err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return m.finishFile(t, part)
}

// recordDASHLive 定期重取动态清单并追加新分片。清单瞬时不可读时下一轮再试。
func (m *Manager) recordDASHLive(t *Task, original, manifestURL *url.URL, tracks []*dualLiveTrack, files []*os.File) error {
	wake := t.beginLive()
	m.notify()
	defer func() { t.endLive(); m.notify() }()
	for {
		for i, track := range tracks {
			if err := m.appendDualTrack(t, original, track, files[i]); err != nil {
				return err
			}
		}
		if t.finishLive.Load() {
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
		data, base, err := m.readHLSPlaylistWithWake(t, original, manifestURL, manifestURL.String(), wake)
		select {
		case <-t.cancel:
			return contextCanceled
		default:
		}
		if t.finishLive.Load() {
			return nil
		}
		if err != nil {
			continue
		}
		manifest, err := parseDASHManifest(data, base)
		if err != nil {
			continue
		}
		for _, track := range tracks {
			if t.finishLive.Load() {
				return nil
			}
			selected, err := matchDASHLiveTrack(manifest, track.dashSelected)
			if err != nil {
				return err
			}
			if selected == nil {
				continue
			}
			track.absorb(selected.Segments)
		}
	}
}

// Keep the original representation throughout the recording. Refreshes can
// change which representation ranks highest; that must not mix two timelines.
func matchDASHLiveTrack(manifest *dashManifest, original *dashTrack) (*dashTrack, error) {
	if manifest == nil || original == nil {
		return nil, i18n.NewError("DASH 直播轨道身份无效")
	}
	var match *dashTrack
	for i := range manifest.Tracks {
		candidate := &manifest.Tracks[i]
		if candidate.Kind != original.Kind || candidate.ID != original.ID || candidate.Language != original.Language {
			continue
		}
		if original.ID == "" && (candidate.Bandwidth != original.Bandwidth || candidate.Height != original.Height || candidate.Codecs != original.Codecs || candidate.MIME != original.MIME) {
			continue
		}
		if match != nil {
			return nil, i18n.NewError("DASH 直播轨道身份不唯一")
		}
		match = candidate
	}
	if match == nil {
		return nil, nil // temporarily absent from the rolling snapshot
	}
	if match.Index != nil {
		return nil, i18n.NewError("DASH 直播的 SegmentBase 封装暂不支持")
	}
	if match.MIME != original.MIME || match.Codecs != original.Codecs || hlsLiveInitializationChanged(hlsWindowInitialization(original.Segments), match.Segments) {
		return nil, i18n.NewError("DASH 直播轨道的初始化信息发生变化")
	}
	return match, nil
}
