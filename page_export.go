package main

// 任务列表导出：CSV 文件写到默认保存目录，UTF-8 BOM 便于表格软件识别。

import (
	"encoding/csv"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"openDL/download"
	"openDL/pathrec"
)

// exportStateLabel 返回导出用的状态文本。
func exportStateLabel(state download.State) string {
	switch state {
	case download.Done:
		return tr("已完成")
	case download.Running:
		return tr("下载中")
	case download.Canceled:
		return tr("已暂停")
	case download.ErrorS:
		return tr("出错")
	default:
		return tr("排队中")
	}
}

// importTaskList 从 CSV 文件导入任务；地址列为空或不合法的行跳过，与现有
// 任务同地址的行不重复添加。
func (pg *mainPage) importTaskList() {
	if !mygo.App.IsReady() || pg.mgr == nil {
		return
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  pg.win,
		Title:   tr("导入任务列表"),
		Filters: []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}},
	})
	if err != nil || len(paths) == 0 {
		return
	}
	file, err := os.Open(paths[0])
	if err != nil {
		pg.flash(fmt.Sprintf(tr("导入任务列表失败：%s"), err.Error()))
		return
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		pg.flash(tr("任务列表文件格式无效"))
		return
	}
	existing := map[string]bool{}
	for _, snapshot := range pg.mgr.Snapshots() {
		existing[snapshot.URL] = true
	}
	added, skipped := 0, 0
	for index, record := range records {
		if len(record) < 2 {
			continue
		}
		record[0] = strings.TrimPrefix(record[0], "\ufeff")
		address := strings.TrimSpace(record[1])
		if address == "" || (index == 0 && address == tr("地址")) {
			continue
		}
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" {
			skipped++
			continue
		}
		switch parsed.Scheme {
		case "http", "https", "ftp", "ftps":
		default:
			skipped++
			continue
		}
		if existing[address] {
			skipped++
			continue
		}
		existing[address] = true
		req := download.Request{URL: address, Headers: map[string]string{}}
		if name := strings.TrimSpace(record[0]); name != "" {
			req.Name = pathrec.SanitizeFilename(name)
		}
		if len(record) > 2 {
			if page := strings.TrimSpace(record[2]); page != "" {
				req.PageURL = page
				req.Headers["Referer"] = page
			}
		}
		if len(record) > 6 {
			if directory := strings.TrimSpace(record[6]); directory != "" {
				req.Directory = filepath.Dir(directory)
			}
		}
		if len(record) > 7 {
			// 队列按显示名映射到本机队列；未知名称入主队列。
			switch label := strings.TrimSpace(record[7]); label {
			case "":
			case tr("夜间队列"):
				req.Queue = "night"
			default:
				for _, spec := range activeCustomQueues {
					if spec.Name == label {
						req.Queue = spec.ID
						break
					}
				}
			}
		}
		if len(record) > 11 {
			req.MediaMode = strings.TrimSpace(record[11])
		}
		if len(record) > 12 {
			req.MediaQuality = strings.TrimSpace(record[12])
		}
		if len(record) > 13 {
			req.AudioLanguage = strings.TrimSpace(record[13])
		}
		pg.mgr.Add(req)
		added++
	}
	if added == 0 {
		pg.flash(tr("任务列表没有可导入的下载地址"))
		return
	}
	pg.flash(fmt.Sprintf(tr("已导入 %d 个任务，跳过 %d 行"), added, skipped))
}

// exportQueueLabel 返回导出用的队列文本。
func exportQueueLabel(snapshot download.Snapshot) string {
	switch {
	case snapshot.Night:
		return tr("夜间队列")
	case snapshot.Queue == "":
		return tr("主队列")
	default:
		return customQueueName(snapshot.Queue)
	}
}

// exportTaskList 把当前任务列表导出为 CSV 文件；同名文件自动避让。
func (pg *mainPage) exportTaskList() {
	if pg.mgr == nil {
		return
	}
	directory := strings.TrimSpace(pg.preferences.Directory)
	if directory == "" {
		directory = downloadDir()
	}
	stem := "opendl-tasks-" + time.Now().Format("20060102-150405")
	path := ""
	var file *os.File
	var err error
	for i := 0; i < 1000; i++ {
		name := stem + ".csv"
		if i > 0 {
			name = fmt.Sprintf("%s (%d).csv", stem, i)
		}
		candidate := filepath.Join(directory, name)
		file, err = os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			path = candidate
			break
		}
		if !os.IsExist(err) {
			break
		}
	}
	if file == nil {
		pg.flash(fmt.Sprintf(tr("导出任务列表失败：%s"), err.Error()))
		return
	}
	defer file.Close()
	// BOM 让常见表格软件按 UTF-8 识别中文。
	if _, err := file.WriteString("\ufeff"); err != nil {
		pg.flash(fmt.Sprintf(tr("导出任务列表失败：%s"), err.Error()))
		return
	}
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{tr("名称"), tr("地址"), tr("来源页"), tr("状态"), tr("大小"), tr("已下载"), tr("保存位置"), tr("队列"), tr("添加时间"), tr("完成时间"), tr("错误"), tr("媒体模式"), tr("画质"), tr("音轨语言"), tr("同步周期")}); err != nil {
		pg.flash(fmt.Sprintf(tr("导出任务列表失败：%s"), err.Error()))
		return
	}
	for _, snapshot := range pg.mgr.Snapshots() {
		name := snapshot.Name
		if name == "" {
			name = filepath.Base(snapshot.FilePath)
		}
		finished := ""
		if !snapshot.Finished.IsZero() {
			finished = snapshot.Finished.Format("2006-01-02 15:04:05")
		}
		created := ""
		if !snapshot.Created.IsZero() {
			created = snapshot.Created.Format("2006-01-02 15:04:05")
		}
		if err := writer.Write([]string{
			name,
			snapshot.URL,
			snapshot.PageURL,
			exportStateLabel(snapshot.State),
			strconv.FormatInt(snapshot.Total, 10),
			strconv.FormatInt(snapshot.Done, 10),
			snapshot.FilePath,
			exportQueueLabel(snapshot),
			created,
			finished,
			snapshot.Err,
			snapshot.MediaMode,
			snapshot.MediaQuality,
			snapshot.AudioLanguage,
			strconv.Itoa(snapshot.SyncMinutes),
		}); err != nil {
			pg.flash(fmt.Sprintf(tr("导出任务列表失败：%s"), err.Error()))
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		pg.flash(fmt.Sprintf(tr("导出任务列表失败：%s"), err.Error()))
		return
	}
	pg.flash(fmt.Sprintf(tr("已导出任务列表：%s"), path))
}
