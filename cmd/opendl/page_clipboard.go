package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Clipboard state and services belong to the UI thread. The worker only
// schedules reads while the preference is enabled.
func (pg *mainPage) syncClipboardPrompt() {
	if !pg.preferences.ClipboardDownloadPrompt {
		if pg.clipboardPromptStop != nil {
			close(pg.clipboardPromptStop)
			pg.clipboardPromptStop = nil
		}
		return
	}
	if pg.clipboardPromptStop != nil || pg.stop == nil {
		return
	}
	pg.clipboardLast, pg.clipboardPrimed = "", false
	if pg.svc != nil {
		pg.clipboardLast, pg.clipboardPrimed = pg.svc.ReadClipboard(), true
	}
	stop := make(chan struct{})
	pg.clipboardPromptStop = stop
	win, appStop := pg.win, pg.stop
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-appStop:
				return
			case <-stop:
				return
			case <-ticker.C:
				win.Update(func() {
					if pg.clipboardPromptStop == stop && !pg.quitting && pg.svc != nil {
						pg.checkClipboardPrompt()
					}
				})
			}
		}
	}()
}

func (pg *mainPage) checkClipboardPrompt() {
	text := pg.svc.ReadClipboard()
	if !pg.clipboardPrimed {
		pg.clipboardLast, pg.clipboardPrimed = text, true
		return
	}
	if text == pg.clipboardLast {
		return
	}
	pg.clipboardLast = text
	if text == "" || pg.showAdd || len(pg.browserPromptWindows) != 0 {
		return
	}
	links := d1ParseLinks(text)
	if len(links) > 0 && clipboardIsLinks(text, links) {
		pg.dlgLinks = strings.Join(links, "\n")
		pg.openAdd(false)
	}
}

func (pg *mainPage) writeClipboard(services ui.Services, text string) {
	if pg.clipboardPromptStop != nil {
		pg.clipboardLast, pg.clipboardPrimed = text, true
	}
	services.WriteClipboard(text)
}

// Only prompt for a short list of standalone links, such as copied URLs.
func clipboardIsLinks(text string, links []string) bool {
	set := make(map[string]bool, len(links))
	for _, link := range links {
		set[link] = true
	}
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		if lines > 20 || !set[line] {
			return false
		}
	}
	return lines > 0
}
