package main

// 诊断日志与导出：标准 log 输出滚动写入 %LOCALAPPDATA%\opendl\logs（保留
// 3 个文件），「导出诊断信息」汇总版本、最近日志与脱敏配置；导出内容绝不
// 包含 Cookie、密码或令牌。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openDL/i18n"

	"github.com/egoist/mygo"
)

const (
	diagLogName    = "opendl.log"
	diagLogMaxSize = 1 << 20 // 单个日志文件 1 MiB
	diagLogKeep    = 3       // opendl.log 与两个滚动文件
	diagRecentKeep = 200     // 导出保留的最近日志行数
)

// diagLogDir 是诊断日志目录：%LOCALAPPDATA%\opendl\logs。
func diagLogDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		base = dir
	}
	return filepath.Join(base, "opendl", "logs")
}

// diagLogWriter 把日志滚动写入文件并保留最近行；写入失败静默返回，
// 日志不得影响主流程。
type diagLogWriter struct {
	mu     sync.Mutex
	file   *os.File
	size   int64
	path   string
	recent []string
}

func (w *diagLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		w.recent = append(w.recent, line)
		if len(w.recent) > diagRecentKeep {
			w.recent = w.recent[len(w.recent)-diagRecentKeep:]
		}
	}
	if w.file == nil {
		if err := w.open(); err != nil {
			return len(p), nil
		}
	}
	if w.size+int64(len(p)) > diagLogMaxSize {
		_ = w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// open 打开当前日志文件并按既有长度续写。
func (w *diagLogWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	return nil
}

// rotate 把 opendl.log 依次滚动为 .1、.2，超出保留数的旧文件删除。
func (w *diagLogWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file, w.size = nil, 0
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, diagLogKeep-1))
	for i := diagLogKeep - 2; i >= 0; i-- {
		from := w.path
		if i > 0 {
			from = fmt.Sprintf("%s.%d", w.path, i)
		}
		_ = os.Rename(from, fmt.Sprintf("%s.%d", w.path, i+1))
	}
	return w.open()
}

var diagLog *diagLogWriter

// setupDiagLog 把标准 log 输出接到滚动文件；stderr 输出保持不变，
// 仅接管其落盘副本。
func setupDiagLog() {
	dir := diagLogDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	diagLog = &diagLogWriter{path: filepath.Join(dir, diagLogName)}
	log.SetOutput(io.MultiWriter(os.Stderr, diagLog))
}

// diagnosticsReport 汇总诊断信息：版本、时间、最近日志与脱敏配置。
// 配置经 settingsExport 复制并清空密码字段，绝不包含 Cookie、密码或令牌。
func diagnosticsReport(pg *mainPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s diagnostics\n", appBrand)
	fmt.Fprintf(&b, "version: %s\n", d1AppVersion())
	fmt.Fprintf(&b, "generated: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "locale: %s\n", i18n.Locale())
	b.WriteString("\n[recent log]\n")
	if diagLog != nil {
		diagLog.mu.Lock()
		b.WriteString(strings.Join(diagLog.recent, "\n"))
		diagLog.mu.Unlock()
		b.WriteString("\n")
	}
	b.WriteString("\n[settings]\n")
	prefs := settingsExport(pg.preferences)
	prefs.Proxy.Password = ""
	if data, err := json.MarshalIndent(prefs, "", "  "); err == nil {
		b.Write(data)
		b.WriteString("\n")
	}
	return b.String()
}

// exportDiagnostics 导出诊断信息到用户选择的文件；写盘走后台通道。
func (pg *mainPage) exportDiagnostics() {
	if !mygo.App.IsReady() {
		return
	}
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            pg.win,
		Title:             tr("导出诊断信息"),
		DefaultPath:       "opendl-diagnostics.txt",
		Filters:           []mygo.FileFilter{{Name: "Text", Extensions: []string{"txt"}}},
		CreateDirectories: true,
	})
	if err != nil || path == "" {
		return
	}
	report := diagnosticsReport(pg)
	write := func() error { return os.WriteFile(path, []byte(report), 0o600) }
	if pg.win == nil {
		if err := write(); err != nil {
			pg.flash("导出诊断信息失败：%s", err.Error())
			return
		}
		pg.flash("已导出诊断信息：%s", path)
		return
	}
	pg.queueFileWrite(func() error {
		err := write()
		pg.win.Update(func() {
			select {
			case <-pg.stop:
			default:
				if err != nil {
					pg.flash("导出诊断信息失败：%s", err.Error())
					return
				}
				pg.flash("已导出诊断信息：%s", path)
			}
		})
		return nil
	})
}
