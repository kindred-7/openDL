package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"time"

	"openDL/download"

	"github.com/egoist/mygo"
)

// trayIconSize 是托盘图标边长；mygo 建议 32px PNG（Retina 16pt）。
const trayIconSize = 32

// setupTray 在通知区域放置图标；启用期间关闭主窗口驻留托盘。
func (pg *mainPage) setupTray() error {
	if pg.tray != nil {
		return nil
	}
	if !mygo.App.IsReady() {
		return fmt.Errorf(tr("系统托盘需要桌面应用"))
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: appBrand, Icon: trayIconPNG(appIconPNG, trayIconSize), ToolTip: appBrand})
	if err != nil {
		return fmt.Errorf(tr("无法创建托盘图标：%w"), err)
	}
	if runtime.GOOS == "linux" {
		// AppIndicator 只支持菜单，操作由菜单承担。
		tray.SetMenu(pg.trayMenu())
	} else {
		// 不预装菜单，左键点击直达主窗口；右键时临时挂载菜单弹出。
		tray.OnClick(pg.showMainWindow)
		tray.OnRightClick(func() {
			tray.SetMenu(pg.trayMenu())
			defer tray.SetMenu(nil)
			tray.PopUpMenu()
		})
	}
	pg.tray = tray
	return nil
}

// trayStatusWatch 把进行中数量与总速率写入托盘悬停提示；空闲时保持纯品牌
// 名。托盘可能延后创建或已拆除，每次检查后再写；读取任务状态只在界面线程。
func (pg *mainPage) trayStatusWatch() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		if pg.win != nil {
			pg.win.Update(func() {
				tray := pg.tray
				if tray == nil {
					return
				}
				running := 0
				var speed int64
				for _, r := range pg.allRows() {
					if r.pending {
						continue
					}
					speed += r.snap.Speed
					if r.snap.State == download.Running {
						running++
					}
				}
				tip := appBrand
				if running > 0 {
					tip = appBrand + " " + fmt.Sprintf(tr("%d 个下载中"), running) + "，" + bytesText(speed) + "/s"
				}
				tray.SetToolTip(tip)
			})
		}
		select {
		case <-pg.stop:
			return
		case <-t.C:
		}
	}
}

func (pg *mainPage) trayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: tr("显示主窗口"), Click: func(*mygo.MenuItem, *mygo.Window) { pg.showMainWindow() }},
		mygo.Separator(),
		{Label: tr("全部开始"), Click: func(*mygo.MenuItem, *mygo.Window) { pg.startAllTasks() }},
		{Label: tr("全部暂停"), Click: func(*mygo.MenuItem, *mygo.Window) { pg.pauseAllTasks() }},
		mygo.Separator(),
		{Label: tr("退出"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
}

// teardownTray 移除托盘图标，关闭主窗口恢复为直接退出应用。
func (pg *mainPage) teardownTray() {
	if pg.tray == nil {
		return
	}
	pg.tray.Destroy()
	pg.tray = nil
}

// showMainWindow 打开并聚焦主窗口。
func (pg *mainPage) showMainWindow() {
	if pg.win == nil {
		return
	}
	pg.win.Show()
	pg.win.Restore()
	pg.win.Focus()
}

// closeToBackground 报告关闭主窗口是否驻留后台：关闭时最小化到托盘开启，
// 且托盘入口可用；入口丢失时当场补建，保证后台始终可达。
func (pg *mainPage) closeToBackground() bool {
	if pg.quitting || !pg.preferences.MinTrayClose {
		return false
	}
	if pg.tray == nil {
		if err := pg.setupTray(); err != nil {
			return false
		}
	}
	return true
}

// trayIconPNG 把应用图标缩到托盘尺寸，面积平均保留圆角与字形边缘。
func trayIconPNG(src []byte, size int) []byte {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	bounds := img.Bounds()
	visible := image.Rectangle{}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := img.At(x, y).RGBA()
			if alpha >= 16*257 {
				pixel := image.Rect(x, y, x+1, y+1)
				if visible.Empty() {
					visible = pixel
				} else {
					visible = visible.Union(pixel)
				}
			}
		}
	}
	if !visible.Empty() && max(visible.Dx(), visible.Dy()) <= min(bounds.Dx(), bounds.Dy()) {
		// 保持源图比例，用可见边界的长边构造方形取样区域。
		side := max(visible.Dx(), visible.Dy())
		center := visible.Min.Add(visible.Max)
		left, top := (center.X-side)/2, (center.Y-side)/2
		left = max(bounds.Min.X, min(left, bounds.Max.X-side))
		top = max(bounds.Min.Y, min(top, bounds.Max.Y-side))
		bounds = image.Rect(left, top, left+side, top+side)
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0 := bounds.Min.Y + y*bounds.Dy()/size
		y1 := max(bounds.Min.Y+(y+1)*bounds.Dy()/size, y0+1)
		for x := 0; x < size; x++ {
			x0 := bounds.Min.X + x*bounds.Dx()/size
			x1 := max(bounds.Min.X+(x+1)*bounds.Dx()/size, x0+1)
			var r, g, b, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					b += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(b / n >> 8), uint8(a / n >> 8)})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return src
	}
	return out.Bytes()
}
