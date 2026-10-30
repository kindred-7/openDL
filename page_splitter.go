package main

import "github.com/egoist/mygo/ui"

// A quiet one-pixel divider with a larger input target. Only the centered
// rounded grip highlights; the full panel edge never turns into an accent.
func panelSplit(c *ui.Context, p *d1Palette, key string, size *float32, lower, upper float32, first, second func(), changed func()) ui.Element {
	return resizablePanelSplit(c, p, key, size, lower, upper, false, false, 1, first, second, changed)
}

func collapsiblePanelSplit(c *ui.Context, p *d1Palette, key string, size *float32, lower, upper, reveal float32, first, second func(), changed func()) ui.Element {
	return resizablePanelSplit(c, p, key, size, lower, upper, false, true, reveal, first, second, changed)
}

func panelSplitVertical(c *ui.Context, p *d1Palette, key string, size *float32, lower, upper float32, first, second func(), changed func()) ui.Element {
	return resizablePanelSplit(c, p, key, size, lower, upper, true, false, 1, first, second, changed)
}

func resizablePanelSplit(c *ui.Context, p *d1Palette, key string, size *float32, lower, upper float32, vertical, collapsible bool, reveal float32, first, second func(), changed func()) ui.Element {
	c.Key(key)
	var root ui.Element
	if vertical {
		root = ui.Column(c)
	} else {
		root = ui.Row(c)
	}
	root.AlignItems(ui.Stretch)
	set := func(value float32) {
		value = clampPanel(value, lower, upper)
		if value != *size {
			*size = value
			if changed != nil {
				changed()
			}
		}
	}
	root.Children(func() {
		firstPane := ui.Column(c).Shrink(0)
		if vertical {
			firstPane.Height(*size).FillWidth()
		} else {
			firstPane.Width(*size * reveal).FillHeight()
		}
		if collapsible {
			firstPane.Clip().Children(func() {
				if reveal > 0 {
					ui.Column(c).Width(*size).FillHeight().Shrink(0).Margin(0, 0, 0, *size*(reveal-1)).Children(first)
				}
			})
		} else {
			firstPane.Children(first)
		}
		divider := ui.Box(c).Shrink(0).Background(p.bd).Focusable().FocusRing(false).Role(ui.RoleSplitter)
		back, forth := ui.KeyLeft, ui.KeyRight
		if vertical {
			divider.Height(1).FillWidth().Label(tr("调整底部面板高度"))
			back, forth = ui.KeyUp, ui.KeyDown
		} else {
			dividerWidth := float32(1)
			if collapsible {
				dividerWidth = reveal
			}
			divider.Width(dividerWidth).FillHeight().Label(tr("调整侧栏宽度")).Disabled(collapsible && reveal < 1)
		}
		divider.OnShortcut(0, back, func() { set(*size - 8) }).OnShortcut(0, forth, func() { set(*size + 8) })
		secondPane := ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).MinHeight(0)
		if vertical {
			secondPane.FillWidth()
		} else {
			secondPane.FillHeight()
		}
		secondPane.Children(second)
		if !collapsible || reveal >= 1 {
			c.Key("resize-target")
			handle := ui.Box(c).Absolute()
			if vertical {
				handle.Left(0).Top(*size - 4).FillWidth().Height(9).Cursor(ui.CursorResizeNS)
			} else {
				handle.Left(*size - 4).Top(0).Width(9).FillHeight().Cursor(ui.CursorResizeEW)
			}
			dx, dy, held := handle.Dragged()
			delta := dx
			if vertical {
				delta = dy
			}
			if held {
				divider.Focus()
				if delta != 0 {
					set(*size + delta)
				}
			}
			active := held || divider.FocusVisible()
			if active || handle.Hovered() {
				color := p.bd2
				if active {
					color = p.mu
				}
				handle.Draw(func(painter *ui.Painter, r ui.Rect) {
					width, height := float32(4), min(float32(64), max(float32(0), r.H-16))
					if vertical {
						width, height = min(float32(64), max(float32(0), r.W-16)), 4
					}
					painter.Fill(ui.Rect{X: r.X + (r.W-width)/2, Y: r.Y + (r.H-height)/2, W: width, H: height}, color, 2)
				})
			}
		}
	})
	return root
}
