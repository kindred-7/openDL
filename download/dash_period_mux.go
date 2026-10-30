package download

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openDL/i18n"
)

// Each input is independently initialized and has a separate checkpoint.
type dashPeriodInput struct {
	video, audio             string
	width, height            int
	videoOffset, audioOffset uint64
	videoScale, audioScale   uint64
	duration                 time.Duration
	videoHLS, audioHLS       bool
	videoDelay, audioDelay   float64
}

// The first visual sample entry contains coded width and height. The caller
// already validated the initialization and requires a single track.
func dashVideoDimensions(r io.ReaderAt, end int64) (int, int, error) {
	budget := 20000
	boxes, err := mp4Boxes(r, 0, end, &budget)
	if err != nil {
		return 0, 0, err
	}
	width, height := 0, 0
	for _, box := range boxes {
		if box.kind != "moov" {
			continue
		}
		err = mp4Walk(r, box, &budget, 0, func(kind string, data []byte) {
			if kind != "stsd" || len(data) < 44 || binary.BigEndian.Uint32(data[4:8]) < 1 {
				return
			}
			entrySize := binary.BigEndian.Uint32(data[8:12])
			if entrySize < 36 || uint64(entrySize) > uint64(len(data)-8) {
				return
			}
			width = int(binary.BigEndian.Uint16(data[40:42]))
			height = int(binary.BigEndian.Uint16(data[42:44]))
		})
		if err != nil {
			return 0, 0, err
		}
	}
	if width < 2 || height < 2 || width > 7680 || height > 7680 {
		return 0, 0, i18n.NewError("DASH 周期视频尺寸无效或超出限制")
	}
	return width, height, nil
}

func dashPeriodSegments(track *dashTrack, period int) []hlsSegment {
	if track == nil {
		return nil
	}
	var result []hlsSegment
	for _, segment := range track.Segments {
		if segment.period == period {
			segment.period = 0 // the temporary file has its own timeline
			result = append(result, segment)
		}
	}
	return result
}

