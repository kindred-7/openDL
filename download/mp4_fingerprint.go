package download

// 初始化段结构指纹覆盖电影和轨道时间刻度、轨道显示信息、处理类型、编辑列表、样本描述与 trex 默认值，
// 打包时间戳、时长等附带差异不影响指纹。字节不同的初始化段只要结构一致，
// 可作为同一轨道的继续（多周期拼接与初始化段切换的共同基础）。

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"openDL/i18n"
)

var errMP4InitIncompatible = i18n.NewError("MP4 初始化段不同，暂不支持该封装")

const mp4FingerprintVersion = 5

// mp4InitFingerprint 计算初始化段的结构指纹。
func mp4InitFingerprint(r io.ReaderAt, start, end int64) (string, error) {
	budget := 20000
	boxes, err := mp4Boxes(r, start, end, &budget)
	if err != nil {
		return "", err
	}
	engine := sha256.New()
	found := false
	for _, box := range boxes {
		if box.kind != "moov" {
			continue
		}
		found = true
		children, err := mp4Boxes(r, box.body, box.end, &budget)
		if err != nil {
			return "", err
		}
		for _, child := range children {
			switch child.kind {
			case "mvhd":
				// Edit-list segment durations use the movie timescale, even
				// when the track's own media timescale is unchanged.
				data, err := mp4Read(r, child, 0, min(child.end-child.body, 24))
				if err != nil || len(data) < 16 {
					return "", i18n.NewError("MP4 电影时间刻度无效")
				}
				offset := 12
				if data[0] == 1 {
					offset = 20
				} else if data[0] != 0 {
					return "", i18n.NewError("MP4 电影时间刻度无效")
				}
				if len(data) < offset+4 || binary.BigEndian.Uint32(data[offset:offset+4]) == 0 {
					return "", i18n.NewError("MP4 电影时间刻度无效")
				}
				fmt.Fprintf(engine, "mvhd-timescale:%d\n", binary.BigEndian.Uint32(data[offset:offset+4]))
			case "trak":
				id, timescale, description, err := mp4TrackFingerprint(r, child, &budget)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(engine, "trak:%d:%d:%s\n", id, timescale, description)
			case "mvex":
				entries, err := mp4Boxes(r, child.body, child.end, &budget)
				if err != nil {
					return "", err
				}
				for _, entry := range entries {
					if entry.kind != "trex" {
						continue
					}
					data, err := mp4Read(r, entry, 0, entry.end-entry.body)
					if err != nil {
						return "", err
					}
					if len(data) < 24 {
						return "", fmt.Errorf(i18n.T("MP4 分片轨道默认值不完整"))
					}
					fmt.Fprintf(engine, "trex:%x\n", data)
				}
			}
		}
	}
	if !found {
		return "", fmt.Errorf(i18n.T("MP4 初始化段缺少 moov"))
	}
	return hex.EncodeToString(engine.Sum(nil)), nil
}

// captureInitialization 记录首个初始化段的字节摘要与结构指纹。
func captureInitialization(state *hlsMP4State, out *os.File, start, end int64) {
	if state == nil || state.initDigest != "" {
		return
	}
	if digest, err := chunkDigest(out, chunkSpan{start: start, end: end - 1}); err == nil {
		state.initDigest = digest
	}
	if fingerprint, err := mp4InitFingerprint(out, start, end); err == nil {
		state.initStructure = fingerprint
	}
}

// spliceState 导出多周期缝合状态供检查点保存。
func (s *hlsMP4State) spliceState() *dashSpliceState {
	if s == nil {
		return nil
	}
	return &dashSpliceState{
		FingerprintVersion: mp4FingerprintVersion,
		Period:             s.period,
		Shift:              s.shift,
		ShiftActive:        s.shiftActive,
		PendingShift:       s.pendingShift,
		LastEnd:            s.lastEnd,
		InitDigest:         s.initDigest,
		InitStructure:      s.initStructure,
	}
}

// restoreSplice 从检查点恢复多周期缝合状态；指纹为空时保留现场计算值。
func (s *hlsMP4State) restoreSplice(splice *dashSpliceState) {
	if s == nil || splice == nil {
		return
	}
	s.period = splice.Period
	s.shift = splice.Shift
	s.shiftActive = splice.ShiftActive
	s.pendingShift = splice.PendingShift
	s.lastEnd = splice.LastEnd
	if splice.InitDigest != "" {
		s.initDigest = splice.InitDigest
	}
	if splice.InitStructure != "" {
		s.initStructure = splice.InitStructure
	}
}

