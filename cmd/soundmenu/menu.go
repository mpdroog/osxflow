package main

// What the menu lists and what each row does, kept free of X11, the sound
// server and the session bus so it is tested with none of them.

import (
	"fmt"
	"image"
	"image/color"

	"github.com/mpdroog/osxflow/internal/audio"
	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/mpris"
)

// view is everything the menu, the icon and the overlay show.
type view struct {
	// connected reports whether the sound server can be reached.
	connected bool
	audio     audio.State

	// players is sorted with the one playing first; the menu shows only
	// that one.
	players []mpris.Player
}

// actions is what the menu's rows do.
type actions interface {
	setVolume(output bool, name string, value float64, final bool)
	setMute(output bool, name string, mute bool)
	setDefault(output bool, name string)
	playPause(bus string)
	next(bus string)
	previous(bus string)
	settings()
}

// maxDevices caps each device list: enough for built-in audio, a headset
// and a monitor or two.
const maxDevices = 6

func buildRows(v *view, act actions) []menu.Row {
	rows := make([]menu.Row, 0, 24)
	out, in := v.audio.DefaultOutput(), v.audio.DefaultInput()

	rows = append(rows, menu.Row{Kind: menu.Header, Label: "Sound", Detail: levelText(out)})
	switch {
	case !v.connected:
		rows = append(rows, menu.Row{Kind: menu.Note, Label: "Sound server not running"})
	case out == nil:
		rows = append(rows, menu.Row{Kind: menu.Note, Label: "No sound output"})
	default:
		name, muted := out.Name, out.Muted
		rows = append(rows,
			menu.Row{
				Kind:  menu.Slider,
				Glyph: speakerGlyph(out.Volume, muted),
				Value: out.Volume,
				Slide: func(value float64, final bool) { act.setVolume(true, name, value, final) },
			},
			menu.Row{
				Kind:     menu.Item,
				Label:    "Mute",
				On:       muted,
				Glyph:    speakerGlyph(0, true),
				Trailing: menu.TrailingSwitch,
				KeepOpen: true,
				Click:    func() { act.setMute(true, name, !muted) },
			})
	}

	if v.connected && len(v.audio.Outputs) > 1 {
		rows = append(rows, menu.Row{Kind: menu.Separator}, menu.Row{Kind: menu.Section, Label: "Output"})
		rows = append(rows, deviceRows(v.audio.Outputs, true, act)...)
	}

	if v.connected && in != nil {
		name, muted := in.Name, in.Muted
		rows = append(rows,
			menu.Row{Kind: menu.Separator},
			menu.Row{Kind: menu.Section, Label: "Input"},
			menu.Row{
				Kind:  menu.Slider,
				Glyph: micGlyph(muted),
				Value: in.Volume,
				Slide: func(value float64, final bool) { act.setVolume(false, name, value, final) },
			},
			menu.Row{
				Kind:     menu.Item,
				Label:    "Mute microphone",
				On:       muted,
				Glyph:    micGlyph(true),
				Trailing: menu.TrailingSwitch,
				KeepOpen: true,
				Click:    func() { act.setMute(false, name, !muted) },
			})
		if len(v.audio.Inputs) > 1 {
			rows = append(rows, deviceRows(v.audio.Inputs, false, act)...)
		}
	}

	if len(v.players) > 0 {
		p := v.players[0]
		rows = append(rows, menu.Row{Kind: menu.Separator}, menu.Row{Kind: menu.Section, Label: "Now Playing"})
		rows = append(rows, playerRows(&p, act)...)
	}

	return append(rows,
		menu.Row{Kind: menu.Separator},
		menu.Row{Kind: menu.Action, Label: "Sound Settings…", Click: act.settings})
}

// levelText is the header's figure: the volume, or that it is muted.
func levelText(d *audio.Device) string {
	switch {
	case d == nil:
		return ""
	case d.Muted:
		return "Muted"
	}
	return fmt.Sprintf("%.0f%%", d.Volume*100)
}

// deviceRows lists devices to choose the default from. The default is lit
// and inert; picking another keeps the menu open, so the badge can be seen
// moving to it.
func deviceRows(devices []audio.Device, output bool, act actions) []menu.Row {
	rows := make([]menu.Row, 0, min(len(devices), maxDevices))
	for i := range devices {
		if len(rows) == maxDevices {
			break
		}
		d := &devices[i]
		r := menu.Row{Kind: menu.Item, Label: d.Description, On: d.Default, Glyph: deviceGlyph(d, output), KeepOpen: true}
		if !d.Default {
			name := d.Name
			r.Click = func() { act.setDefault(output, name) }
		}
		rows = append(rows, r)
	}
	return rows
}

func deviceGlyph(d *audio.Device, output bool) menu.Glyph {
	switch {
	case d.Headphones:
		return menu.Symbol(glyph.Headphones)
	case output:
		return speakerGlyph(1, false)
	}
	return menu.Symbol(glyph.Mic)
}

// playerRows shows a player's track and its buttons. A button the player
// says it cannot use is drawn dimmed and does nothing.
func playerRows(p *mpris.Player, act actions) []menu.Row {
	title, detail := p.Title, p.Artist
	switch {
	case title == "":
		title, detail = p.Identity, ""
	case detail == "":
		detail = p.Identity
	}
	bus := p.BusName
	playing := p.Status == mpris.StatusPlaying

	item := menu.Row{Kind: menu.Item, Label: title, Detail: detail, On: playing, Glyph: menu.Symbol(glyph.Play)}
	toggle := menu.Button{Glyph: menu.Symbol(glyph.Play)}
	if playing {
		item.Glyph, toggle.Glyph = menu.Symbol(glyph.Pause), menu.Symbol(glyph.Pause)
	}
	if (playing && p.CanPause) || (!playing && p.CanPlay) {
		toggle.Click = func() { act.playPause(bus) }
		item.Click, item.KeepOpen = toggle.Click, true
	}
	previous := menu.Button{Glyph: menu.Symbol(glyph.Previous)}
	if p.CanGoPrevious {
		previous.Click = func() { act.previous(bus) }
	}
	next := menu.Button{Glyph: menu.Symbol(glyph.Next)}
	if p.CanGoNext {
		next.Click = func() { act.next(bus) }
	}
	return []menu.Row{item, {Kind: menu.Transport, Buttons: []menu.Button{previous, toggle, next}}}
}

// wavesFor is how many sound waves a volume lights: none when muted or
// silent, then one per third.
func wavesFor(volume float64, muted bool) int {
	switch {
	case muted || volume <= 0:
		return 0
	case volume < 1.0/3:
		return 1
	case volume < 2.0/3:
		return 2
	}
	return glyph.SpeakerWaves
}

func speakerGlyph(volume float64, muted bool) menu.Glyph {
	waves := wavesFor(volume, muted)
	return func(dst *image.RGBA, x0, y0, s float64, col color.RGBA) {
		glyph.Speaker(dst, x0, y0, s, waves, muted, col, glyph.Dim(col, 0.33))
	}
}

func micGlyph(muted bool) menu.Glyph {
	if !muted {
		return menu.Symbol(glyph.Mic)
	}
	return func(dst *image.RGBA, x0, y0, s float64, col color.RGBA) {
		glyph.Slashed(dst, x0, y0, s, col, glyph.Mic(x0, y0, s))
	}
}
