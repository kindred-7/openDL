package download

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"openDL/i18n"
)

// NormalizeMediaMuxProgram performs syntax checks only; starting or probing a
// user-selected executable is deferred until a media task actually needs it.
func NormalizeMediaMuxProgram(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") || !filepath.IsAbs(value) {
		return "", i18n.NewError("FFmpeg 路径必须是有效的绝对路径")
	}
	return filepath.Clean(value), nil
}

// SetMediaMuxProgram preserves an invalid persisted value so it cannot
// silently switch to a different program from PATH.
func (m *Manager) SetMediaMuxProgram(value string) {
	m.mu.Lock()
	m.mediaMuxProgram = value
	m.mu.Unlock()
}

func (m *Manager) resolveMediaMuxProgram() (string, error) {
	m.mu.Lock()
	configured := m.mediaMuxProgram
	m.mu.Unlock()
	value, err := NormalizeMediaMuxProgram(configured)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	value, err = exec.LookPath("ffmpeg")
	if err != nil {
		return "", i18n.NewError("独立音视频轨合并需要安装 FFmpeg 并将其加入系统 PATH，或在设置中指定路径")
	}
	return filepath.Abs(value)
}

// muxDiagnostics bounds subprocess output while letting the child finish even
// when a malformed input produces an excessive number of diagnostic messages.
type muxDiagnostics struct{ buf bytes.Buffer }

func (d *muxDiagnostics) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := (16 << 10) - d.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = d.buf.Write(p)
	}
	return n, nil
}

// muxLocalTracks creates an unpublished temporary MP4 in destinationDir. The
// caller owns the returned file and must remove it or pass it to the existing
// verified publication path. The executable must have been selected by the
// application; this helper never downloads or installs external software.
// Each input has its own file-only whitelist, so nested remote references in
// untrusted media cannot be opened by the subprocess.
func muxLocalTracks(ctx context.Context, executable, video, audio, destinationDir string) (string, error) {
	if ctx == nil || !filepath.IsAbs(executable) || !filepath.IsAbs(video) || !filepath.IsAbs(audio) || !filepath.IsAbs(destinationDir) {
		return "", i18n.NewError("媒体混流需要有效的本地绝对路径")
	}
	if filepath.Clean(video) == filepath.Clean(audio) {
		return "", i18n.NewError("视频和音频轨道不能使用同一文件")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(destinationDir, ".opendl-mux-*.mp4")
	if err != nil {
		return "", fmt.Errorf(i18n.T("无法创建混流临时文件：%w"), err)
	}
	output := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(output)
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(output)
		}
	}()
	// -map requires both streams; a missing stream must not publish video-only
	// or audio-only output. -c copy does not decode or re-encode the media.
	cmd := exec.CommandContext(ctx, executable,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-protocol_whitelist", "file", "-i", video,
		"-protocol_whitelist", "file", "-i", audio,
		"-map", "0:v:0", "-map", "1:a:0", "-map_metadata", "-1", "-map_chapters", "-1",
		"-c", "copy", "-f", "mp4", output)
	hideMediaMux(cmd)
	var diagnostics muxDiagnostics
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		message := strings.TrimSpace(diagnostics.buf.String())
		if message == "" {
			return "", fmt.Errorf(i18n.T("媒体混流失败：%w"), err)
		}
		return "", fmt.Errorf(i18n.T("媒体混流失败：%s"), message)
	}
	if err := ctx.Err(); err != nil {
		return "", err
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
