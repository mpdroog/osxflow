package main

// The tray icon, and what it and its tooltip say.

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/sni"
)

// iconSizes are the pixmaps handed to the tray, which picks the one
// closest to what it draws: 44 on this 2x panel.
var iconSizes = [...]int{22, 32, 44, 64}

// The icon sits on the dark panel: white when on, faint when off or not
// working, with a dot while a device is connected.
var (
	colIconOn  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colIconOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x50}
)

type iconState struct {
	on        bool
	connected bool
}

func iconFor(v *view) iconState {
	s := iconState{on: powerOf(v).on}
	if s.on {
		for i := range v.state.Devices {
			if v.state.Devices[i].Connected {
				s.connected = true
			}
		}
	}
	return s
}

func renderIcon(size int, s iconState) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	b := img.Bounds()
	side := float64(size)
	col := colIconOn
	if !s.on {
		col = colIconOff
	}
	glyph.Fill(img, b, col, glyph.Bluetooth(0, 0, side))
	if s.connected {
		dot := glyph.Circle(side*0.84, side*0.84, side*0.12)
		glyph.Erase(img, b, side*0.06, dot)
		glyph.Fill(img, b, colIconOn, dot)
	}
	return img
}

func iconPixmaps(s iconState) []sni.Pixmap {
	out := make([]sni.Pixmap, 0, len(iconSizes))
	for _, size := range iconSizes {
		out = append(out, sni.FromImage(renderIcon(size, s)))
	}
	return out
}

func tooltipFor(v *view) sni.ToolTip {
	p := powerOf(v)
	var lines []string
	switch {
	case p.note != "":
		lines = append(lines, p.note)
	case !p.on:
		lines = append(lines, "Bluetooth: off")
	default:
		lines = append(lines, "Bluetooth: on")
		for i := range v.state.Devices {
			d := &v.state.Devices[i]
			if !d.Connected {
				continue
			}
			line := "Connected: " + d.Name
			if b := batteryText(d); b != "" {
				line += fmt.Sprintf(" (%s)", b)
			}
			lines = append(lines, line)
		}
	}
	return sni.ToolTip{Title: "Bluetooth", Text: strings.Join(lines, "\n")}
}
