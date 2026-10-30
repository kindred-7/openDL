package download

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"openDL/i18n"
)

// dashManifest is an internal transfer plan. URLs and credentials are kept in
// memory only; a future checkpoint must store fingerprints rather than URLs.
type dashManifest struct {
	Tracks                 []dashTrack
	HasAudio, HasVideo     bool
	Dynamic                bool // type="dynamic"：直播清单，跟随滚动窗口录制
	PeriodCount            int
	AudioError, VideoError error // first unsupported representation per type
}

type dashTrack struct {
	ID, Kind, MIME, Codecs, Language string
	Period                           int // 多周期清单的周期序号，从 0 开始
	Bandwidth                        int64
	Height                           int
	Main                             bool
	Segments                         []hlsSegment
	Index                            *dashIndexPlan
}

type dashXML struct {
	XMLName  xml.Name     `xml:"MPD"`
	Type     string       `xml:"type,attr"`
	Duration string       `xml:"mediaPresentationDuration,attr"`
	Base     []string     `xml:"BaseURL"`
	Periods  []dashPeriod `xml:"Period"`
}

type dashPeriod struct {
	Duration string           `xml:"duration,attr"`
	Start    string           `xml:"start,attr"`
	Base     []string         `xml:"BaseURL"`
	Template *dashTemplate    `xml:"SegmentTemplate"`
	List     *dashSegmentList `xml:"SegmentList"`
	Index    *dashSegmentBase `xml:"SegmentBase"`
	Sets     []dashSet        `xml:"AdaptationSet"`
}

