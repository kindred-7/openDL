package download

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	"openDL/i18n"
)

const maxHLSInitialization = 16 << 20

type mp4TrackDefaults struct {
	size     uint32
	duration uint32
}

func parseHLSMap(value string, base *url.URL) (hlsSegment, error) {
	attrs, err := hlsAttributes(value)
	if err != nil {
		return hlsSegment{}, err
	}
	ref, err := url.Parse(attrs["URI"])
	if err != nil || attrs["URI"] == "" {
		return hlsSegment{}, fmt.Errorf(i18n.T("HLS 初始化段地址无效"))
	}
	resolved := base.ResolveReference(ref)
	resolved.Fragment = ""
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" {
		return hlsSegment{}, fmt.Errorf(i18n.T("HLS 初始化段只支持 HTTP 或 HTTPS"))
	}
	segment := hlsSegment{url: resolved.String(), mp4: true, init: true}
	for key := range attrs {
		if key != "URI" && key != "BYTERANGE" {
			return hlsSegment{}, fmt.Errorf(i18n.T("暂不支持 HLS 初始化段属性 %s"), key)
		}
	}
	if text, ok := attrs["BYTERANGE"]; ok {
		length, offset, explicit := strings.Cut(text, "@")
		segment.length, err = strconv.ParseInt(length, 10, 64)
		if err != nil || segment.length <= 0 || !explicit {
			return hlsSegment{}, fmt.Errorf(i18n.T("HLS 初始化段范围需要有效长度和明确起点"))
		}
		segment.start, err = strconv.ParseInt(offset, 10, 64)
		if err != nil || segment.start < 0 || segment.start > math.MaxInt64-(segment.length-1) {
			return hlsSegment{}, fmt.Errorf(i18n.T("HLS 初始化段范围超出限制"))
		}
	}
	return segment, nil
}

// Only fragment-relative, self-contained media can be concatenated without
// rewriting offsets. Validate metadata with bounded reads; never load mdat.
// This is a container check, not a decoder or a codec compatibility guarantee.
// References: RFC 8216 §3.3 and W3C ISO BMFF Byte Stream Format §3–4.
type hlsMP4State struct {
	tracks        map[uint32]mp4TrackDefaults // track ID -> trex defaults
	initDigest    string                      // 首个初始化段的摘要
	initStructure string                      // 首个初始化段的结构指纹；字节不同时的备用比对
	period        int                         // 已写入分片的周期序号
	pendingShift  bool                        // 周期切换后的首片需要整体平移
	shiftActive   bool
	shift         uint64
	lastEnd       uint64 // 已接受分片的解码结束时间
}

