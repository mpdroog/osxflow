package main

// The tray icon, and what it and its tooltip say.

import (
	"fmt"
	"image"
	"image/color"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/sni"
)

// iconSizes are the pixmaps handed to the tray, which picks the one
// closest to what it draws: 44 on this 2x panel.
var iconSizes = [...]int{22, 32, 44, 64}

// The tray icon sits on the dark panel. With no output to control it is
// drawn faint, as a disabled control would be.
var (
	colIconOn  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colIconDim = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x5a}
	colIconOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x30}
)

// iconState is everything the icon shows. It is comparable, so the icon is
// redrawn only when it would look different -- not on every percent of a
// volume drag.
type iconState struct {
	present bool
	waves   int
	muted   bool
}

func iconFor(v *view) iconState {
	out := v.audio.DefaultOutput()
	if !v.connected || out == nil {
		return iconState{}
	}
	return iconState{present: true, waves: wavesFor(out.Volume, out.Muted), muted: out.Muted}
}

func renderIcon(size int, s iconState) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	on, off := colIconOn, colIconDim
	if !s.present {
		on, off = colIconOff, colIconOff
	}
	glyph.Speaker(img, 0, 0, float64(size), s.waves, s.muted, on, off)
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
	out := v.audio.DefaultOutput()
	var text string
	switch {
	case !v.connected:
		text = "Sound server not running"
	case out == nil:
		text = "No sound output"
	case out.Muted:
		text = "Muted · " + out.Description
	default:
		text = fmt.Sprintf("%.0f%% · %s", out.Volume*100, out.Description)
	}
	return sni.ToolTip{Title: "Sound", Text: text}
}
