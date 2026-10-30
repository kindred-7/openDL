package download

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"openDL/i18n"
)

// selectHLSAudio prefers the advertised default, then an autoselect rendition,
// and otherwise preserves the manifest's order. A missing URI means the audio
// is carried in the selected variant rather than in a separate playlist.
func selectHLSAudio(data []byte, base *url.URL, group, wantedLanguage string) (string, bool, error) {
	choices, err := hlsAudioChoices(data, base, group, wantedLanguage)
	if err != nil {
		return "", false, err
	}
	return choices[0].url, choices[0].url != "", nil
}

type hlsAudioChoice struct {
	url                string
	languageRank, rank int
}

func hlsAudioChoices(data []byte, base *url.URL, group, wantedLanguage string) ([]hlsAudioChoice, error) {
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), maxHLSPlaylist)
	var choices []hlsAudioChoice
	var invalid error
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if err != nil {
			return nil, err
		}
		if attrs["TYPE"] != "AUDIO" || attrs["GROUP-ID"] != group {
			continue
		}
		languageRank := audioLanguageRank(wantedLanguage, attrs["LANGUAGE"])
		if languageRank == 0 {
			continue
		}
		rank := 0
		if attrs["AUTOSELECT"] == "YES" {
			rank = 1
		}
		if attrs["DEFAULT"] == "YES" {
			rank = 2
		}
		address := attrs["URI"]
		if address != "" {
			ref, err := url.Parse(address)
			if err != nil || ref.User != nil {
				invalid = i18n.NewError("HLS 音轨地址无效")
				continue
			}
			u := base.ResolveReference(ref)
			u.Fragment = ""
			if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				invalid = i18n.NewError("HLS 音轨只支持 HTTP 或 HTTPS")
				continue
			}
			address = u.String()
		}
		choices = append(choices, hlsAudioChoice{url: address, languageRank: languageRank, rank: rank})
		if len(choices) > 1000 {
			return nil, i18n.NewError("HLS 音轨数量超过限制")
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(choices) == 0 {
		if invalid != nil {
			return nil, invalid
		}
		if wantedLanguage != "" {
			return nil, i18n.NewError("清单中没有指定语言的音轨")
		}
		return nil, i18n.NewError("HLS 音轨组不存在")
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].languageRank != choices[j].languageRank {
			return choices[i].languageRank > choices[j].languageRank
		}
		return choices[i].rank > choices[j].rank
	})
	return choices, nil
}

