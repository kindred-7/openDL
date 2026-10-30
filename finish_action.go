package main

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 全部完成后动作：所有队列清空后执行一次（如关闭计算机）。
// 只在观察到完成事件后触发；手动暂停或取消让队列清空时不触发，
// 与 IDM 的“手动停止则取消完成后动作”保持一致。

type finishActionTracker struct {
	mu    sync.Mutex
	fired bool
}

var finishAction finishActionTracker

// consider 由每次引擎状态变化后调用。idle 表示没有下载中或排队中的任务；
// 每次观察到活动任务都会重新武装，新一批下载完成后仍会触发。
func (f *finishActionTracker) consider(justCompleted, idle bool, fire func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !idle {
		f.fired = false
		return
	}
	if justCompleted && !f.fired {
		f.fired = true
		fire()
	}
}

// queueFinished 在引擎线程上收到“队列已清空”，动作回到 UI 线程执行。
func (pg *mainPage) queueFinished() {
	if pg.win == nil {
		return
	}
	pg.win.Update(func() {
		select {
		case <-pg.stop:
			return
		default:
			pg.runFinishAction()
		}
	})
}

// runFinishAction 执行设置的完成后动作；未设置或不在重复日期内时什么
// 都不做。
func (pg *mainPage) runFinishAction() {
	if !nightDaysAllow(pg.preferences.FinishDays, time.Now()) {
		return
	}
	switch pg.preferences.FinishAction {
	case "shutdown":
		if err := requestShutdown(); err != nil {
			pg.flash(tr("关闭计算机失败：") + err.Error())
			return
		}
		pg.flash(tr("已请求 60 秒后关闭计算机"))
	case "sleep":
		if err := requestSleep(); err != nil {
			pg.flash(tr("进入睡眠失败：") + err.Error())
		}
	case "run":
		program := strings.TrimSpace(pg.preferences.FinishProgram)
		if program == "" {
			pg.flash(tr("未配置完成后运行的程序"))
			return
		}
		if err := launchFinishProgram(program, pg.preferences.FinishArgs); err != nil {
			pg.flash(tr("运行程序失败：") + err.Error())
		}
	}
}

// requestShutdown 请求系统关机。Windows 留 60 秒反悔窗口（shutdown /a 可
// 取消）；其他平台交给系统的关机命令，失败原因回显到状态栏。
func requestShutdown() error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("shutdown.exe", "/s", "/t", "60")
	} else {
		cmd = exec.Command("systemctl", "poweroff")
	}
	return cmd.Start()
}

// requestSleep 让系统进入睡眠；与关机一样按平台尽力而为。
func requestSleep() error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32.exe", "powrprof.dll,SetSuspendState", "0,1,0")
	} else {
		cmd = exec.Command("systemctl", "suspend")
	}
	return cmd.Start()
}

// launchFinishProgram 启动完成后动作配置的程序，不等待它退出。参数以空格
// 分隔。
func launchFinishProgram(program, args string) error {
	return exec.Command(program, strings.Fields(args)...).Start()
}
