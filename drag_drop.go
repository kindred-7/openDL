package main

// 外部拖放：拖入的链接、文本或链接列表文件进入添加下载对话框；已完成
// 任务的文件可以拖出到浏览器、资源管理器等应用（复制操作）。

import (
	"os"
	"path/filepath"
	"strings"

	"openDL/pathrec"

	"github.com/egoist/mygo/transfer"
)

// takeDropped 处理拖入主窗口的数据；解析在后台进行，不阻塞界面。
func (pg *mainPage) takeDropped(drop transfer.Drop) {
	if pg.win == nil {
		return
	}
	pg.dropJob++
	job := pg.dropJob
	data := drop.Data
	win := pg.win
	go func() {
		links := droppedLinks(data)
		win.Update(func() {
			select {
			case <-pg.stop:
				return
			default:
			}
			if job != pg.dropJob {
				return
			}
			if len(links) == 0 {
				pg.flash(tr("未识别到下载链接"))
				return
			}
			pg.dlgLinks = strings.Join(links, "\n")
			pg.openAdd(false)
		})
	}()
}

// droppedLinks 提取拖放数据中的下载链接：直接拖来的地址、选中的文本，
// 或包含链接的小型文本文件。
func droppedLinks(data transfer.Data) []string {
	seen := map[string]bool{}
	var links []string
	add := func(values []string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || len(value) > 4096 || seen[value] {
				continue
			}
			lower := strings.ToLower(value)
			if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
				continue
			}
			seen[value] = true
			links = append(links, value)
		}
	}
	if urls, err := data.URLs(); err == nil {
		add(urls)
	}
	if len(links) == 0 {
		if files, err := data.Files(); err == nil {
			for _, file := range files {
				add(linksFromTextFile(file))
			}
		}
	}
	if len(links) == 0 {
		if raw, err := data.Read(transfer.Text); err == nil {
			add(pathrec.ExtractURLs(string(raw)))
		}
	}
	if len(links) > 500 {
		links = links[:500]
	}
	return links
}

// linksFromTextFile 从文本文件提取链接；仅读取小型文本文件。
func linksFromTextFile(path string) []string {
	const maxLinkFileBytes = 1 << 20
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".html", ".htm", ".url", ".md", ".log":
	default:
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 || info.Size() > maxLinkFileBytes {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return pathrec.ExtractURLs(string(raw))
}
