package main

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"openDL/download"

	"github.com/egoist/mygo/ui"
)

type fileIconRequest struct {
	key, path string
	actual    bool
}

const fileIconCacheLimit = 128

// The UI owns the cache, while one worker owns all Shell calls. Nil entries
// represent requests in flight or icons that are unavailable on this system.
func (pg *mainPage) taskFileIcon(r d1Row) *ui.Bitmap {
	if pg.fileIconRequests == nil {
		return nil
	}
	req := fileIconRequest{path: filepath.Ext(r.rowName())}
	req.key = "type:" + strings.ToLower(req.path)
	if !r.pending && r.snap.State == download.Done && r.snap.FilePath != "" {
		req.path, req.actual = r.snap.FilePath, true
		req.key = "file:" + strings.ToLower(filepath.Clean(req.path)) + ":" + strconv.FormatInt(r.snap.Finished.UnixNano(), 10)
	}
	if bitmap, found := pg.fileIcons[req.key]; found {
		return bitmap
	}
	select {
	case pg.fileIconRequests <- req:
		if len(pg.fileIconOrder) >= fileIconCacheLimit {
			delete(pg.fileIcons, pg.fileIconOrder[0])
			pg.fileIconOrder = pg.fileIconOrder[1:]
		}
		pg.fileIcons[req.key] = nil
		pg.fileIconOrder = append(pg.fileIconOrder, req.key)
	default:
		// Retry on the next frame rather than block scrolling on a full queue.
	}
	return nil
}

func (pg *mainPage) fileIconWatch() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cleanup, ok := initFileIconThread()
	if !ok {
		return
	}
	defer cleanup()
	for {
		select {
		case <-pg.stop:
			return
		case req := <-pg.fileIconRequests:
			select {
			case <-pg.stop:
				return
			default:
			}
			img := extractFileIcon(req.path, req.actual)
			var bitmap *ui.Bitmap
			if img != nil {
				bitmap = ui.NewBitmap(img)
			}
			pg.win.Update(func() {
				select {
				case <-pg.stop:
				default:
					if _, present := pg.fileIcons[req.key]; present {
						pg.fileIcons[req.key] = bitmap
					}
				}
			})
		}
	}
}
