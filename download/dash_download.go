package download

import (
	"fmt"
	"net/url"
	"strconv"

	"openDL/i18n"
)

// A single-track DASH presentation can use the existing fragmented-MP4
// transfer and checkpoint machinery. Never silently omit a declared track.
func (m *Manager) resolveDASH(t *Task, original *url.URL) ([]hlsSegment, *url.URL, error) {
	data, base, err := m.readHLSPlaylist(t, original, original, original.String())
	if err != nil {
		return nil, nil, err
	}
	manifest, err := parseDASHManifest(data, base)
	if err != nil {
		return nil, nil, err
	}
	if manifest.HasVideo && manifest.HasAudio {
		return nil, nil, fmt.Errorf(i18n.T("DASH 包含独立音视频轨道，当前尚不支持混流"))
	}
	video, audio, err := m.selectAvailableDASHTracks(t, original, base, manifest)
	if err != nil {
		return nil, nil, err
	}
	if video != nil {
		return video.Segments, base, nil
	}
	return audio.Segments, base, nil
}

// Try another representation when its index cannot be materialized. Selection
// happens before any media file is written, and remains bounded across tracks.
func (m *Manager) selectAvailableDASHTracks(t *Task, original, base *url.URL, manifest *dashManifest) (*dashTrack, *dashTrack, error) {
	if manifest == nil {
		return nil, nil, i18n.NewError("DASH 清单无效或超过大小限制")
	}
	if manifest.PeriodCount > 1 {
		var videoOut, audioOut *dashTrack
		for period := 0; period < manifest.PeriodCount; period++ {
			subset := &dashManifest{PeriodCount: 1, HasAudio: manifest.HasAudio, HasVideo: manifest.HasVideo, AudioError: manifest.AudioError, VideoError: manifest.VideoError}
			for _, track := range manifest.Tracks {
				if track.Period == period {
					track.Period = 0 // subset selection must not recurse by period
					subset.Tracks = append(subset.Tracks, track)
				}
			}
			if len(subset.Tracks) == 0 {
				return nil, nil, fmt.Errorf(i18n.T("DASH 周期 %d 没有支持的轨道"), period+1)
			}
			video, audio, err := m.selectAvailableDASHTracks(t, original, base, subset)
			if err != nil {
				return nil, nil, err
			}
			if video != nil {
				videoOut = mergeDASHPeriodTrack(videoOut, video)
			}
			if audio != nil {
				audioOut = mergeDASHPeriodTrack(audioOut, audio)
			}
		}
		return videoOut, audioOut, nil
	}
	remaining := *manifest
	remaining.Tracks = append([]dashTrack(nil), manifest.Tracks...)
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		select {
		case <-t.cancel:
			return nil, nil, contextCanceled
		default:
		}
		video, audio, err := selectDASHTracksPerPeriod(&remaining, t.mediaQuality, t.audioLanguage)
		if err != nil {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, err
		}
		var failed *dashTrack
		for _, track := range []*dashTrack{video, audio} {
			if track == nil || track.Index == nil {
				continue
			}
			if manifest.Dynamic {
				err = i18n.NewError("DASH 直播的 SegmentBase 封装暂不支持")
			} else {
				err = m.materializeDASHIndex(t, original, base, track)
			}
			if err != nil {
				if track.Kind == "video" {
					lastErr = fmt.Errorf(i18n.T("没有可用的 DASH 视频轨道：%w"), err)
				} else {
					lastErr = fmt.Errorf(i18n.T("没有可用的 DASH 音频轨道：%w"), err)
				}
				failed = track
				break
			}
			track.Index = nil
		}
		if failed == nil {
			return video, audio, nil
		}
		for i := range remaining.Tracks {
			if &remaining.Tracks[i] == failed {
				remaining.Tracks = append(remaining.Tracks[:i], remaining.Tracks[i+1:]...)
				break
			}
		}
	}
	select {
	case <-t.cancel:
		return nil, nil, contextCanceled
	default:
	}
	return nil, nil, lastErr
}

