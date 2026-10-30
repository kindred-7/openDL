package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"

	"openDL/style"

	"github.com/egoist/mygo/ui"
)

type panelLayout struct {
	Left             float32             `json:"sidebar_width"`
	SidebarCollapsed bool                `json:"sidebar_collapsed,omitempty"`
	Right            float32             `json:"detail_width"`
	Bottom           float32             `json:"detail_height"`
	Columns          []taskColumnSetting `json:"columns,omitempty"`
	Version          int                 `json:"version,omitempty"`
}

func (p *panelLayout) defaults() {
	p.ensureColumns()
	for _, item := range []struct {
		value    *float32
		fallback float32
	}{
		{&p.Left, style.WidthSidebar}, {&p.Right, 360}, {&p.Bottom, 260},
	} {
		if *item.value <= 0 || math.IsNaN(float64(*item.value)) || math.IsInf(float64(*item.value), 0) {
			*item.value = item.fallback
		}
	}
}

func clampPanel(value, lower, upper float32) float32 {
	return max(lower, min(value, max(lower, upper)))
}

func panelLayoutPath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "opendl", "panel-layout.json"), err
}

func loadPanelLayout() panelLayout {
	var value panelLayout
	if path, err := panelLayoutPath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &value) != nil {
				value = panelLayout{}
			}
		}
	}
	value.defaults()
	return value
}

func (pg *mainPage) contentWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	sidebar := pg.panes.Left * pg.sidebarReveal
	divider := pg.sidebarReveal
	return max(float32(320), w-sidebar-divider)
}

func (pg *mainPage) taskViewportWidth(c *ui.Context) float32 {
	if pg.rightDetailOpen() {
		return pg.tablePaneWidth
	}
	return pg.contentWidth(c)
}

func (pg *mainPage) tableAreaHeight(c *ui.Context) float32 {
	_, h := c.Size()
	title := c.TitleBar().Height
	if title <= 0 {
		title = style.HeightTitleBar
	}
	return max(float32(300), h-title-style.HeightStatusBar-style.HeightToolbar)
}

// Divider sizes live in persistent fields. Rebase the first pane only when its
// containing area or dock position changes, preserving an ongoing resize.
func (pg *mainPage) preparePanelLayout(c *ui.Context) {
	w, _ := c.Size()
	pg.panes.Left = clampPanel(pg.panes.Left, 180, min(float32(480), w-480))
	width, height := pg.contentWidth(c), pg.tableAreaHeight(c)
	pg.panes.Right = clampPanel(pg.panes.Right, 280, width-321)
	pg.panes.Bottom = clampPanel(pg.panes.Bottom, 140, height-151)
	right, bottom := pg.rightDetailOpen(), !pg.detailCollapsed && pg.detailPlacement != "right"
	if pg.layoutW != width || pg.layoutRight != right {
		pg.tablePaneWidth = width - pg.panes.Right - 1
	}
	if pg.layoutH != height || pg.layoutBottom != bottom {
		pg.tablePaneHeight = height - pg.panes.Bottom - 1
	}
	pg.layoutW, pg.layoutH = width, height
	pg.layoutRight, pg.layoutBottom = right, bottom
}

func (pg *mainPage) resetPanelLayout() {
	pg.panes = panelLayout{Columns: pg.panes.Columns, SidebarCollapsed: pg.panes.SidebarCollapsed, Version: pg.panes.Version}
	pg.panes.defaults()
	pg.layoutW, pg.layoutH = 0, 0
	pg.panelLayoutChanged()
}

func (pg *mainPage) panelLayoutChanged() {
	if pg.layoutWrites == nil {
		return
	}
	snapshot := pg.panes
	snapshot.Columns = append([]taskColumnSetting(nil), pg.panes.Columns...)
	// Keep only the latest snapshot; resizing never waits for disk I/O.
	select {
	case pg.layoutWrites <- snapshot:
	default:
		select {
		case <-pg.layoutWrites:
		default:
		}
		select {
		case pg.layoutWrites <- snapshot:
		default:
		}
	}
}

func writePanelLayout(value panelLayout) error {
	path, err := panelLayoutPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (pg *mainPage) panelLayoutWatch() {
	defer close(pg.layoutDone)
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	var latest panelLayout
	dirty := false
	flush := func() {
		if !dirty {
			return
		}
		dirty = false
		if err := writePanelLayout(latest); err != nil {
			pg.win.Update(func() {
				select {
				case <-pg.stop:
				default:
					pg.flash(tr("无法保存面板尺寸：") + err.Error())
				}
			})
		}
	}
	for {
		select {
		case latest = <-pg.layoutWrites:
			dirty = true
		case <-ticker.C:
			flush()
		case <-pg.stop:
			select {
			case latest = <-pg.layoutWrites:
				dirty = true
			default:
			}
			flush()
			return
		}
	}
}
