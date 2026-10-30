// Package style carries the app's design tokens: the shadcn/ui "nova" look
// built on Radix color scales, mapped onto MyGo's Theme so every widget
// picks up the palette, radius and spacing. Light() and Dark() hold the two
// Radix scales; the app defaults to light.
package style

import "github.com/egoist/mygo/ui"

// Palette is the full token set one theme paints with. It carries a few
// tokens (faint text, card vs surface) beyond MyGo's Theme, and builds the
// Theme from itself with Theme().
type Palette struct {
	Dark bool

	Background, Card, Surface2, Surface3 Color
	Border, BorderSoft                   Color

	Text, TextSoft, TextFaint Color

	Primary, PrimaryHover, PrimaryActive, OnPrimary Color

	Success, Warning, Danger Color
	Focus, Selection         Color
	Inverse, InverseText     Color
}

// shorthand for readability inside the token tables.
type Color = ui.Color

// Colors are compatibility projections of Rules; do not add a second palette.

// Light returns the Radix light scales: white cards on slate1, slate12
// text, violet9 primary. This is the default theme.
func Light() *Palette { return paletteFromRules(false) }
func Dark() *Palette  { return paletteFromRules(true) }

// Compatibility adapter: legacy views consume the same semantic tokens.
func paletteFromRules(dark bool) *Palette {
	c := NewRules(dark).Colors
	return &Palette{Dark: dark, Background: c.Page, Card: c.Bg, Surface2: c.Hover, Surface3: c.Pressed,
		Border: c.Border, BorderSoft: c.Border, Text: c.Fg, TextSoft: c.Muted, TextFaint: c.Faint,
		Primary: c.Primary, PrimaryHover: c.PrimaryHover, PrimaryActive: c.PrimaryPressed, OnPrimary: c.OnPrimary,
		Success: c.Success, Warning: c.Warning, Danger: c.Danger, Focus: c.Focus, Selection: c.Selection,
		Inverse: c.Fg, InverseText: c.Bg}
}

// Dashboard accents from the reference design: soft badge pairs, chart
// colors and the detail-panel icon tint.
var (
	BadgeBlueBg   = ui.Hex("#E8F0FE")
	BadgeBlueFg   = ui.Hex("#2563EB")
	BadgeGreenBg  = ui.Hex("#E6F7EC")
	BadgeGreenFg  = ui.Hex("#16A34A")
	BadgeOrangeBg = ui.Hex("#FDF0E2")
	BadgeOrangeFg = ui.Hex("#D97706")
	BadgeYellowBg = ui.Hex("#FBF7E3")
	BadgeYellowFg = ui.Hex("#CA8A04")
	DetailIconBg  = ui.Hex("#EDE9FE")
	DetailIconFg  = ui.Hex("#7C3AED")
	ChartLine     = ui.Hex("#2563EB")
	ChartUp       = ui.Hex("#22C55E")
	Danger        = ui.Hex("#DC2626")
)

// Metrics in the shadcn spirit: 4px spacing base, 8px radius, 13px text.
const (
	radius     = 8
	spacing    = 4
	fontSize   = 13
	scrollbarW = 6
)

// Theme builds the MyGo theme this palette paints the widgets with.
func (p *Palette) Theme() *ui.Theme {
	return &ui.Theme{
		Dark:           p.Dark,
		Background:     p.Background,
		Surface:        p.Card,
		SurfaceHover:   p.Surface2,
		SurfacePressed: p.Surface3,
		Border:         p.Border,
		Text:           p.Text,
		TextMuted:      p.TextSoft,
		Accent:         p.Primary,
		AccentHover:    p.PrimaryHover,
		AccentPressed:  p.PrimaryActive,
		AccentText:     p.OnPrimary,
		Danger:         p.Danger,
		Warning:        p.Warning,
		Success:        p.Success,
		Selection:      p.Selection,
		Focus:          p.Focus,
		Inverse:        p.Inverse,
		InverseText:    p.InverseText,
		Scrollbar:      p.Surface3,
		ScrollbarWidth: scrollbarW,
		Radius:         radius,
		Spacing:        spacing,
		FontSize:       fontSize,
		Font:           FontFamily,
	}
}
