package main

// What the menu lists and what each row does, kept free of X11 and of the
// system services so it is tested with neither.

import (
	"fmt"

	"github.com/mpdroog/osxflow/internal/backlight"
	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/upower"
)

// view is everything the menu and the icon show.
type view struct {
	battery    upower.Battery
	hasBattery bool

	// screen and keyboard are nil when the machine has no such light.
	screen, keyboard *backlight.Device

	presentation bool
}

// actions is what the menu's rows do.
type actions interface {
	setScreen(value float64, final bool)
	setKeyboard(value float64, final bool)
	setPresentation(on bool)
	settings()
}

func buildRows(v *view, act actions) []menu.Row {
	rows := make([]menu.Row, 0, 14)
	if v.hasBattery {
		b := &v.battery
		rows = append(rows, menu.Row{Kind: menu.Header, Label: "Battery", Detail: fmt.Sprintf("%.0f%%", b.Percentage)})
		if remaining := upower.Remaining(b); remaining != "" {
			rows = append(rows, menu.Row{Kind: menu.Note, Label: remaining})
		}
		source := "Power source: Battery"
		if !b.OnBattery {
			source = "Power source: Power adapter"
		}
		rows = append(rows, menu.Row{Kind: menu.Note, Label: source})
		if health := upower.Health(b); health != "" {
			rows = append(rows, menu.Row{Kind: menu.Note, Label: health})
		}
	} else {
		rows = append(rows, menu.Row{Kind: menu.Header, Label: "Battery"}, menu.Row{Kind: menu.Note, Label: "No battery found"})
	}

	if v.screen != nil || v.keyboard != nil {
		rows = append(rows, menu.Row{Kind: menu.Separator})
	}
	if v.screen != nil {
		rows = append(rows,
			menu.Row{Kind: menu.Section, Label: "Display"},
			menu.Row{Kind: menu.Slider, Glyph: menu.Symbol(glyph.Sun), Value: v.screen.Fraction(), Slide: act.setScreen})
	}
	if v.keyboard != nil {
		rows = append(rows,
			menu.Row{Kind: menu.Section, Label: "Keyboard"},
			menu.Row{Kind: menu.Slider, Glyph: menu.Symbol(glyph.KeyboardLight), Value: v.keyboard.Fraction(), Slide: act.setKeyboard})
	}

	on := v.presentation
	return append(rows,
		menu.Row{Kind: menu.Separator},
		menu.Row{
			Kind:     menu.Item,
			Label:    "Presentation mode",
			On:       on,
			Glyph:    menu.Symbol(glyph.Presentation),
			Trailing: menu.TrailingSwitch,
			KeepOpen: true,
			Click:    func() { act.setPresentation(!on) },
		},
		menu.Row{Kind: menu.Separator},
		menu.Row{Kind: menu.Action, Label: "Power Settings…", Click: act.settings},
	)
}
