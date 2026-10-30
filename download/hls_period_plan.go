package download

import "openDL/i18n"

// hlsAlignedPeriods pairs discontinuity groups, not media sequence numbers.
// Matching groups is necessary but does not itself establish a timestamp
// anchor; the caller must preserve the tracks' relative presentation times.
func hlsAlignedPeriods(video, audio []hlsSegment) (int, error) {
	sequences := func(segments []hlsSegment) ([]uint64, error) {
		if len(segments) == 0 {
			return nil, i18n.NewError("HLS 独立轨道缺少周期")
		}
		var result []uint64
		media := false
		for _, segment := range segments {
			if !segment.mp4 || segment.period < 0 || segment.period >= maxMediaPeriods {
				return nil, i18n.NewError("HLS 独立轨道周期无效")
			}
			if segment.period == len(result) {
				if !segment.init || (len(result) > 0 && (!media || segment.hlsDiscontinuity <= result[len(result)-1])) {
					return nil, i18n.NewError("HLS 独立轨道周期无法按不连续序号对齐")
				}
				result = append(result, segment.hlsDiscontinuity)
				media = false
			} else if segment.period != len(result)-1 || segment.hlsDiscontinuity != result[len(result)-1] || segment.init {
				return nil, i18n.NewError("HLS 独立轨道周期无法按不连续序号对齐")
			} else {
				media = true
			}
		}
		if !media {
			return nil, i18n.NewError("HLS 独立轨道周期缺少媒体分片")
		}
		return result, nil
	}
	v, err := sequences(video)
	if err != nil {
		return 0, err
	}
	a, err := sequences(audio)
	if err != nil {
		return 0, err
	}
	if len(v) != len(a) {
		return 0, i18n.NewError("HLS 音视频的不连续周期数量不一致")
	}
	for i := range v {
		if v[i] != a[i] {
			return 0, i18n.NewError("HLS 音视频的不连续序号不一致")
		}
	}
	return len(v), nil
}
