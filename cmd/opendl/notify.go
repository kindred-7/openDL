package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// 下载完成通知：短时间内的多个完成事件合并为一条系统通知，点击回到主窗口。
// 提示音跟随系统通知设置，不再单独播放声音。

const (
	completionNotifyID    = "download-complete"
	completionNotifyDelay = 1500 * time.Millisecond
)

type completionNotifier struct {
	mu      sync.Mutex
	pending []string
	lastID  string // 最近一次完成的任务编号；通知点击后定位并清空
	timer   *time.Timer
}

var completeNotifier completionNotifier

// add 收集一个完成的文件名，并在短暂延迟后合并成一条通知。
func (n *completionNotifier) add(name, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending = append(n.pending, completionName(name))
	n.lastID = id
	if len(n.pending) > 64 {
		n.pending = n.pending[len(n.pending)-64:]
	}
	if n.timer == nil {
		n.timer = time.AfterFunc(completionNotifyDelay, n.flush)
	}
}

func (n *completionNotifier) flush() {
	n.mu.Lock()
	names := n.pending
	n.pending = nil
	n.timer = nil
	n.mu.Unlock()
	if len(names) == 0 || !mygo.App.IsReady() {
		return
	}
	// 固定 ID：上一条尚未处理时直接替换，不堆积通知。
	_ = mygo.NewNotification(mygo.NotificationOptions{
		ID:    completionNotifyID,
		Title: tr("下载完成"),
		Body:  completionBody(names),
	}).Show()
}

// takeLast 取走最近一次完成的任务编号，供通知点击后定位。
func (n *completionNotifier) takeLast() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	id := n.lastID
	n.lastID = ""
	return id
}

// completionName 限制单个文件名长度，保证通知正文一行可读。
func completionName(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return tr("未命名文件")
	}
	if len(runes) <= 48 {
		return string(runes)
	}
	return string(runes[:45]) + "..."
}

// completionBody 把完成的文件名合并成一行摘要，超出的以数量概括。
func completionBody(names []string) string {
	shown, suffix := names, ""
	if len(shown) > 3 {
		shown, suffix = shown[:3], fmt.Sprintf(tr(" 等 %d 个文件"), len(names))
	}
	return strings.Join(shown, tr("、")) + suffix
}

// setupNotifications 把通知点击路由到主窗口；应用回到前台时清理已读通知。
func (pg *mainPage) setupNotifications() {
	mygo.App.OnNotificationClick(func(id string) {
		if id == completionNotifyID {
			if taskID := completeNotifier.takeLast(); taskID != "" {
				pg.sel = taskID
			}
			pg.showMainWindow()
		}
	})
	mygo.App.OnDidBecomeActive(func() { mygo.ClearNotifications() })
}