func (m *Manager) downloadHLS(t *Task) error {
	// A resumed run must re-fetch AES keys: the same URL may serve a new key.
	t.hlsKeyMu.Lock()
	t.hlsKeys = nil
	t.hlsKeyMu.Unlock()
	original, err := url.Parse(t.sourceURL())
	if err != nil {
		return err
	}
	data, base, err := m.readHLSPlaylist(t, original, original, original.String())
	if err != nil {
		return err
	}
	variants, master, err := parseHLSMasterVariants(data, base, true)
	if err != nil {
		return err
	}
	if !master {
		if t.audioLanguage != "" {
			return i18n.NewError("清单未提供可选择的音轨语言")
		}
		segments, ended, err := parseHLSWindow(data, base)
		if err != nil {
			return err
		}
		t.metaMu.Lock()
		expected := t.expectedSHA256
		t.metaMu.Unlock()
		expected, err = NormalizeSHA256(expected)
		if err != nil {
			return err
		}
		return m.downloadHLSResolved(t, original, base, segments, !ended, expected)
	}
	variants, err = pickHLSVariants(variants, t.mediaQuality)
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		return i18n.NewError("没有可用画质")
	}
	budget := 16
	var lastErr error
	for _, variant := range variants {
		select {
		case <-t.cancel:
			return contextCanceled
		default:
		}
		if budget <= 0 {
			break
		}
		choices := []hlsAudioChoice{{}}
		if variant.audio == "" {
			if t.audioLanguage != "" {
				lastErr = i18n.NewError("清单未提供可选择的音轨语言")
				continue
			}
		} else {
			choices, err = hlsAudioChoices(data, base, variant.audio, t.audioLanguage)
			if err != nil {
				lastErr = err
				continue
			}
		}
		video, videoBase, videoLive, err := m.resolveHLSTS(t, original, base, variant.url, make(map[string]bool), &budget, 0)
		if err != nil {
			lastErr = fmt.Errorf(i18n.T("无法读取 HLS 视频轨道：%w"), err)
			continue
		}
		for _, choice := range choices {
			select {
			case <-t.cancel:
				return contextCanceled
			default:
			}
			if choice.url == "" {
				if t.audioLanguage != "" {
					lastErr = i18n.NewError("暂不支持选择内嵌音轨的语言")
					continue
				}
				t.metaMu.Lock()
				expected := t.expectedSHA256
				t.metaMu.Unlock()
				expected, err = NormalizeSHA256(expected)
				if err != nil {
					return err
				}
				return m.downloadHLSResolved(t, original, videoBase, video, videoLive, expected)
			}
			if budget <= 0 {
				break
			}
			audio, audioBase, audioLive, err := m.resolveHLSTS(t, original, base, choice.url, make(map[string]bool), &budget, 0)
			if err != nil {
				lastErr = fmt.Errorf(i18n.T("无法读取 HLS 音频轨道：%w"), err)
				continue
			}
			if videoLive || audioLive {
				return m.downloadDualMediaLive(t, original, videoBase, audioBase, video, audio)
			}
			err = m.downloadDualMedia(t, original, videoBase, audioBase, video, audio)
			if !errors.Is(err, errMP4InitIncompatible) {
				return err
			}
			periods, alignErr := hlsAlignedPeriods(video, audio)
			if alignErr != nil {
				return alignErr
			}
			if periods > len(t.dashPeriods) {
				return i18n.NewError("初始化段结构不兼容，周期转码最多支持 8 个周期")
			}
			if periods < 2 {
				return err
			}
			return m.downloadDASHSeparatePeriods(t, original, videoBase, audioBase, &dashTrack{Segments: video}, &dashTrack{Segments: audio}, periods)
		}
	}
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	if lastErr == nil {
		lastErr = i18n.NewError("没有可用画质")
	}
	return fmt.Errorf(i18n.T("没有可下载的 HLS 画质：%w"), lastErr)
}

// HLS single-track periods can use the isolated period pipeline when their
// initialization changes. Multi-track inputs are rejected before transcoding.
func (m *Manager) downloadHLSResolved(t *Task, original, base *url.URL, segments []hlsSegment, live bool, expected string) error {
	err := m.downloadResolvedMedia(t, original, base, segments, live, expected)
	if live || !errors.Is(err, errMP4InitIncompatible) || len(segments) == 0 {
		return err
	}
	periods := segments[len(segments)-1].period + 1
	if periods > len(t.dashPeriods) {
		return i18n.NewError("初始化段结构不兼容，周期转码最多支持 8 个周期")
	}
	if periods < 2 {
		return err
	}
	t.metaMu.Lock()
	path := t.filePath
	t.metaMu.Unlock()
	t.checkpointMu.Lock()
	var initEnd int64
	if len(t.hlsCompleted) > 0 {
		initEnd = t.hlsCompleted[0].End
	}
	t.checkpointMu.Unlock()
	if path == "" || initEnd <= 0 {
		return i18n.NewError("HLS 轨道类型无法确定")
	}
	file, openErr := os.Open(extendedPath(path + ".part"))
	if openErr != nil {
		return openErr
	}
	defer file.Close()
	var state hlsMP4State
	if err = state.accept(file, 0, initEnd, true); err != nil {
		return err
	}
	if len(state.tracks) != 1 {
		return i18n.NewError("HLS 周期转码暂不支持包含多条轨道的输入")
	}
	budget := 20000
	boxes, err := mp4Boxes(file, 0, initEnd, &budget)
	if err != nil {
		return err
	}
	kind := ""
	for _, box := range boxes {
		if box.kind != "moov" {
			continue
		}
		if err = mp4Walk(file, box, &budget, 0, func(name string, data []byte) {
			if name == "hdlr" && len(data) >= 12 {
				kind = string(data[8:12])
			}
		}); err != nil {
			return err
		}
	}
	// Windows cannot replace the partial output while this handle is open.
	if err := file.Close(); err != nil {
		return err
	}
	track := &dashTrack{Segments: segments}
	switch kind {
	case "vide":
		return m.downloadDASHSeparatePeriods(t, original, base, nil, track, nil, periods)
	case "soun":
		return m.downloadDASHSeparatePeriods(t, original, nil, base, nil, track, periods)
	default:
		return i18n.NewError("HLS 轨道类型无法确定")
	}
}
