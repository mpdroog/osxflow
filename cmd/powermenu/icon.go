package main

// The tray icon, and what it and its tooltip say.

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/sni"
	"github.com/mpdroog/osxflow/internal/upower"
)

// iconSizes are the pixmaps handed to the tray, which picks the one
// closest to what it draws: 44 on this 2x panel.
var iconSizes = [...]int{22, 32, 44, 64}

// lowPercent is where the charge turns red, as macOS's does.
const lowPercent = 20

// The tray icon sits on the dark panel. The charge is white, green while
// charging and red when low, as on macOS.
var (
	colIconOn   = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colCharging = color.RGBA{R: 0x30, G: 0xd1, B: 0x58, A: 0xff}
	colLow      = color.RGBA{R: 0xff, G: 0x45, B: 0x3a, A: 0xff}
)

// iconState is everything the icon shows. It is comparable, so the icon is
// redrawn only when it would look different -- at most once per percent.
type iconState struct {
	present  bool
	percent  int
	charging bool
	low      bool
}

func iconFor(v *view) iconState {
	if !v.hasBattery {
		return iconState{}
	}
	b := &v.battery
	s := iconState{present: true, percent: int(math.Round(b.Percentage)), charging: b.Charging()}
	s.low = s.percent <= lowPercent && !s.charging
	return s
}

// renderIcon draws the tray icon at one size: a battery filled to its
// charge, with a bolt while charging, or empty when there is no battery.
func renderIcon(size int, s iconState) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fill := colIconOn
	switch {
	case s.charging:
		fill = colCharging
	case s.low:
		fill = colLow
	}
	fraction := 0.0
	if s.present {
		fraction = float64(s.percent) / 100
	}
	glyph.Battery(img, 0, 0, float64(size), fraction, s.charging, colIconOn, fill)
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
	if !v.hasBattery {
		return sni.ToolTip{Title: "Battery", Text: "No battery found"}
	}
	b := &v.battery
	text := fmt.Sprintf("%.0f%%", b.Percentage)
	if remaining := upower.Remaining(b); remaining != "" {
		text += ", " + remaining
	}
	return sni.ToolTip{Title: "Battery", Text: text}
}
