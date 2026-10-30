package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	controlMotion = 120 * time.Millisecond
	selectMotion  = 160 * time.Millisecond
	segmentMotion = 220 * time.Millisecond
)

// segmentedMotion reports a selection surface's animated position and width.
type segmentedMotion struct {
	x, width float32
}

func motionTarget(active bool) float32 {
	if active {
		return 1
	}
	return 0
}

// segmentedSlide paints a selection surface behind equal-width items.
// The surface widens briefly for a neighboring move and settles on arrival;
// farther moves retain their resting width and glide without stretching.
func segmentedSlide(container ui.Element, selected, count int, inset, gap, radius float32, color ui.Color) segmentedMotion {
	if count <= 0 {
		return segmentedMotion{}
	}
	last := ui.Local(container, "segment-selected", func() int { return selected })
	leftEase, rightEase := ui.EaseInOut, ui.EaseInOut
	if selected == *last+1 {
		leftEase, rightEase = ui.EaseIn, ui.EaseOut
	} else if selected == *last-1 {
		leftEase, rightEase = ui.EaseOut, ui.EaseIn
	}
	*last = selected
	left := container.AnimateTo("segment-left", float32(selected), ui.Animation{Duration: segmentMotion, Ease: leftEase})
	right := container.AnimateTo("segment-right", float32(selected), ui.Animation{Duration: segmentMotion, Ease: rightEase})
	position := (left + right) / 2
	container.Draw(func(painter *ui.Painter, bounds ui.Rect) {
		stepWidth := max(float32(0), (bounds.W-2*inset-gap*float32(count-1))/float32(count))
		height := max(float32(0), bounds.H-2*inset)
		pitch := stepWidth + gap
		start := bounds.X + inset + min(left, right)*pitch
		width := stepWidth + (max(left, right)-min(left, right))*pitch
		painter.Fill(ui.Rect{X: start, Y: bounds.Y + inset, W: width, H: height}, color, radius)
	})
	return segmentedMotion{x: position, width: 1 + max(left, right) - min(left, right)}
}

type navSlide struct {
	y, height float32
	valid     bool
}

type navGroup struct{ first, last int }

type navHoverState struct {
	index int
	y, h  float32
	group int
	// Bounds come from the previous committed frame, so a group switch
	// cannot draw the next shape until that frame has been committed.
	pending bool
	valid   bool
	// ghost 是跨组跳变时留在原地淡出的上一组色块。
	ghostY, ghostH float32
	ghostValid     bool
	// liveA 交替指定哪个通道当前承载淡入的色块。
	liveA bool
}

// Bounds are from the last committed frame; handles remain valid across builds.
func navSlidePosition(c *ui.Context, list ui.Element, listHandle, selectedHandle *ui.Handle) navSlide {
	if selectedHandle == nil {
		return navSlide{}
	}
	listBounds, selectedBounds := listHandle.Bounds(c), selectedHandle.Bounds(c)
	requested := ui.Local(list, "nav-bounds-frame", func() bool { return false })
	if listBounds.H <= 0 || selectedBounds.H <= 0 {
		if !*requested {
			*requested = true
			c.AnimationFrame()
		}
		return navSlide{}
	}
	*requested = false
	y := list.Animate("nav-selection-position", selectedBounds.Y-listBounds.Y, 220*time.Millisecond)
	return navSlide{y: listBounds.Y + y, height: selectedBounds.H, valid: true}
}

