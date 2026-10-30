package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

func shortcutFromKey(mods ui.Modifiers, key ui.Key) (string, string) {
	if mods & ^(ui.Ctrl|ui.Alt|ui.Shift) != 0 || mods&ui.Ctrl != 0 && mods&ui.Alt != 0 {
		return "", "该组合可能被系统或输入法占用，请选择其他快捷键"
	}
	name := ""
	if key >= ui.KeyA && key <= ui.KeyZ {
		name = string(rune('A' + key - ui.KeyA))
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		name = string(rune('0' + key - ui.Key0))
	}
	keys := map[ui.Key]string{ui.KeyEnter: "Enter", ui.KeyEscape: "Esc", ui.KeyDelete: "Delete", ui.KeyF1: "F1", ui.KeyF2: "F2", ui.KeyF3: "F3", ui.KeyF4: "F4", ui.KeyF5: "F5", ui.KeyF6: "F6", ui.KeyF7: "F7", ui.KeyF8: "F8", ui.KeyF9: "F9", ui.KeyF10: "F10", ui.KeyF11: "F11", ui.KeyF12: "F12"}
	if v, ok := keys[key]; ok {
		name = v
	}
	if name == "" {
		return "", "不支持此按键，请使用字母、数字、功能键、Enter 或 Delete"
	}
	if mods == 0 && key != ui.KeyEnter && key != ui.KeyDelete && key != ui.KeyEscape && (key < ui.KeyF1 || key > ui.KeyF12) {
		return "", "请同时按住 Ctrl 或 Alt 等修饰键"
	}
	if mods == ui.Shift && key != ui.KeyF10 {
		return "", "请同时按住 Ctrl 或 Alt"
	}
	if key == ui.KeyEscape && mods&ui.Ctrl != 0 {
		return "", "该组合由系统保留"
	}
	if mods&ui.Alt != 0 && (key == ui.KeyF4 || key == ui.KeyEnter || key == ui.KeyEscape) && !(mods == ui.Alt && key == ui.KeyEnter) {
		return "", "该组合由系统保留"
	}
	var parts []string
	if mods&ui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	return strings.Join(append(parts, name), " "), ""
}

func parseShortcut(value string) (ui.Modifiers, ui.Key, bool) {
	parts := strings.Fields(strings.ReplaceAll(value, "+", " "))
	var mods ui.Modifiers
	if len(parts) == 0 {
		return 0, 0, false
	}
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(p) {
		case "ctrl":
			mods |= ui.Ctrl
		case "alt":
			mods |= ui.Alt
		case "shift":
			mods |= ui.Shift
		default:
			return 0, 0, false
		}
	}
	name := strings.ToUpper(parts[len(parts)-1])
	var key ui.Key
	if len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z' {
		key = ui.KeyA + ui.Key(name[0]-'A')
	} else if len(name) == 1 && name[0] >= '0' && name[0] <= '9' {
		key = ui.Key0 + ui.Key(name[0]-'0')
	} else {
		key = map[string]ui.Key{"ENTER": ui.KeyEnter, "ESC": ui.KeyEscape, "DELETE": ui.KeyDelete, "F1": ui.KeyF1, "F2": ui.KeyF2, "F3": ui.KeyF3, "F4": ui.KeyF4, "F5": ui.KeyF5, "F6": ui.KeyF6, "F7": ui.KeyF7, "F8": ui.KeyF8, "F9": ui.KeyF9, "F10": ui.KeyF10, "F11": ui.KeyF11, "F12": ui.KeyF12}[name]
	}
	if key == ui.KeyUnknown {
		return 0, 0, false
	}
	_, err := shortcutFromKey(mods, key)
	return mods, key, err == ""
}

func shortcutConflict(prefs appPreferences, id, value string) string {
	mods, key, ok := parseShortcut(value)
	if !ok {
		return "无效的快捷键"
	}
	if (id == "search" || id == "add") && mods == ui.Ctrl && (key == ui.KeyA || key == ui.KeyC || key == ui.KeyV || key == ui.KeyX || key == ui.KeyZ || key == ui.KeyY) {
		return "该组合用于文本编辑，请选择其他快捷键"
	}
	if (mods == 0 && key == ui.KeyEscape) || (mods == ui.Alt && key == ui.KeyEnter) || (mods == ui.Shift && key == ui.KeyF10) {
		return "该组合已用于界面操作"
	}
	for _, item := range shortcutDefinitions {
		if item.id != id {
			m, k, valid := parseShortcut(shortcutText(prefs, item.id))
			if valid && m == mods && k == key {
				return "快捷键已用于：" + tr(item.title)
			}
		}
	}
	return ""
}

func (pg *mainPage) shortcut(id string) (ui.Modifiers, ui.Key) {
	mods, key, ok := parseShortcut(shortcutText(pg.preferences, id))
	if !ok {
		for _, d := range shortcutDefinitions {
			if d.id == id {
				mods, key, _ = parseShortcut(d.keys)
				break
			}
		}
	}
	return mods, key
}
func (pg *mainPage) shortcutPressed(c *ui.Context, id string) bool {
	m, k := pg.shortcut(id)
	return c.Shortcut(m, k)
}
func (pg *mainPage) taskShortcut(c *ui.Context, id string, fn func()) {
	m, k := pg.shortcut(id)
	c.OnShortcut(m, k, fn)
}
func (pg *mainPage) menuShortcut(c *ui.Context, id string, fn func()) {
	m, k := pg.shortcut(id)
	c.OnShortcut(m, k, fn)
}
