package style

import (
	"bytes"
	"embed"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Lucide SVG sources are pinned and licensed in lucide/README.md.
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

// Browser marks come from Bootstrap Icons v1.13.1 (https://github.com/twbs/icons).
const (
	IconBrowserChrome  = "browser-chrome"
	IconBrowserEdge    = "browser-edge"
	IconBrowserFirefox = "browser-firefox"
)

//go:embed lucide/icons/*.svg lucide/LICENSE bootstrap-icons/icons/*.svg bootstrap-icons/LICENSE
var iconSources embed.FS

var iconCache sync.Map // canonical icon name -> immutable *ui.SVG

// Compatibility names used by existing controls, not a second icon family.
var lucideAliases = map[string]string{
	"share": "share-2", "sliders": "sliders-horizontal", "layout": "panel-left",
	"close": "x", "home": "house", "checkcircle": "circle-check", "pausecircle": "circle-pause",
	"disk": "hard-drive", "bolt": "zap", "chevron": "chevron-right", "dots": "ellipsis", "sort": "arrow-down-up",
	"stop": "square", "cog": "settings", "alert": "circle-alert", "music": "headphones",
	"zip": "file-archive", "box": "package", "disc": "disc-3", "down": "download",
	"chev": "chevron-down", "redo": "rotate-cw",
}

func iconSVG(name string) *ui.SVG {
	if canonical, ok := lucideAliases[name]; ok {
		name = canonical
	}
	if cached, ok := iconCache.Load(name); ok {
		return cached.(*ui.SVG)
	}
	dir := "lucide/icons/"
	switch name {
	case IconBrowserChrome, IconBrowserEdge, IconBrowserFirefox:
		dir = "bootstrap-icons/icons/"
	}
	source, err := iconSources.ReadFile(dir + name + ".svg")
	if err != nil {
		// Keep a visible fallback for missing glyphs.
		if name != IconFile {
			return iconSVG(IconFile)
		}
		panic(err) // Required embedded fallback is a source packaging error.
	}
	if dir == "lucide/icons/" {
		stroke := []byte(`stroke-width="` + strconv.FormatFloat(float64(StrokeIcon), 'f', -1, 32) + `"`)
		source = bytes.ReplaceAll(source, []byte(`stroke-width="2"`), stroke)
	}
	icon := ui.MustParseSVG(source)
	stored, _ := iconCache.LoadOrStore(name, icon)
	return stored.(*ui.SVG)
}

// Icon is the single entry point for application glyphs. Elements remain local
// to each build; only immutable parsed SVG data is shared between frames.
func Icon(c *ui.Context, name string, size float32, color ui.Color) ui.Element {
	return ui.Icon(c, iconSVG(name)).Size(size, size).TextColor(color)
}
