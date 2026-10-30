package style

import (
	"bytes"
	"embed"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Official Lucide SVG sources, pinned and licensed in lucide/README.md.
// Keep the existing constants so both native page implementations share one API.
const (
	IconDownload   = "download"
	IconRSS        = "rss"
	IconShare      = "share-2"
	IconMoon       = "moon"
	IconSliders    = "sliders-horizontal"
	IconSearch     = "search"
	IconPause      = "pause"
	IconPlay       = "play"
	IconPlus       = "plus"
	IconLayout     = "panel-left"
	IconFolder     = "folder"
	IconFolderOpen = "folder-open"
	IconClose      = "x"
	IconHome       = "house"
	IconCheck      = "check"
	IconCheckC     = "circle-check"
	IconPauseC     = "circle-pause"
	IconClock      = "clock"
	IconGlobe      = "globe"
	IconTrash      = "trash"
	IconDisk       = "hard-drive"
	IconSun        = "sun"
	IconBell       = "bell"
	IconLink       = "link"
	IconUpload     = "upload"
	IconBolt       = "zap"
	IconCopy       = "copy"
	IconChevron    = "chevron-right"
	IconDots       = "ellipsis"
	IconSort       = "arrow-down-up"
	IconList       = "list"
	IconFile       = "file"
	IconMinus      = "minus"
	IconArrowUp    = "arrow-up"
	IconArrowDown  = "arrow-down"
	IconStatus     = "circle-dot"
)

//go:embed lucide/icons/*.svg lucide/LICENSE
var lucideSources embed.FS

var lucideCache sync.Map // canonical Lucide name -> immutable *ui.SVG

// Compatibility names used by existing controls, not a second icon family.
var lucideAliases = map[string]string{
	"share": "share-2", "sliders": "sliders-horizontal", "layout": "panel-left",
	"close": "x", "home": "house", "checkcircle": "circle-check", "pausecircle": "circle-pause",
	"disk": "hard-drive", "bolt": "zap", "chevron": "chevron-right", "dots": "ellipsis", "sort": "arrow-down-up",
	"stop": "square", "cog": "settings", "alert": "circle-alert", "music": "headphones",
	"zip": "file-archive", "box": "package", "disc": "disc-3", "down": "download",
	"chev": "chevron-down", "redo": "rotate-cw",
}

func lucideSVG(name string) *ui.SVG {
	if canonical, ok := lucideAliases[name]; ok {
		name = canonical
	}
	if cached, ok := lucideCache.Load(name); ok {
		return cached.(*ui.SVG)
	}
	source, err := lucideSources.ReadFile("lucide/icons/" + name + ".svg")
	if err != nil {
		// A missing semantic match must still use Lucide, never a blank glyph.
		if name != IconFile {
			return lucideSVG(IconFile)
		}
		panic(err) // Required embedded fallback is a source packaging error.
	}
	stroke := []byte(`stroke-width="` + strconv.FormatFloat(float64(StrokeIcon), 'f', -1, 32) + `"`)
	source = bytes.ReplaceAll(source, []byte(`stroke-width="2"`), stroke)
	icon := ui.MustParseSVG(source)
	stored, _ := lucideCache.LoadOrStore(name, icon)
	return stored.(*ui.SVG)
}

// Icon is the single entry point for application glyphs. Elements remain local
// to each build; only immutable parsed SVG data is shared between frames.
func Icon(c *ui.Context, name string, size float32, color ui.Color) ui.Element {
	return ui.Icon(c, lucideSVG(name)).Size(size, size).TextColor(color)
}