func (m *Manager) downloadDASHSeparatePeriods(t *Task, original, videoBase, audioBase *url.URL, video, audio *dashTrack, periods int) error {
	if periods > len(t.dashPeriods) {
		return i18n.NewError("初始化段结构不兼容，周期转码最多支持 8 个周期")
	}
	if periods < 2 || periods > 8 || (video == nil && audio == nil) || (video != nil && videoBase == nil) || (audio != nil && audioBase == nil) {
		return i18n.NewError("DASH 周期分片计划无效")
	}
	program, err := m.resolveMediaMuxProgram()
	if err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	if filePath == "" || !strings.EqualFold(filepath.Ext(filePath), ".mp4") {
		return i18n.NewError("DASH 媒体格式已改变，请新建任务")
	}
	t.done.Store(0)
	t.total.Store(-1)
	t.resumeSafe.Store(false)
	t.resetSegments(nil)
	inputs := make([]dashPeriodInput, periods)
	for period := 0; period < periods; period++ {
		for _, item := range []struct {
			track      *dashTrack
			base       *url.URL
			path       *string
			checkpoint *dashTrackCheckpoint
			kind       string
		}{{video, videoBase, &inputs[period].video, &t.dashPeriods[period].Video, "video"}, {audio, audioBase, &inputs[period].audio, &t.dashPeriods[period].Audio, "audio"}} {
			if item.track == nil {
				continue
			}
			segments := dashPeriodSegments(item.track, period)
			if len(segments) < 2 || !segments[0].init || !segments[0].mp4 {
				return i18n.NewError("DASH 周期分片计划无效")
			}
			isHLS := t.mediaMode == "hls-ts" && segments[0].dashTimescale == 0
			if (!isHLS && segments[0].dashTimescale == 0) || segments[0].dashTimescale > 1<<53 || segments[0].dashOffset > 1<<53 {
				return i18n.NewError("DASH 周期时间偏移无效或超出限制")
			}
			if inputs[period].duration != 0 && inputs[period].duration != segments[0].dashPeriodDuration {
				return i18n.NewError("DASH 周期分片计划无效")
			}
			inputs[period].duration = segments[0].dashPeriodDuration
			if item.kind == "video" {
				inputs[period].videoScale, inputs[period].videoOffset = segments[0].dashTimescale, segments[0].dashOffset
				inputs[period].videoHLS = isHLS
			} else {
				inputs[period].audioScale, inputs[period].audioOffset = segments[0].dashTimescale, segments[0].dashOffset
				inputs[period].audioHLS = isHLS
			}
			path := dashPeriodPath(filePath, t.id, period, item.kind)
			t.checkpointMu.Lock()
			saved := cloneDASHCheckpoint(*item.checkpoint)
			t.checkpointMu.Unlock()
			file, err := openDASHTrackFile(path, saved)
			if err != nil {
				return err
			}
			if err := m.prepareOutputLimit(t, file, -1); err != nil {
				file.Close()
				return err
			}
			err = m.downloadDASHTrack(t, file, original, item.base, segments, item.checkpoint)
			t.checkpointMu.Lock()
			checkpoint := cloneDASHCheckpoint(*item.checkpoint)
			t.checkpointMu.Unlock()
			if err == nil && len(checkpoint.Completed) > 0 {
				var state hlsMP4State
				err = state.accept(file, 0, checkpoint.Completed[0].End, true)
				if err == nil && len(state.tracks) != 1 {
					err = i18n.NewError("DASH 周期转码暂不支持包含多条轨道的输入")
				}
				if err == nil && item.kind == "video" {
					inputs[period].width, inputs[period].height, err = dashVideoDimensions(file, checkpoint.Completed[0].End)
				}
			} else if err == nil {
				err = i18n.NewError("DASH 周期分片计划无效")
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			*item.path, err = filepath.Abs(file.Name())
			if err != nil {
				return err
			}
		}
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
	t.reencoding.Store(true)
	m.notify()
	if video != nil && audio != nil && t.mediaMode == "hls-ts" {
		for i := range inputs {
			var videoPTS, audioPTS float64
			videoPTS, err = firstDecodedPTS(ctx, program, inputs[i].video, true)
			if err == nil {
				audioPTS, err = firstDecodedPTS(ctx, program, inputs[i].audio, false)
			}
			if err != nil {
				break
			}
			difference := videoPTS - audioPTS
			if math.IsNaN(difference) || math.IsInf(difference, 0) || math.Abs(difference) > 60 {
				err = i18n.NewError("HLS 音视频起始时间差无效或超出限制")
				break
			}
			inputs[i].videoDelay = max(difference, 0)
			inputs[i].audioDelay = max(-difference, 0)
		}
	}
	var output string
	if err == nil {
		output, err = transcodeDASHPeriods(ctx, program, inputs, t.directory, video != nil, audio != nil)
	}
	t.reencoding.Store(false)
	m.notify()
	close(finished)
	if err != nil {
		if ctx.Err() != nil {
			return contextCanceled
		}
		return err
	}
	defer os.Remove(output)
	info, err := os.Stat(output)
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
	part := filePath + ".part"
	if err = replaceSyncedFile(output, part); err != nil {
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

// Decode only the first frame using the same timestamp options as the final
// transcode. The paired tracks must originate from the same HLS timeline.
func firstDecodedPTS(ctx context.Context, executable, path string, video bool) (float64, error) {
	if !filepath.IsAbs(path) {
		return 0, i18n.NewError("HLS 轨道路径无效")
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "info", "-xerror", "-copyts", "-protocol_whitelist", "file", "-i", path}
	filter := "ashowinfo"
	if video {
		filter = "showinfo"
		args = append(args, "-map", "0:v:0", "-an", "-vf", "settb=AVTB,"+filter, "-frames:v", "1")
	} else {
		args = append(args, "-map", "0:a:0", "-vn", "-af", "asettb=AVTB,"+filter, "-frames:a", "1")
	}
	args = append(args, "-f", "null", "-")
	cmd := exec.CommandContext(ctx, executable, args...)
	hideMediaMux(cmd)
	var diagnostics muxDiagnostics
	reader := &firstPTSLog{filter: filter}
	cmd.Stderr = io.MultiWriter(&diagnostics, reader)
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf(i18n.T("无法读取 HLS 轨道起始时间：%w"), err)
	}
	reader.consumeLine()
	if reader.found {
		return float64(reader.pts) / 1e6, nil
	}
	return 0, i18n.NewError("无法读取 HLS 轨道起始时间")
}

// Scan complete stderr lines independently of the bounded diagnostics buffer.
// A long metadata line is discarded through its newline, keeping memory use
// bounded while allowing later showinfo lines to be read.
type firstPTSLog struct {
	filter   string
	line     []byte
	dropping bool
	seen     bool
	found    bool
	pts      int64
}

func (p *firstPTSLog) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			p.consumeLine()
			p.dropping = false
			continue
		}
		if p.seen || p.dropping {
			continue
		}
		if len(p.line) >= 8192 {
			if strings.Contains(string(p.line), "Parsed_"+p.filter+"_") && strings.Contains(string(p.line), "pts:") {
				p.seen = true
			}
			p.line = p.line[:0]
			p.dropping = true
			continue
		}
		p.line = append(p.line, b)
	}
	return len(data), nil
}

func (p *firstPTSLog) consumeLine() {
	defer func() { p.line = p.line[:0] }()
	if p.seen || p.dropping {
		return
	}
	line := string(p.line)
	if !strings.Contains(line, "Parsed_"+p.filter+"_") {
		return
	}
	_, value, ok := strings.Cut(line, "pts:")
	if !ok {
		return
	}
	// Do not substitute a later frame when the first frame has no PTS.
	p.seen = true
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return
	}
	// Parse integer PTS after selecting FFmpeg's microsecond AVTB; the
	// displayed pts_time can lose precision for large timestamps.
	pts, err := strconv.ParseInt(fields[0], 10, 64)
	if err == nil && pts >= -1e15 && pts <= 1e15 {
		p.pts, p.found = pts, true
	}
}