type dashSet struct {
	Kind            string               `xml:"contentType,attr"`
	MIME            string               `xml:"mimeType,attr"`
	Codecs          string               `xml:"codecs,attr"`
	Language        string               `xml:"lang,attr"`
	Height          int                  `xml:"height,attr"`
	Base            []string             `xml:"BaseURL"`
	Template        *dashTemplate        `xml:"SegmentTemplate"`
	List            *dashSegmentList     `xml:"SegmentList"`
	Index           *dashSegmentBase     `xml:"SegmentBase"`
	Protection      []struct{}           `xml:"ContentProtection"`
	Roles           []dashRole           `xml:"Role"`
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRole struct {
	Scheme string `xml:"schemeIdUri,attr"`
	Value  string `xml:"value,attr"`
}

func dashMainRole(roles []dashRole) bool {
	found := false
	for _, role := range roles {
		if role.Scheme == "urn:mpeg:dash:role:2011" {
			found = true
			if role.Value == "main" {
				return true
			}
		}
	}
	return !found
}

type dashRepresentation struct {
	ID         string           `xml:"id,attr"`
	MIME       string           `xml:"mimeType,attr"`
	Codecs     string           `xml:"codecs,attr"`
	Bandwidth  int64            `xml:"bandwidth,attr"`
	Height     int              `xml:"height,attr"`
	Base       []string         `xml:"BaseURL"`
	Template   *dashTemplate    `xml:"SegmentTemplate"`
	List       *dashSegmentList `xml:"SegmentList"`
	Index      *dashSegmentBase `xml:"SegmentBase"`
	Protection []struct{}       `xml:"ContentProtection"`
}

type dashTemplate struct {
	Media          *string       `xml:"media,attr"`
	Initialization *string       `xml:"initialization,attr"`
	Timescale      *uint64       `xml:"timescale,attr"`
	Duration       *uint64       `xml:"duration,attr"`
	StartNumber    *uint64       `xml:"startNumber,attr"`
	Offset         *uint64       `xml:"presentationTimeOffset,attr"`
	Timeline       *dashTimeline `xml:"SegmentTimeline"`
}

type dashTimeline struct {
	Entries []dashTimelineEntry `xml:"S"`
}

type dashSegmentList struct {
	Href           string              `xml:"href,attr"`
	Timescale      *uint64             `xml:"timescale,attr"`
	Offset         *uint64             `xml:"presentationTimeOffset,attr"`
	Initialization *dashInitialization `xml:"Initialization"`
	Segments       []dashSegmentURL    `xml:"SegmentURL"`
}
type dashSegmentBase struct {
	IndexRange     string              `xml:"indexRange,attr"`
	Timescale      *uint64             `xml:"timescale,attr"`
	Offset         *uint64             `xml:"presentationTimeOffset,attr"`
	Initialization *dashInitialization `xml:"Initialization"`
}
type dashIndexPlan struct {
	Resource       string
	Start, Length  int64
	Initialization hlsSegment
	Period         int
	Timescale      uint64
	Offset         uint64
	Duration       time.Duration
}

func mergeDASHBase(parent, child *dashSegmentBase) *dashSegmentBase {
	if parent == nil && child == nil {
		return nil
	}
	var result dashSegmentBase
	if parent != nil {
		result = *parent
	}
	if child != nil {
		if child.IndexRange != "" {
			result.IndexRange = child.IndexRange
		}
		if child.Timescale != nil {
			result.Timescale = child.Timescale
		}
		if child.Offset != nil {
			result.Offset = child.Offset
		}
		if child.Initialization != nil {
			result.Initialization = child.Initialization
		}
	}
	return &result
}

func dashByteRange(value string) (int64, int64, error) {
	first, last, ok := strings.Cut(value, "-")
	start, e1 := strconv.ParseInt(first, 10, 64)
	end, e2 := strconv.ParseInt(last, 10, 64)
	if !ok || e1 != nil || e2 != nil || start < 0 || end < start || end-start == math.MaxInt64 {
		return 0, 0, i18n.NewError("DASH 分片字节范围无效")
	}
	return start, end - start + 1, nil
}

func dashIndexedPlan(base *url.URL, data *dashSegmentBase) (*dashIndexPlan, error) {
	if data == nil || data.IndexRange == "" || data.Initialization == nil {
		return nil, i18n.NewError("DASH 索引或初始化段范围缺失")
	}
	start, length, err := dashByteRange(data.IndexRange)
	if err != nil {
		return nil, err
	}
	if length > 4<<20 {
		return nil, i18n.NewError("DASH 索引超过大小限制")
	}
	initURL, err := dashBase(base, []string{data.Initialization.Source})
	if err != nil {
		return nil, err
	}
	var initStart, initLength int64
	if data.Initialization.Range != "" {
		initStart, initLength, err = dashByteRange(data.Initialization.Range)
		if err != nil {
			return nil, err
		}
		if initLength > maxHLSInitialization {
			return nil, i18n.NewError("HLS 初始化段超过 16 MiB")
		}
	} else if data.Initialization.Source == "" || initURL.String() == base.String() {
		// Without a range the entire resource is the initialization segment.
		// It cannot also be the indexed media file.
		return nil, i18n.NewError("DASH 索引或初始化段范围缺失")
	}
	return &dashIndexPlan{Resource: base.String(), Start: start, Length: length, Initialization: hlsSegment{url: initURL.String(), start: initStart, length: initLength, mp4: true, init: true}}, nil
}

type dashInitialization struct {
	Source string `xml:"sourceURL,attr"`
	Range  string `xml:"range,attr"`
}
type dashSegmentURL struct {
	Media      string `xml:"media,attr"`
	Range      string `xml:"mediaRange,attr"`
	Index      string `xml:"index,attr"`
	IndexRange string `xml:"indexRange,attr"`
}

func mergeDASHList(parent, child *dashSegmentList) *dashSegmentList {
	if parent == nil && child == nil {
		return nil
	}
	var result dashSegmentList
	if parent != nil {
		result = *parent
	}
	if child != nil {
		if child.Href != "" {
			result.Href = child.Href
		}
		if child.Timescale != nil {
			result.Timescale = child.Timescale
		}
		if child.Offset != nil {
			result.Offset = child.Offset
		}
		if child.Initialization != nil {
			result.Initialization = child.Initialization
		}
		if len(child.Segments) > 0 {
			result.Segments = child.Segments
		}
	}
	return &result
}

func dashListSegments(base *url.URL, list *dashSegmentList) ([]hlsSegment, error) {
	if list == nil || list.Href != "" || list.Initialization == nil || len(list.Segments) == 0 {
		return nil, i18n.NewError("DASH 分片列表无效或不支持外部引用")
	}
	if len(list.Segments) >= maxHLSSegments {
		return nil, i18n.NewError("DASH 分片数量超过限制")
	}
	build := func(address, rawRange string, init bool) (hlsSegment, error) {
		if address == "" && rawRange == "" {
			return hlsSegment{}, i18n.NewError("DASH 分片列表缺少地址或字节范围")
		}
		u, err := dashBase(base, []string{address})
		if err != nil {
			return hlsSegment{}, err
		}
		result := hlsSegment{url: u.String(), mp4: true, init: init}
		if rawRange != "" {
			result.start, result.length, err = dashByteRange(rawRange)
			if err != nil {
				return hlsSegment{}, err
			}
		}
		return result, nil
	}
	first, err := build(list.Initialization.Source, list.Initialization.Range, true)
	if err != nil {
		return nil, err
	}
	segments := []hlsSegment{first}
	for _, entry := range list.Segments {
		// Index information is useful for seeking within a segment. Full
		// sequential downloads can use the explicitly addressed media instead.
		segment, err := build(entry.Media, entry.Range, false)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

type dashTimelineEntry struct {
	Time     *uint64 `xml:"t,attr"`
	Duration uint64  `xml:"d,attr"`
	Repeat   int64   `xml:"r,attr"`
}

func mergeDashTemplate(parent, child *dashTemplate) *dashTemplate {
	if parent == nil && child == nil {
		return nil
	}
	var result dashTemplate
	if parent != nil {
		result = *parent
	}
	if child != nil {
		if child.Media != nil {
			result.Media = child.Media
		}
		if child.Initialization != nil {
			result.Initialization = child.Initialization
		}
		if child.Timescale != nil {
			result.Timescale = child.Timescale
		}
		if child.Duration != nil {
			result.Duration = child.Duration
		}
		if child.StartNumber != nil {
			result.StartNumber = child.StartNumber
		}
		if child.Offset != nil {
			result.Offset = child.Offset
		}
		if child.Timeline != nil {
			result.Timeline = child.Timeline
		}
	}
	return &result
}

var dashDurationPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func dashDuration(value string) (time.Duration, error) {
	return parseDASHDuration(value, false)
}

func parseDASHDuration(value string, allowZero bool) (time.Duration, error) {
	parts := dashDurationPattern.FindStringSubmatch(value)
	if parts == nil || (parts[1] == "" && parts[2] == "" && parts[3] == "" && parts[4] == "") {
		return 0, fmt.Errorf(i18n.T("DASH 时长格式无效"))
	}
	seconds := float64(0)
	for i, scale := range []float64{86400, 3600, 60, 1} {
		if parts[i+1] != "" {
			n, err := strconv.ParseFloat(parts[i+1], 64)
			if err != nil {
				return 0, fmt.Errorf(i18n.T("DASH 时长格式无效"))
			}
			seconds += n * scale
		}
	}
	if seconds < 0 || (!allowZero && seconds == 0) || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf(i18n.T("DASH 时长超出限制"))
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 && (!allowZero || seconds != 0) {
		return 0, fmt.Errorf(i18n.T("DASH 时长超出限制"))
	}
	return duration, nil
}

func dashBase(parent *url.URL, values []string) (*url.URL, error) {
	if len(values) == 0 {
		return parent, nil
	}
	// Multiple CDN alternatives require retry and checkpoint identity handling.
	// Use the first advertised alternative consistently for this plan.
	ref, err := url.Parse(strings.TrimSpace(values[0]))
	if err != nil || ref.User != nil {
		return nil, fmt.Errorf(i18n.T("DASH 资源地址无效"))
	}
	u := parent.ResolveReference(ref)
	u.Fragment = ""
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf(i18n.T("DASH 资源只支持 HTTP 或 HTTPS"))
	}
	return u, nil
}

// dashExpand expands the DASH template tokens and rejects unsupported tokens
// instead of accidentally downloading a literal or incorrect resource.
func dashExpand(template, id string, bandwidth int64, number, sampleTime uint64) (string, error) {
	var result strings.Builder
	for i := 0; i < len(template); {
		if template[i] != '$' {
			result.WriteByte(template[i])
			i++
			continue
		}
		if i+1 < len(template) && template[i+1] == '$' {
			result.WriteByte('$')
			i += 2
			continue
		}
		end := strings.IndexByte(template[i+1:], '$')
		if end < 0 {
			return "", fmt.Errorf(i18n.T("DASH 地址模板无效"))
		}
		end += i + 1
		token := template[i+1 : end]
		name, format, hasFormat := strings.Cut(token, "%")
		width := 0
		if hasFormat {
			if len(format) < 3 || format[0] != '0' || format[len(format)-1] != 'd' {
				return "", fmt.Errorf(i18n.T("DASH 地址模板格式不支持"))
			}
			var err error
			width, err = strconv.Atoi(format[1 : len(format)-1])
			if err != nil || width < 1 || width > 20 {
				return "", fmt.Errorf(i18n.T("DASH 地址模板格式不支持"))
			}
		}
		var value string
		switch name {
		case "RepresentationID":
			if hasFormat || id == "" || strings.ContainsAny(id, "/?#\\") {
				return "", fmt.Errorf(i18n.T("DASH 表示标识无效"))
			}
			value = id
		case "Bandwidth":
			if bandwidth <= 0 {
				return "", fmt.Errorf(i18n.T("DASH 码率无效"))
			}
			value = strconv.FormatInt(bandwidth, 10)
		case "Number":
			value = strconv.FormatUint(number, 10)
		case "Time":
			value = strconv.FormatUint(sampleTime, 10)
		default:
			return "", fmt.Errorf(i18n.T("DASH 地址模板包含不支持的变量"))
		}
		if width > len(value) {
			result.WriteString(strings.Repeat("0", width-len(value)))
		}
		result.WriteString(value)
		i = end + 1
		if result.Len() > 8192 {
			return "", fmt.Errorf(i18n.T("DASH 资源地址过长"))
		}
	}
	return result.String(), nil
}

func dashSegments(base *url.URL, rep dashRepresentation, template *dashTemplate, duration time.Duration) ([]hlsSegment, error) {
	if template == nil || template.Media == nil || template.Initialization == nil || *template.Media == "" || *template.Initialization == "" {
		return nil, fmt.Errorf(i18n.T("DASH 缺少分片模板或初始化段"))
	}
	timescale, number, offset := uint64(1), uint64(1), uint64(0)
	if template.Timescale != nil {
		timescale = *template.Timescale
	}
	if template.StartNumber != nil {
		number = *template.StartNumber
	}
	if template.Offset != nil {
		offset = *template.Offset
	}
	if timescale == 0 {
		return nil, fmt.Errorf(i18n.T("DASH 时间刻度无效"))
	}
	var periodEnd uint64
	if duration > 0 {
		ticksFloat := math.Ceil(float64(duration) / float64(time.Second) * float64(timescale))
		if ticksFloat < 1 || ticksFloat >= float64(math.MaxUint64) {
			return nil, fmt.Errorf(i18n.T("DASH 时间范围超出限制"))
		}
		ticks := uint64(ticksFloat)
		if offset > math.MaxUint64-ticks {
			return nil, fmt.Errorf(i18n.T("DASH 时间范围超出限制"))
		}
		periodEnd = offset + ticks
	}
	segments := make([]hlsSegment, 0, 32)
	mediaURLs := make(map[string]bool)
	add := func(pattern string, number, sampleTime uint64, init bool) error {
		if len(segments) >= maxHLSSegments {
			return fmt.Errorf(i18n.T("DASH 分片数量超过限制"))
		}
		value, err := dashExpand(pattern, rep.ID, rep.Bandwidth, number, sampleTime)
		if err != nil {
			return err
		}
		u, err := dashBase(base, []string{value})
		if err != nil {
			return err
		}
		address := u.String()
		if !init {
			if mediaURLs[address] {
				return i18n.NewError("DASH 分片模板生成了重复地址")
			}
			mediaURLs[address] = true
		}
		segments = append(segments, hlsSegment{url: address, mp4: true, init: init})
		return nil
	}
	if err := add(*template.Initialization, number, 0, true); err != nil {
		return nil, err
	}
	var position uint64
	if template.Timeline != nil {
		if len(template.Timeline.Entries) == 0 {
			return nil, fmt.Errorf(i18n.T("DASH 分片时间线为空"))
		}
		for i, entry := range template.Timeline.Entries {
			if entry.Duration == 0 || entry.Repeat < -1 {
				return nil, fmt.Errorf(i18n.T("DASH 分片时间线无效"))
			}
			if entry.Time != nil {
				position = *entry.Time
			}
			count := uint64(0)
			if entry.Repeat >= 0 {
				count = uint64(entry.Repeat) + 1
			} else {
				limit := periodEnd
				if i+1 < len(template.Timeline.Entries) {
					if template.Timeline.Entries[i+1].Time == nil {
						return nil, fmt.Errorf(i18n.T("DASH 重复时间线缺少结束时间"))
					}
					limit = *template.Timeline.Entries[i+1].Time
				} else if duration <= 0 {
					return nil, i18n.NewError("DASH 重复时间线缺少结束时间")
				}
				if limit <= position {
					return nil, fmt.Errorf(i18n.T("DASH 分片时间线无效"))
				}
				count = (limit-position-1)/entry.Duration + 1
			}
			if count > uint64(maxHLSSegments-len(segments)) {
				return nil, fmt.Errorf(i18n.T("DASH 分片数量超过限制"))
			}
			for j := uint64(0); j < count; j++ {
				if err := add(*template.Media, number, position, false); err != nil {
					return nil, err
				}
				if number == math.MaxUint64 || position > math.MaxUint64-entry.Duration {
					return nil, fmt.Errorf(i18n.T("DASH 时间范围超出限制"))
				}
				number++
				position += entry.Duration
			}
		}
	} else {
		if template.Duration == nil || *template.Duration == 0 || duration <= 0 {
			return nil, fmt.Errorf(i18n.T("DASH 分片时长无效"))
		}
		count := (periodEnd-offset-1)/(*template.Duration) + 1
		if count > uint64(maxHLSSegments-len(segments)) {
			return nil, fmt.Errorf(i18n.T("DASH 分片数量超过限制"))
		}
		for i := uint64(0); i < count; i++ {
			if err := add(*template.Media, number, position, false); err != nil {
				return nil, err
			}
			if number == math.MaxUint64 || position > math.MaxUint64-*template.Duration {
				return nil, fmt.Errorf(i18n.T("DASH 时间范围超出限制"))
			}
			number++
			position += *template.Duration
		}
	}
	if len(segments) < 2 {
		return nil, fmt.Errorf(i18n.T("DASH 没有媒体分片"))
	}
	return segments, nil
}

// parseDASHManifest currently plans single-period static fragmented MP4
// presentations using SegmentTemplate. Unsupported packaging fails explicitly.
func parseDASHManifest(data []byte, base *url.URL) (*dashManifest, error) {
	if len(data) > maxHLSPlaylist || base == nil {
		return nil, fmt.Errorf(i18n.T("DASH 清单无效或超过大小限制"))
	}
	var manifest dashXML
	dec := xml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf(i18n.T("DASH 清单格式无效：%w"), err)
	}
	dynamic := manifest.Type == "dynamic"
	if manifest.Type != "" && manifest.Type != "static" && !dynamic {
		return nil, fmt.Errorf(i18n.T("不支持的 DASH 清单类型 %q"), manifest.Type)
	}
	if len(manifest.Periods) == 0 || len(manifest.Periods) > maxMediaPeriods {
		return nil, fmt.Errorf(i18n.T("DASH 周期数量不在支持范围"))
	}
	if dynamic && len(manifest.Periods) > 1 {
		return nil, i18n.NewError("DASH 直播的多周期清单暂不支持")
	}
	presentation, err := dashDuration(manifest.Duration)
	if err != nil {
		return nil, err
	}
	starts := make([]time.Duration, len(manifest.Periods))
	startKnown := make([]bool, len(manifest.Periods))
	periodDurations := make([]time.Duration, len(manifest.Periods))
	for i, period := range manifest.Periods {
		if period.Start != "" {
			if starts[i], err = parseDASHDuration(period.Start, true); err != nil {
				return nil, err
			}
			startKnown[i] = true
		}
		if period.Duration != "" {
			if periodDurations[i], err = dashDuration(period.Duration); err != nil {
				return nil, err
			}
		}
	}
	if !startKnown[0] {
		startKnown[0] = true
	}
	for i := 1; i < len(starts); i++ {
		if !startKnown[i] {
			if !startKnown[i-1] || periodDurations[i-1] <= 0 || starts[i-1] > time.Duration(math.MaxInt64)-periodDurations[i-1] {
				return nil, i18n.NewError("DASH 周期起始时间无法确定")
			}
			starts[i] = starts[i-1] + periodDurations[i-1]
			startKnown[i] = true
		}
		if starts[i] <= starts[i-1] {
			return nil, i18n.NewError("DASH 周期起始时间无效")
		}
	}
	for i := range starts {
		if i+1 < len(starts) {
			// The next period's start defines the actual transition boundary.
			periodDurations[i] = starts[i+1] - starts[i]
		} else if presentation > 0 {
			if starts[i] >= presentation {
				return nil, i18n.NewError("DASH 周期起始时间无效")
			}
			remaining := presentation - starts[i]
			if periodDurations[i] == 0 || periodDurations[i] > remaining {
				periodDurations[i] = remaining
			}
		}
	}
	base, err = dashBase(base, manifest.Base)
	if err != nil {
		return nil, err
	}
	plan := &dashManifest{Dynamic: dynamic, PeriodCount: len(manifest.Periods)}
	totalSegments := 0
	representationCount := 0
	recordError := func(kind string, err error) {
		if kind == "audio" && plan.AudioError == nil {
			plan.AudioError = err
		}
		if kind == "video" && plan.VideoError == nil {
			plan.VideoError = err
		}
	}
	for periodIndex, period := range manifest.Periods {
		periodBase, periodErr := dashBase(base, period.Base)
		if periodErr != nil {
			return nil, periodErr
		}
		duration := periodDurations[periodIndex]
		for _, set := range period.Sets {
			representationCount += len(set.Representations)
			if representationCount > 1000 {
				return nil, i18n.NewError("DASH 轨道数量超过限制")
			}
			declaredKind := set.Kind
			if declaredKind == "" {
				declaredKind, _, _ = strings.Cut(set.MIME, "/")
			}
			if declaredKind == "audio" {
				plan.HasAudio = true
			}
			if declaredKind == "video" {
				plan.HasVideo = true
			}
			for _, rep := range set.Representations {
				kind, _, _ := strings.Cut(rep.MIME, "/")
				if kind == "audio" {
					plan.HasAudio = true
				}
				if kind == "video" {
					plan.HasVideo = true
				}
			}
			if len(set.Protection) != 0 {
				recordError(declaredKind, i18n.NewError("DASH 轨道使用了不支持的内容保护"))
				continue
			}
			setBase, err := dashBase(periodBase, set.Base)
			if err != nil {
				recordError(declaredKind, err)
				for _, rep := range set.Representations {
					kind, _, _ := strings.Cut(rep.MIME, "/")
					recordError(kind, err)
				}
				continue
			}
			for _, rep := range set.Representations {
				mime, codecs := rep.MIME, rep.Codecs
				height := rep.Height
				if height == 0 {
					height = set.Height
				}
				if mime == "" {
					mime = set.MIME
				}
				if codecs == "" {
					codecs = set.Codecs
				}
				kind := set.Kind
				if kind == "" {
					kind, _, _ = strings.Cut(mime, "/")
				}
				if len(rep.Protection) != 0 {
					recordError(kind, i18n.NewError("DASH 轨道使用了不支持的内容保护"))
					continue
				}
				if (kind != "audio" && kind != "video") || (mime != "audio/mp4" && mime != "video/mp4") {
					continue
				}
				repBase, err := dashBase(setBase, rep.Base)
				if err != nil {
					recordError(kind, err)
					continue
				}
				template := mergeDashTemplate(mergeDashTemplate(period.Template, set.Template), rep.Template)
				list := mergeDASHList(mergeDASHList(period.List, set.List), rep.List)
				index := mergeDASHBase(mergeDASHBase(period.Index, set.Index), rep.Index)
				addressing := "template"
				switch {
				case dashAddressConflict(rep.Template, rep.List, rep.Index), dashAddressConflict(set.Template, set.List, set.Index), dashAddressConflict(period.Template, period.List, period.Index):
					recordError(kind, i18n.NewError("DASH 分片地址方式冲突"))
					continue
				case rep.List != nil:
					addressing = "list"
				case rep.Index != nil:
					addressing = "index"
				case rep.Template != nil:
					addressing = "template"
				case set.List != nil:
					addressing = "list"
				case set.Index != nil:
					addressing = "index"
				case set.Template != nil:
					addressing = "template"
				case period.List != nil:
					addressing = "list"
				case period.Index != nil:
					addressing = "index"
				}
				var segments []hlsSegment
				var indexed *dashIndexPlan
				if addressing == "index" {
					indexed, err = dashIndexedPlan(repBase, index)
				} else if addressing == "list" {
					segments, err = dashListSegments(repBase, list)
				} else {
					segments, err = dashSegments(repBase, rep, template, duration)
				}
				if err != nil {
					recordError(kind, err)
					continue
				}
				totalSegments += len(segments)
				if totalSegments > 100000 {
					return nil, fmt.Errorf(i18n.T("DASH 全部轨道分片数量超过限制"))
				}
				var scale, offset uint64 = 1, 0
				if addressing == "template" && template != nil {
					if template.Timescale != nil {
						scale = *template.Timescale
					}
					if template.Offset != nil {
						offset = *template.Offset
					}
				} else if addressing == "list" && list != nil {
					if list.Timescale != nil {
						scale = *list.Timescale
					}
					if list.Offset != nil {
						offset = *list.Offset
					}
				} else if addressing == "index" && index != nil {
					if index.Timescale != nil {
						scale = *index.Timescale
					}
					if index.Offset != nil {
						offset = *index.Offset
					}
				}
				if scale == 0 {
					recordError(kind, i18n.NewError("DASH 时间刻度无效"))
					continue
				}
				if indexed != nil {
					indexed.Period, indexed.Timescale, indexed.Offset, indexed.Duration = periodIndex, scale, offset, duration
				}
				for i := range segments {
					segments[i].period = periodIndex
					segments[i].dashTimescale, segments[i].dashOffset = scale, offset
					segments[i].dashPeriodDuration = duration
				}
				plan.Tracks = append(plan.Tracks, dashTrack{ID: rep.ID, Kind: kind, MIME: mime, Codecs: codecs, Language: set.Language, Period: periodIndex, Bandwidth: rep.Bandwidth, Height: height, Main: dashMainRole(set.Roles), Segments: segments, Index: indexed})
				if len(plan.Tracks) > 1000 {
					return nil, fmt.Errorf(i18n.T("DASH 轨道数量超过限制"))
				}
			}
		}
	}
	if len(plan.Tracks) == 0 {
		if plan.VideoError != nil {
			return nil, fmt.Errorf(i18n.T("没有可用的 DASH 视频轨道：%w"), plan.VideoError)
		}
		if plan.AudioError != nil {
			return nil, fmt.Errorf(i18n.T("没有可用的 DASH 音频轨道：%w"), plan.AudioError)
		}
		return nil, fmt.Errorf(i18n.T("DASH 清单没有支持的未加密 MP4 轨道"))
	}
	return plan, nil
}

func dashAddressConflict(template *dashTemplate, list *dashSegmentList, index *dashSegmentBase) bool {
	count := 0
	if template != nil {
		count++
	}
	if list != nil {
		count++
	}
	if index != nil {
		count++
	}
	return count > 1
}