// shiftTfdt 在多周期缝合时平移分片的解码起始时间：周期边界把首片对齐到已
// 写入内容的结束时间，同周期后续分片沿用同一偏移。
func (s *hlsMP4State) shiftTfdt(rw *os.File, start, end int64) error {
	if len(s.tracks) != 1 {
		return i18n.NewError("暂不支持多轨 MP4 的周期时间轴拼接")
	}
	budget := 1000
	boxes, err := mp4Boxes(rw, start, end, &budget)
	if err != nil {
		return err
	}
	for _, box := range boxes {
		if box.kind != "moof" {
			continue
		}
		children, err := mp4Boxes(rw, box.body, box.end, &budget)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.kind != "traf" {
				continue
			}
			entries, err := mp4Boxes(rw, child.body, child.end, &budget)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.kind != "tfdt" {
					continue
				}
				data, err := mp4Read(rw, entry, 0, entry.end-entry.body)
				if err != nil {
					return err
				}
				if (len(data) != 8 && len(data) != 12) || data[0] > 1 || len(data) != 8+int(data[0])*4 {
					return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
				}
				var value uint64
				if data[0] == 0 {
					value = uint64(binary.BigEndian.Uint32(data[4:8]))
				} else {
					value = binary.BigEndian.Uint64(data[4:12])
				}
				if s.pendingShift {
					if value >= s.lastEnd {
						// 周期起点晚于拼接点：保留向前的时间跳变。
						s.shift = 0
					} else {
						s.shift = s.lastEnd - value
					}
					s.pendingShift = false
					s.shiftActive = true
				}
				if s.shiftActive {
					if value > ^uint64(0)-s.shift {
						return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
					}
					value += s.shift
					if data[0] == 0 {
						if value > math.MaxUint32 {
							return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
						}
						binary.BigEndian.PutUint32(data[4:8], uint32(value))
					} else {
						binary.BigEndian.PutUint64(data[4:12], value)
					}
					if _, err := rw.WriteAt(data, entry.body); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// advancePeriod 报告分片进入新周期并登记缝合；多周期的时间轴需要在写入后
// 平移到上一周期的结束时间。
func (s *hlsMP4State) advancePeriod(period int) {
	if period > s.period {
		s.period = period
		s.pendingShift = true
	}
}

type mp4Box struct {
	kind             string
	start, body, end int64
}

func mp4Boxes(r io.ReaderAt, start, end int64, budget *int) ([]mp4Box, error) {
	var boxes []mp4Box
	for start < end {
		if end-start < 8 || *budget <= 0 {
			return nil, fmt.Errorf(i18n.T("MP4 容器边界或层级数量无效"))
		}
		*budget--
		var header [16]byte
		if _, err := r.ReadAt(header[:8], start); err != nil {
			return nil, err
		}
		size, head := uint64(binary.BigEndian.Uint32(header[:4])), int64(8)
		kind := string(header[4:8])
		if size == 1 {
			if end-start < 16 {
				return nil, fmt.Errorf(i18n.T("MP4 扩展长度不完整"))
			}
			if _, err := r.ReadAt(header[8:], start+8); err != nil {
				return nil, err
			}
			size, head = binary.BigEndian.Uint64(header[8:]), 16
		}
		// A zero-sized box extends to EOF. Appending another segment would
		// change that boundary, so such fragments cannot be copied verbatim.
		if size < uint64(head) || size > uint64(end-start) {
			return nil, fmt.Errorf(i18n.T("MP4 数据块 %q 的长度无效"), kind)
		}
		boxes = append(boxes, mp4Box{kind: kind, start: start, body: start + head, end: start + int64(size)})
		start += int64(size)
	}
	return boxes, nil
}

func mp4Read(r io.ReaderAt, box mp4Box, offset, size int64) ([]byte, error) {
	if offset < 0 || size < 0 || size > 16<<20 || offset > box.end-box.body || size > box.end-box.body-offset {
		return nil, fmt.Errorf(i18n.T("MP4 数据块 %q 不完整或过大"), box.kind)
	}
	data := make([]byte, int(size))
	_, err := r.ReadAt(data, box.body+offset)
	return data, err
}

func (s *hlsMP4State) accept(r io.ReaderAt, start, end int64, init bool) error {
	budget := 100000
	boxes, err := mp4Boxes(r, start, end, &budget)
	if err != nil {
		return err
	}
	if init {
		if start != 0 || end-start > maxHLSInitialization || len(boxes) < 2 || boxes[0].kind != "ftyp" {
			return fmt.Errorf(i18n.T("MP4 初始化段缺少 ftyp 或超过大小限制"))
		}
		brand, err := mp4Read(r, boxes[0], 0, boxes[0].end-boxes[0].body)
		if err != nil || len(brand) < 8 || len(brand)%4 != 0 {
			return fmt.Errorf(i18n.T("MP4 文件类型无效"))
		}
		compatible := false
		for i := 0; i < len(brand); i += 4 {
			if i == 4 { // minor_version is not a brand
				continue
			}
			value := string(brand[i : i+4])
			compatible = compatible || value == "iso6" || value == "iso7" || value == "iso8" || value == "iso9" || value == "cmfc" || value == "cmfs"
		}
		if !compatible {
			return fmt.Errorf(i18n.T("MP4 初始化段未声明兼容的分片格式"))
		}
		var movie *mp4Box
		for i := 1; i < len(boxes); i++ {
			switch boxes[i].kind {
			case "moov":
				if movie != nil {
					return fmt.Errorf(i18n.T("MP4 初始化段包含重复 moov"))
				}
				movie = &boxes[i]
			case "free", "skip":
			default:
				return fmt.Errorf(i18n.T("暂不支持初始化段中的 MP4 数据块 %q"), boxes[i].kind)
			}
		}
		if movie == nil {
			return fmt.Errorf(i18n.T("MP4 初始化段缺少 moov"))
		}
		tracks, err := mp4InitTracks(r, *movie, &budget)
		if err != nil {
			return err
		}
		s.tracks = tracks
		return nil
	}
	if len(s.tracks) == 0 {
		return fmt.Errorf(i18n.T("MP4 分片缺少初始化段"))
	}
	fragments := 0
	for i := 0; i < len(boxes); i++ {
		box := boxes[i]
		switch box.kind {
		case "moof":
			var data []mp4Box
			for j := i + 1; j < len(boxes) && boxes[j].kind == "mdat"; j++ {
				data = append(data, boxes[j])
			}
			if len(data) == 0 {
				return fmt.Errorf(i18n.T("MP4 分片缺少紧随 moof 的媒体数据"))
			}
			if err := s.fragment(r, box, data, &budget); err != nil {
				return err
			}
			i += len(data)
			fragments++
		case "styp", "sidx", "emsg", "prft", "free", "skip":
		default:
			return fmt.Errorf(i18n.T("暂不支持媒体分片中的 MP4 数据块 %q"), box.kind)
		}
	}
	if fragments == 0 {
		return fmt.Errorf(i18n.T("MP4 分片缺少 moof"))
	}
	return nil
}

func mp4InitTracks(r io.ReaderAt, movie mp4Box, budget *int) (map[uint32]mp4TrackDefaults, error) {
	boxes, err := mp4Boxes(r, movie.body, movie.end, budget)
	if err != nil {
		return nil, err
	}
	ids, defaults := map[uint32]bool{}, map[uint32]mp4TrackDefaults{}
	var header, extends bool
	for _, box := range boxes {
		switch box.kind {
		case "mvhd":
			if header {
				return nil, fmt.Errorf(i18n.T("MP4 电影头重复"))
			}
			if _, err := mp4TimedHeader(r, box, false); err != nil {
				return nil, err
			}
			header = true
		case "trak":
			if extends {
				return nil, fmt.Errorf(i18n.T("MP4 轨道位于 mvex 之后"))
			}
			id, err := mp4Track(r, box, budget)
			if err != nil {
				return nil, err
			}
			if ids[id] || len(ids) >= 64 {
				return nil, fmt.Errorf(i18n.T("MP4 轨道重复或过多"))
			}
			ids[id] = true
		case "mvex":
			if extends {
				return nil, fmt.Errorf(i18n.T("MP4 分片扩展头重复"))
			}
			extends = true
			children, err := mp4Boxes(r, box.body, box.end, budget)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.kind != "trex" {
					continue
				}
				data, err := mp4Read(r, child, 0, 24)
				if err != nil || binary.BigEndian.Uint32(data[:4]) != 0 {
					return nil, fmt.Errorf(i18n.T("MP4 轨道默认值无效"))
				}
				id := binary.BigEndian.Uint32(data[4:8])
				if _, exists := defaults[id]; exists || !ids[id] {
					return nil, fmt.Errorf(i18n.T("MP4 默认轨道与初始化段不一致"))
				}
				if binary.BigEndian.Uint32(data[8:12]) != 1 {
					return nil, fmt.Errorf(i18n.T("暂不支持 MP4 使用其他默认样本描述"))
				}
				defaults[id] = mp4TrackDefaults{duration: binary.BigEndian.Uint32(data[12:16]), size: binary.BigEndian.Uint32(data[16:20])}
			}
		case "pssh":
			return nil, fmt.Errorf(i18n.T("暂不支持带内容保护的 MP4"))
		}
	}
	if !header || !extends || len(ids) == 0 || len(defaults) != len(ids) {
		return nil, fmt.Errorf(i18n.T("MP4 初始化段的轨道信息不完整"))
	}
	return defaults, nil
}

func mp4TimedHeader(r io.ReaderAt, box mp4Box, track bool) (uint32, error) {
	version, err := mp4Read(r, box, 0, 1)
	if err != nil {
		return 0, err
	}
	idOffset, duration, width := int64(12), int64(16), int64(4)
	if version[0] == 1 {
		idOffset, duration, width = 20, 24, 8
	} else if version[0] != 0 {
		return 0, fmt.Errorf(i18n.T("MP4 时间头版本无效"))
	}
	if track {
		duration += 4
	}
	data, err := mp4Read(r, box, 0, duration+width)
	if err != nil {
		return 0, err
	}
	for _, b := range data[duration:] {
		if b != 0 {
			return 0, fmt.Errorf(i18n.T("分片 MP4 初始化段的时长必须为零"))
		}
	}
	id := binary.BigEndian.Uint32(data[idOffset : idOffset+4])
	if track && id == 0 {
		return 0, fmt.Errorf(i18n.T("MP4 轨道编号无效"))
	}
	return id, nil
}

func mp4Track(r io.ReaderAt, track mp4Box, budget *int) (uint32, error) {
	var id uint32
	refs := 0
	var sampleRefs []uint16
	var walk func(int64, int64, int) error
	walk = func(start, end int64, depth int) error {
		if depth > 8 {
			return fmt.Errorf(i18n.T("MP4 轨道层级过深"))
		}
		children, err := mp4Boxes(r, start, end, budget)
		if err != nil {
			return err
		}
		for _, box := range children {
			switch box.kind {
			case "tkhd":
				if id != 0 {
					return fmt.Errorf(i18n.T("MP4 轨道头重复"))
				}
				id, err = mp4TimedHeader(r, box, true)
			case "mdia", "minf", "dinf", "stbl":
				err = walk(box.body, box.end, depth+1)
			case "dref", "stsd":
				var data []byte
				data, err = mp4Read(r, box, 0, 8)
				if err != nil {
					return err
				}
				if binary.BigEndian.Uint32(data[:4]) != 0 {
					return fmt.Errorf(i18n.T("MP4 轨道描述版本无效"))
				}
				entries, entryErr := mp4Boxes(r, box.body+8, box.end, budget)
				if entryErr != nil || uint64(len(entries)) != uint64(binary.BigEndian.Uint32(data[4:8])) || len(entries) == 0 {
					return fmt.Errorf(i18n.T("MP4 轨道描述数量无效"))
				}
				if box.kind == "dref" {
					if refs != 0 {
						return fmt.Errorf(i18n.T("MP4 数据引用重复"))
					}
					for _, entry := range entries {
						flags, readErr := mp4Read(r, entry, 0, 4)
						if readErr != nil || entry.kind != "url " || binary.BigEndian.Uint32(flags) != 1 {
							return fmt.Errorf(i18n.T("暂不支持 MP4 外部数据引用"))
						}
					}
					refs = len(entries)
				} else {
					if len(sampleRefs) != 0 {
						return fmt.Errorf(i18n.T("MP4 样本描述重复"))
					}
					for _, entry := range entries {
						if entry.kind == "encv" || entry.kind == "enca" || entry.kind == "enct" || entry.kind == "encs" || entry.kind == "drms" || entry.kind == "drmi" {
							return fmt.Errorf(i18n.T("暂不支持加密的 MP4 轨道"))
						}
						data, err = mp4Read(r, entry, 0, 8)
						if err != nil {
							return err
						}
						sampleRefs = append(sampleRefs, binary.BigEndian.Uint16(data[6:8]))
					}
				}
			case "stts", "stsc", "stco", "co64":
				var data []byte
				data, err = mp4Read(r, box, 0, 8)
				if err == nil && binary.BigEndian.Uint32(data[4:8]) != 0 {
					return fmt.Errorf(i18n.T("MP4 初始化段包含非分片样本"))
				}
			case "stsz":
				var data []byte
				data, err = mp4Read(r, box, 0, 12)
				if err == nil && binary.BigEndian.Uint32(data[8:12]) != 0 {
					return fmt.Errorf(i18n.T("MP4 初始化段包含非分片样本"))
				}
			case "stz2":
				var data []byte
				data, err = mp4Read(r, box, 0, 12)
				if err == nil && binary.BigEndian.Uint32(data[8:12]) != 0 {
					return fmt.Errorf(i18n.T("MP4 初始化段包含非分片样本"))
				}
			case "pssh", "sinf":
				return fmt.Errorf(i18n.T("暂不支持带内容保护的 MP4"))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(track.body, track.end, 0); err != nil {
		return 0, err
	}
	if id == 0 || refs == 0 || len(sampleRefs) == 0 {
		return 0, fmt.Errorf(i18n.T("MP4 轨道缺少头、数据引用或样本描述"))
	}
	for _, ref := range sampleRefs {
		if ref == 0 || int(ref) > refs {
			return 0, fmt.Errorf(i18n.T("MP4 样本引用了不存在的数据源"))
		}
	}
	return id, nil
}

func (s *hlsMP4State) fragment(r io.ReaderAt, moof mp4Box, media []mp4Box, budget *int) error {
	boxes, err := mp4Boxes(r, moof.body, moof.end, budget)
	if err != nil {
		return err
	}
	var trafs []mp4Box
	header := false
	for _, box := range boxes {
		if box.kind == "traf" {
			trafs = append(trafs, box)
		} else if box.kind == "mfhd" {
			data, err := mp4Read(r, box, 0, 8)
			if err != nil || header || binary.BigEndian.Uint32(data[:4]) != 0 || box.end-box.body != 8 {
				return fmt.Errorf(i18n.T("MP4 电影分片头无效"))
			}
			header = true
		}
	}
	if !header || len(trafs) == 0 {
		return fmt.Errorf(i18n.T("MP4 分片缺少轨道"))
	}
	seen := make(map[uint32]bool)
	for _, traf := range trafs {
		children, err := mp4Boxes(r, traf.body, traf.end, budget)
		if err != nil {
			return err
		}
		var id, sampleSize, defaultDuration uint32
		var decodeTime uint64
		var hasTime, hasHeader bool
		var runs []mp4Box
		for _, box := range children {
			switch box.kind {
			case "tfhd":
				data, err := mp4Read(r, box, 0, box.end-box.body)
				if err != nil || len(data) < 8 || hasHeader {
					return fmt.Errorf(i18n.T("MP4 分片轨道头无效"))
				}
				flags := binary.BigEndian.Uint32(data[:4])
				if flags & ^uint32(0x02003a) != 0 || (flags&0x020000 == 0 && len(trafs) != 1) {
					return fmt.Errorf(i18n.T("暂不支持使用绝对或隐式跨轨道偏移的 MP4"))
				}
				id = binary.BigEndian.Uint32(data[4:8])
				var exists bool
				defaults, found := s.tracks[id]
				exists = found
				if !exists || seen[id] {
					return fmt.Errorf(i18n.T("MP4 分片轨道与初始化段不一致"))
				}
				sampleSize, defaultDuration = defaults.size, defaults.duration
				pos := 8
				for _, flag := range []uint32{2, 8, 16, 32} {
					if flags&flag != 0 {
						if len(data)-pos < 4 {
							return fmt.Errorf(i18n.T("MP4 分片轨道默认值不完整"))
						}
						if flag == 16 {
							sampleSize = binary.BigEndian.Uint32(data[pos : pos+4])
						}
						if flag == 8 {
							defaultDuration = binary.BigEndian.Uint32(data[pos : pos+4])
						}
						if flag == 2 && binary.BigEndian.Uint32(data[pos:pos+4]) != 1 {
							return fmt.Errorf(i18n.T("暂不支持 MP4 分片切换样本描述"))
						}
						pos += 4
					}
				}
				if pos != len(data) {
					return fmt.Errorf(i18n.T("MP4 分片轨道头长度无效"))
				}
				hasHeader = true
			case "tfdt":
				data, err := mp4Read(r, box, 0, box.end-box.body)
				if err != nil || hasTime || len(data) < 4 || (len(data) != 8 && len(data) != 12) || data[0] > 1 || len(data) != 8+int(data[0])*4 || binary.BigEndian.Uint32(data[:4])&0xffffff != 0 {
					return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
				}
				if data[0] == 0 {
					decodeTime = uint64(binary.BigEndian.Uint32(data[4:8]))
				} else {
					decodeTime = binary.BigEndian.Uint64(data[4:12])
				}
				hasTime = true
			case "trun":
				runs = append(runs, box)
			case "senc", "saiz", "saio":
				return fmt.Errorf(i18n.T("暂不支持带辅助加密数据的 MP4"))
			}
		}
		if !hasHeader || !hasTime || len(runs) == 0 {
			return fmt.Errorf(i18n.T("MP4 分片轨道信息不完整"))
		}
		seen[id] = true
		cursor := int64(-1)
		var durationSum uint64
		for _, run := range runs {
			var runDuration uint64
			cursor, runDuration, err = mp4Run(r, run, moof.start, cursor, sampleSize, defaultDuration, media)
			if err != nil {
				return err
			}
			if durationSum > ^uint64(0)-runDuration {
				return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
			}
			durationSum += runDuration
		}
		if decodeTime > ^uint64(0)-durationSum {
			return fmt.Errorf(i18n.T("MP4 分片解码时间无效"))
		}
		if end := decodeTime + durationSum; end > s.lastEnd {
			s.lastEnd = end
		}
	}
	return nil
}

func mp4Run(r io.ReaderAt, run mp4Box, base, cursor int64, defaultSize, defaultDuration uint32, media []mp4Box) (int64, uint64, error) {
	data, err := mp4Read(r, run, 0, run.end-run.body)
	if err != nil || len(data) < 8 {
		return 0, 0, fmt.Errorf(i18n.T("MP4 样本运行表不完整"))
	}
	flags := binary.BigEndian.Uint32(data[:4])
	version := flags >> 24
	flags &= 0xffffff
	if version > 1 || flags & ^uint32(0xf05) != 0 || flags&4 != 0 && flags&0x400 != 0 {
		return 0, 0, fmt.Errorf(i18n.T("MP4 样本运行表标志无效"))
	}
	count := uint64(binary.BigEndian.Uint32(data[4:8]))
	pos := 8
	if flags&1 != 0 {
		if len(data)-pos < 4 {
			return 0, 0, fmt.Errorf(i18n.T("MP4 样本偏移不完整"))
		}
		offset := int64(int32(binary.BigEndian.Uint32(data[pos : pos+4])))
		if offset < 0 || offset > math.MaxInt64-base {
			return 0, 0, fmt.Errorf(i18n.T("MP4 样本偏移无效"))
		}
		cursor = base + offset
		pos += 4
	} else if cursor < 0 {
		return 0, 0, fmt.Errorf(i18n.T("MP4 首个运行表缺少相对偏移"))
	}
	if flags&4 != 0 {
		pos += 4
	}
	stride, sizeOffset, durationOffset := 0, -1, -1
	for _, flag := range []uint32{0x100, 0x200, 0x400, 0x800} {
		if flags&flag != 0 {
			if flag == 0x200 {
				sizeOffset = stride
			}
			if flag == 0x100 {
				durationOffset = stride
			}
			stride += 4
		}
	}
	if pos > len(data) || uint64(len(data)-pos) != count*uint64(stride) {
		return 0, 0, fmt.Errorf(i18n.T("MP4 样本数量与运行表长度不一致"))
	}
	length := count * uint64(defaultSize)
	if sizeOffset >= 0 {
		length = 0
		for i := uint64(0); i < count; i++ {
			p := pos + int(i)*stride + sizeOffset
			length += uint64(binary.BigEndian.Uint32(data[p : p+4]))
		}
	}
	if count == 0 || length == 0 || length > uint64(math.MaxInt64-cursor) {
		return 0, 0, fmt.Errorf(i18n.T("MP4 样本长度无效"))
	}
	duration := count * uint64(defaultDuration)
	if durationOffset >= 0 {
		duration = 0
		for i := uint64(0); i < count; i++ {
			p := pos + int(i)*stride + durationOffset
			duration += uint64(binary.BigEndian.Uint32(data[p : p+4]))
		}
	}
	if duration == 0 {
		return 0, 0, fmt.Errorf(i18n.T("MP4 样本时长无效"))
	}
	for _, box := range media {
		if cursor >= box.body && cursor <= box.end && length <= uint64(box.end-cursor) {
			return cursor + int64(length), duration, nil
		}
	}
	return 0, 0, fmt.Errorf(i18n.T("MP4 样本超出当前分片的媒体数据范围"))
}