// selectDASHTracksPerPeriod 按周期分别选轨并拼接分片计划。多周期要求每个
// 周期都有所选类型的可用表示；SegmentBase 封装的多周期暂不支持。
func selectDASHTracksPerPeriod(manifest *dashManifest, quality, wantedLanguage string) (*dashTrack, *dashTrack, error) {
	if manifest == nil {
		return nil, nil, i18n.NewError("DASH 清单无效或超过大小限制")
	}
	maxPeriod := manifest.PeriodCount - 1
	if maxPeriod < 0 {
		maxPeriod = 0
	}
	for _, track := range manifest.Tracks {
		if track.Period > maxPeriod {
			maxPeriod = track.Period
		}
	}
	if maxPeriod == 0 {
		return selectDASHTracks(manifest, quality, wantedLanguage)
	}
	var videoOut, audioOut *dashTrack
	for period := 0; period <= maxPeriod; period++ {
		// 每个周期都必须保留清单声明的音视频类型，不能静默遗漏轨道。
		subset := &dashManifest{HasAudio: manifest.HasAudio, HasVideo: manifest.HasVideo, AudioError: manifest.AudioError, VideoError: manifest.VideoError}
		for i := range manifest.Tracks {
			track := manifest.Tracks[i]
			if track.Period != period {
				continue
			}
			subset.Tracks = append(subset.Tracks, track)
			if track.Kind == "audio" {
				subset.HasAudio = true
			} else if track.Kind == "video" {
				subset.HasVideo = true
			}
		}
		if len(subset.Tracks) == 0 {
			return nil, nil, fmt.Errorf(i18n.T("DASH 周期 %d 没有支持的轨道"), period+1)
		}
		video, audio, err := selectDASHTracks(subset, quality, wantedLanguage)
		if err != nil {
			return nil, nil, err
		}
		if video != nil {
			if video.Index != nil {
				return nil, nil, i18n.NewError("DASH 多周期的 SegmentBase 封装暂不支持")
			}
			videoOut = mergeDASHPeriodTrack(videoOut, video)
		}
		if audio != nil {
			if audio.Index != nil {
				return nil, nil, i18n.NewError("DASH 多周期的 SegmentBase 封装暂不支持")
			}
			audioOut = mergeDASHPeriodTrack(audioOut, audio)
		}
	}
	if videoOut == nil && audioOut == nil {
		return nil, nil, i18n.NewError("DASH 清单没有支持的未加密 MP4 轨道")
	}
	return videoOut, audioOut, nil
}

// mergeDASHPeriodTrack 拼接周期分片计划，保留首个周期的表示身份。
func mergeDASHPeriodTrack(merged, next *dashTrack) *dashTrack {
	if merged == nil {
		copy := *next
		copy.Segments = append([]hlsSegment(nil), next.Segments...)
		return &copy
	}
	merged.Segments = append(merged.Segments, next.Segments...)
	return merged
}

// Select independently so separate audio and video can later be downloaded
// and checkpointed without confusing representation identities.
func selectDASHTracks(manifest *dashManifest, quality, wantedLanguage string) (*dashTrack, *dashTrack, error) {
	if manifest == nil {
		return nil, nil, i18n.NewError("DASH 清单无效或超过大小限制")
	}
	var video, audio *dashTrack
	hasVideoCandidate, hasAudioCandidate := false, false
	bestAudioRank := 0
	limit := 0
	if quality != "" {
		var err error
		limit, err = strconv.Atoi(quality)
		if err != nil || limit < 16 || limit > 21600 {
			return nil, nil, fmt.Errorf(i18n.T("画质参数无效"))
		}
	}
	for i := range manifest.Tracks {
		track := &manifest.Tracks[i]
		if track.Kind == "audio" {
			hasAudioCandidate = true
			rank := audioLanguageRank(wantedLanguage, track.Language)
			if rank > 0 && (audio == nil || rank > bestAudioRank || (rank == bestAudioRank && (track.Main && !audio.Main || track.Main == audio.Main && track.Bandwidth > audio.Bandwidth))) {
				audio = track
				bestAudioRank = rank
			}
			continue
		}
		if track.Kind == "video" {
			hasVideoCandidate = true
		}
		if track.Kind != "video" || (limit > 0 && track.Height > limit) {
			continue
		}
		if video == nil || (track.Height > 0 && video.Height == 0) || (track.Height > 0 && video.Height > 0 && track.Height > video.Height) || (track.Height == video.Height && track.Bandwidth > video.Bandwidth) {
			video = track
		}
	}
	if (wantedLanguage != "" && audio == nil) || (manifest.HasVideo && video == nil) || (manifest.HasAudio && audio == nil) || (video == nil && audio == nil) {
		if manifest.HasVideo && video == nil && !hasVideoCandidate && manifest.VideoError != nil {
			return nil, nil, fmt.Errorf(i18n.T("没有可用的 DASH 视频轨道：%w"), manifest.VideoError)
		}
		if manifest.HasAudio && audio == nil && !hasAudioCandidate && manifest.AudioError != nil {
			return nil, nil, fmt.Errorf(i18n.T("没有可用的 DASH 音频轨道：%w"), manifest.AudioError)
		}
		if wantedLanguage != "" && audio == nil {
			return nil, nil, i18n.NewError("清单中没有指定语言的音轨")
		}
		return nil, nil, fmt.Errorf(i18n.T("DASH 清单没有符合画质要求的轨道"))
	}
	return video, audio, nil
}