// Re-encoding is required when decoder configuration changes. FFmpeg's
// concat filter receives each period on a common, aspect-preserving canvas.
// Input protocols are restricted to local files.
func transcodeDASHPeriods(ctx context.Context, executable string, inputs []dashPeriodInput, directory string, hasVideo, hasAudio bool) (string, error) {
	if ctx == nil || !filepath.IsAbs(executable) || len(inputs) < 2 || len(inputs) > 8 || (!hasVideo && !hasAudio) {
		return "", i18n.NewError("DASH 周期分片计划无效")
	}
	file, err := os.CreateTemp(directory, ".opendl-period-output-*.mp4")
	if err != nil {
		return "", err
	}
	output := file.Name()
	if err = file.Close(); err != nil {
		_ = os.Remove(output)
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(output)
		}
	}()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-xerror", "-copyts", "-y"}
	var filters, concat strings.Builder
	canvasWidth, canvasHeight := 0, 0
	if hasVideo {
		for _, input := range inputs {
			if input.width < 2 || input.height < 2 || input.width > 7680 || input.height > 7680 {
				return "", i18n.NewError("DASH 周期视频尺寸无效或超出限制")
			}
			canvasWidth = max(canvasWidth, input.width)
			canvasHeight = max(canvasHeight, input.height)
		}
		canvasWidth += canvasWidth % 2
		canvasHeight += canvasHeight % 2
	}
	index := 0
	for period, input := range inputs {
		if hasVideo {
			if !filepath.IsAbs(input.video) || (!input.videoHLS && input.videoScale == 0) || input.videoScale > 1<<53 || input.videoOffset > 1<<53 {
				return "", i18n.NewError("DASH 周期分片计划无效")
			}
			args = append(args, "-protocol_whitelist", "file", "-i", input.video)
			if input.videoHLS {
				fmt.Fprintf(&filters, "[%d:v:0]setpts=PTS-STARTPTS,trim=start=0", index)
			} else {
				fmt.Fprintf(&filters, "[%d:v:0]setpts=PTS-(%d/%d)/TB,trim=start=0", index, input.videoOffset, input.videoScale)
			}
			if input.duration > 0 {
				fmt.Fprintf(&filters, ":end=%d.%09d", input.duration/time.Second, input.duration%time.Second)
			}
			if input.videoHLS && input.videoDelay > 0 {
				fmt.Fprintf(&filters, ",tpad=start_mode=clone:start_duration=%.9f", input.videoDelay)
			}
			fmt.Fprintf(&filters, ",scale=trunc(iw*sar/2)*2:ih,setsar=1,scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2[v%d];", canvasWidth, canvasHeight, canvasWidth, canvasHeight, period)
			fmt.Fprintf(&concat, "[v%d]", period)
			index++
		}
		if hasAudio {
			if !filepath.IsAbs(input.audio) || (!input.audioHLS && input.audioScale == 0) || input.audioScale > 1<<53 || input.audioOffset > 1<<53 {
				return "", i18n.NewError("DASH 周期分片计划无效")
			}
			args = append(args, "-protocol_whitelist", "file", "-i", input.audio)
			if input.audioHLS {
				fmt.Fprintf(&filters, "[%d:a:0]asetpts=PTS-STARTPTS,atrim=start=0", index)
			} else {
				fmt.Fprintf(&filters, "[%d:a:0]asetpts=PTS-(%d/%d)/TB,atrim=start=0", index, input.audioOffset, input.audioScale)
			}
			if input.duration > 0 {
				fmt.Fprintf(&filters, ":end=%d.%09d", input.duration/time.Second, input.duration%time.Second)
			}
			if input.audioHLS && input.audioDelay > 0 {
				fmt.Fprintf(&filters, ",adelay=%.3f:all=1", input.audioDelay*1000)
			}
			fmt.Fprintf(&filters, ",aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo[a%d];", period)
			fmt.Fprintf(&concat, "[a%d]", period)
			index++
		}
	}
	v, a := 0, 0
	if hasVideo {
		v = 1
	}
	if hasAudio {
		a = 1
	}
	fmt.Fprintf(&filters, "%sconcat=n=%d:v=%d:a=%d", concat.String(), len(inputs), v, a)
	if hasVideo {
		filters.WriteString("[vout]")
	}
	if hasAudio {
		filters.WriteString("[aout]")
	}
	args = append(args, "-filter_complex", filters.String())
	if hasVideo {
		args = append(args, "-map", "[vout]", "-c:v", "mpeg4", "-q:v", "3")
	}
	if hasAudio {
		args = append(args, "-map", "[aout]", "-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-f", "mp4", output)
	cmd := exec.CommandContext(ctx, executable, args...)
	hideMediaMux(cmd)
	var diagnostics muxDiagnostics
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf(i18n.T("DASH 周期转码失败：%s"), strings.TrimSpace(diagnostics.buf.String()))
	}
	info, err := os.Stat(output)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", i18n.NewError("媒体混流未生成有效文件")
	}
	keep = true
	return output, nil
}