// Paint the moving surfaces behind the rows, clipped to whole groups rather
// than individual rows so their 2px gaps never cut the travelling shape.
func navSurfaces(c *ui.Context, list ui.Element, listHandle *ui.Handle, rows []ui.Handle, groups []navGroup, p *d1Palette, slide navSlide) {
	bounds := listHandle.Bounds(c)
	x, y, over := list.PointerPosition()
	// PointerPosition is geometric and can include points covered by a popup.
	// Hovered follows the hit chain, so covered navigation does not light up.
	over = over && list.Hovered()
	state := ui.Local(list, "nav-hover-state", func() navHoverState { return navHoverState{index: -1, group: -1} })
	rowBounds := make([]ui.Rect, len(rows))
	hovered := -1
	for i := range rows {
		rowBounds[i] = rows[i].Bounds(c)
		r := rowBounds[i]
		if over && r.W > 0 && r.H > 0 && bounds.X+x >= r.X && bounds.X+x < r.X+r.W && bounds.Y+y >= r.Y && bounds.Y+y < r.Y+r.H {
			hovered = i
		}
	}
	groupRects := make([]ui.Rect, 0, len(groups))
	groupOf := func(index int) int {
		for gi, group := range groups {
			if index >= group.first && index <= group.last {
				return gi
			}
		}
		return -1
	}
	for _, group := range groups {
		first, last := rowBounds[group.first], rowBounds[group.last]
		if first.H <= 0 || last.H <= 0 {
			continue
		}
		groupRects = append(groupRects, ui.Rect{X: 0, Y: first.Y - bounds.Y, W: bounds.W, H: last.Y + last.H - first.Y})
		// Keep the same target while the pointer crosses a gap inside a group.
		if hovered < 0 && over && state.valid && state.index >= group.first && state.index <= group.last &&
			bounds.Y+y >= first.Y && bounds.Y+y < last.Y+last.H {
			hovered = state.index
		}
	}
	previousY, previousH := state.y, state.h
	if hovered >= 0 {
		r := rowBounds[hovered]
		state.pending = true
		if r.H > 0 {
			// 跨组跳变：上一组色块留在原地淡出，新色块在新位置淡入。
			if next := groupOf(hovered); state.valid && state.group >= 0 && next >= 0 && next != state.group {
				state.ghostY, state.ghostH, state.ghostValid = previousY, previousH, previousH > 0
				state.liveA = !state.liveA
			}
			state.index, state.y, state.h, state.valid, state.group = hovered, r.Y-bounds.Y, r.H, true, groupOf(hovered)
			state.pending = false
		}
	}
	// 两个通道交替承载淡入的当前色块与淡出的旧色块：跨组时对调目标，新块
	// 0→1、旧块 1→0 同时进行，两者半透明重叠。ReduceMotion 时直接到位。
	liveKey, ghostKey := "nav-hover-fade-b", "nav-hover-fade-a"
	if state.liveA {
		liveKey, ghostKey = "nav-hover-fade-a", "nav-hover-fade-b"
	}
	live := list.Animate(liveKey, motionTarget(hovered >= 0), controlMotion)
	ghost := list.Animate(ghostKey, 0, controlMotion)
	if state.pending {
		c.AnimationFrame()
	}
	hoverY := state.y
	if state.valid {
		hoverY = list.Animate("nav-hover-position", state.y, 180*time.Millisecond)
	}
	// 跨组淡入淡出期间新色块直接落在新位置，不与旧色块一起滑动；位置通道
	// 继续跟踪，淡出结束时不会跳变。
	crossing := state.ghostValid && ghost > 0.001
	if crossing {
		hoverY = state.y
	}
	selectedY := slide.y - bounds.Y
	list.Draw(func(painter *ui.Painter, r ui.Rect) {
		for _, group := range groupRects {
			clip := ui.Rect{X: r.X + group.X, Y: r.Y + group.Y, W: group.W, H: group.H}
			painter.Clip(clip, 0, func() {
				if state.ghostValid && state.ghostH > 0 && ghost > 0.001 {
					painter.Fill(ui.Rect{X: r.X, Y: r.Y + state.ghostY, W: r.W, H: state.ghostH}, p.hover.Alpha(ghost), 6)
				}
				if state.valid && live > 0.001 {
					painter.Fill(ui.Rect{X: r.X, Y: r.Y + hoverY, W: r.W, H: state.h}, p.hover.Alpha(live), 6)
				}
				if slide.valid {
					painter.Fill(ui.Rect{X: r.X, Y: r.Y + selectedY, W: r.W, H: slide.height}, p.sel, 6)
				}
			})
		}
	})
}

func navMotion(row ui.Element, p *d1Palette, selected bool, slide navSlide) {
	if !slide.valid && selected {
		row.Background(p.sel)
	} else if !selected && row.FocusVisible() {
		row.Background(p.hover)
	}
}

func hoverMotion(e ui.Element, base, hover ui.Color) {
	amount := e.Animate("hover-surface", motionTarget(e.Hovered() || e.Pressed() || e.FocusVisible()), controlMotion)
	if base.A == 0 {
		e.Background(hover.Alpha(amount))
	} else {
		e.Background(base.Mix(hover, amount))
	}
}

func switchMotion(toggle ui.Element, p *d1Palette, on bool) (float32, ui.Color, ui.Color) {
	amount := toggle.Animate("switch-position", motionTarget(on), selectMotion)
	return 2 + 12*amount, p.bd2.Mix(p.pri, amount), ui.Hex("#FFFFFF").Mix(p.onpri, amount)
}
