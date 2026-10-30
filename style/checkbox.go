package style

import (
	"openDL/i18n"

	"github.com/egoist/mygo/ui"
)

// Checkbox keeps base-widget input/focus behavior while using Lucide for its
// mark, so legacy views do not reintroduce a second set of drawn glyphs.
func Checkbox(c *ui.Context, p *Palette, checked *bool, label string) ui.Element {
	e := ui.CheckboxBase(c, checked).Gap(8).AlignItems(ui.Center).Radius(4).Label(i18n.T(label))
	e.Children(func() {
		mark := ui.Box(c).Size(SizeCheckbox, SizeCheckbox).Radius(4).Border(1, p.Border).Center()
		if *checked {
			mark.Background(p.Primary).BorderColor(p.Primary).Children(func() {
				Icon(c, IconCheck, 11, p.OnPrimary)
			})
		}
		if label != "" {
			ui.Text(c, i18n.T(label)).FontSize(TextBase).TextColor(p.Text)
		}
	})
	return e
}