// reuseInitialization 判断重复初始化段能否复用：字节一致或结构指纹一致时
// 置缝合标记并返回 true；结构不同时返回错误。调用方负责回退已写入字节。
func reuseInitialization(state *hlsMP4State, out *os.File, start, end int64) (bool, error) {
	if state == nil || len(state.tracks) == 0 {
		return false, nil
	}
	digest, err := chunkDigest(out, chunkSpan{start: start, end: end - 1})
	if err != nil {
		return false, err
	}
	if digest == state.initDigest {
		state.pendingShift = true
		return true, nil
	}
	// Fingerprints only cover selected structural fields. Validate the whole
	// new initialization segment before treating a matching fingerprint as
	// reusable, so unsupported boxes or protection cannot be hidden by it.
	if end <= start || end-start > maxHLSInitialization {
		return false, i18n.NewError("MP4 初始化段缺少 ftyp 或超过大小限制")
	}
	var candidate hlsMP4State
	if err := candidate.accept(io.NewSectionReader(out, start, end-start), 0, end-start, true); err != nil {
		return false, err
	}
	fingerprint, err := mp4InitFingerprint(out, start, end)
	if err != nil {
		return false, err
	}
	if fingerprint != state.initStructure {
		return false, errMP4InitIncompatible
	}
	state.pendingShift = true
	return true, nil
}

// mp4TrackFingerprint 提取一个 trak 的结构要素：轨道号、mdhd 时间刻度与
// 完整样本描述的摘要。
func mp4TrackFingerprint(r io.ReaderAt, trak mp4Box, budget *int) (uint32, uint64, string, error) {
	id, timescale := uint32(0), uint64(0)
	description := ""
	semantics := sha256.New()
	err := mp4Walk(r, trak, budget, 0, func(kind string, data []byte) {
		switch kind {
		case "tkhd":
			if len(data) >= 24 {
				if data[0] == 1 && len(data) >= 32 {
					id = binary.BigEndian.Uint32(data[20:24])
				} else if data[0] == 0 {
					id = binary.BigEndian.Uint32(data[12:16])
				}
			}
			// Preserve display matrix, dimensions, layer and volume while
			// excluding creation/modification times and track duration.
			if len(data) >= 4 {
				fmt.Fprintf(semantics, "tkhd-flags:%x\n", data[1:4])
			}
			if len(data) >= 84 && data[0] == 0 {
				fmt.Fprintf(semantics, "tkhd-display:%x\n", data[32:])
			} else if len(data) >= 96 && data[0] == 1 {
				fmt.Fprintf(semantics, "tkhd-display:%x\n", data[44:])
			} else {
				fmt.Fprintf(semantics, "tkhd-short:%x\n", data)
			}
		case "mdhd":
			if len(data) >= 16 {
				if data[0] == 1 && len(data) >= 24 {
					timescale = uint64(binary.BigEndian.Uint32(data[20:24]))
				} else if data[0] == 0 {
					timescale = uint64(binary.BigEndian.Uint32(data[12:16]))
				}
			}
		case "stsd":
			digest := sha256.Sum256(data)
			description = hex.EncodeToString(digest[:])
		case "hdlr":
			// The handler type identifies how the track is interpreted.
			// Names and reserved fields do not alter media semantics.
			if len(data) >= 12 {
				fmt.Fprintf(semantics, "hdlr:%x\n", data[8:12])
			} else {
				fmt.Fprintf(semantics, "invalid-hdlr:%x\n", data)
			}
		case "elst":
			// Edit lists map media time to the movie timeline. Reusing an
			// initialization with a different list can shift audio or video.
			fmt.Fprintf(semantics, "elst:%x\n", data)
		}
	})
	if err != nil {
		return 0, 0, "", err
	}
	if id == 0 || timescale == 0 || description == "" {
		return 0, 0, "", fmt.Errorf(i18n.T("MP4 初始化段的轨道信息不完整"))
	}
	return id, timescale, fmt.Sprintf("%s:%x", description, semantics.Sum(nil)), nil
}

// mp4Walk 递归遍历 trak 的后代数据块，把叶子负载交给收集器。
func mp4Walk(r io.ReaderAt, box mp4Box, budget *int, depth int, collect func(kind string, data []byte)) error {
	if depth > 6 {
		return nil
	}
	children, err := mp4Boxes(r, box.body, box.end, budget)
	if err != nil {
		return err
	}
	for _, child := range children {
		switch child.kind {
		case "tkhd", "mdhd", "stsd", "hdlr", "elst":
			data, err := mp4Read(r, child, 0, child.end-child.body)
			if err != nil {
				return err
			}
			collect(child.kind, data)
		case "trak", "mdia", "minf", "stbl", "edts", "moov", "mvex":
			if err := mp4Walk(r, child, budget, depth+1, collect); err != nil {
				return err
			}
		}
	}
	return nil
}
