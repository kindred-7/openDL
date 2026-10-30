package main

// 下载后扫描：任务完成后调用配置的扫描程序检查文件。参数模板以空格分隔，
// {file} 表示文件路径；模板为空时文件作为唯一参数。非零退出码视为未通过。

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

const (
	scanTimeout  = 10 * time.Minute
	scanNotifyID = "download-scan"
)

// scanSlots 限制同时运行的扫描进程数量，批量完成不拖垮机器。
var scanSlots = make(chan struct{}, 2)

// startPostScan 在后台执行扫描；未配置程序或没有文件时什么都不做。设置
// 由调用方在 UI 线程读取，扫描进程与界面互不影响。
func (pg *mainPage) startPostScan(name, file string) {
	program := strings.TrimSpace(pg.preferences.ScanProgram)
	if program == "" || file == "" {
		return
	}
	args := scanArguments(pg.preferences.ScanArgs, file)
	go func() {
		scanSlots <- struct{}{}
		defer func() { <-scanSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
		defer cancel()
		err := exec.CommandContext(ctx, program, args...).Run()
		switch {
		case err == nil:
			return
		case ctx.Err() != nil:
			pg.reportScan(name, "扫描超时")
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				pg.reportScan(name, "扫描未通过")
			} else {
				pg.reportScan(name, "无法启动扫描程序")
			}
		}
	}()
}

// scanArguments 展开参数模板：{file} 替换为文件路径；没有占位符时把文件
// 追加为最后一个参数。
func scanArguments(template, file string) []string {
	fields := strings.Fields(template)
	args := make([]string, 0, len(fields)+1)
	replaced := false
	for _, field := range fields {
		if field == "{file}" {
			args = append(args, file)
			replaced = true
			continue
		}
		args = append(args, field)
	}
	if !replaced {
		args = append(args, file)
	}
	return args
}

// reportScan 把扫描异常回显到状态栏并弹出一条通知；通过时不打扰用户。
func (pg *mainPage) reportScan(name, reason string) {
	message := tr(reason) + tr("：") + completionName(name)
	if pg.win == nil {
		return
	}
	pg.win.Update(func() {
		select {
		case <-pg.stop:
			return
		default:
			pg.flash(message)
		}
	})
	_ = mygo.NewNotification(mygo.NotificationOptions{
		ID:    scanNotifyID,
		Title: tr("下载后扫描"),
		Body:  message,
	}).Show()
}
