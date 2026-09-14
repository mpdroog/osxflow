package menu

// Every size and colour a menu draws with.
//
// Sizes are logical pixels, multiplied by the display scale once, here --
// as in the dock, and for the same reason: at 2x a menu that ignores the
// scale is a menu you squint at.

import (
	"image/color"
)

// Logical sizes, before scaling.
const (
	baseMenuPadY = 6
	baseMenuPadX = 14
	baseRadius   = 12

	// baseGap is the air between the panel and the menu.
	baseGap = 6

	// The round badge in front of an Item, and the symbol in it.
	baseBadge      = 26
	baseBadgeGlyph = 15

	baseSwitchW = 34
	baseSwitchH = 20
	baseLock    = 11

	baseSliderGlyph = 16
	baseTrackH      = 5
	baseKnob        = 18

	baseButton      = 34
	baseButtonGlyph = 16

	baseTextPt    = 11
	baseSectionPt = 9
)

var baseHeights = [kindCount]float64{
	Toggle:    38,
	Header:    34,
	Section:   24,
	Note:      24,
	Item:      34,
	Slider:    34,
	Transport: 40,
	Separator: 11,
	Action:    30,
}

// Theme is a menu's layout in real pixels.
type Theme struct {
	Scale float64

	// Width is the menu's width. Every menu from one Host is as wide.
	Width int

	padY, padX, radius, gap float64

	heights [kindCount]float64

	badge, badgeGlyph   float64
	switchW, switchH    float64
	lock                float64
	sliderGlyph, trackH float64
	knob                float64
	button, buttonGlyph float64

	textPt, sectionPt float64
}

// NewTheme scales the layout for a display scale, with menus width logical
// pixels wide.
func NewTheme(scale, width float64) *Theme {
	if scale <= 0 {
		scale = 1
	}
	px := func(v float64) float64 { return v * scale }
	t := &Theme{
		Scale:       scale,
		Width:       int(px(width) + 0.5),
		padY:        px(baseMenuPadY),
		padX:        px(baseMenuPadX),
		radius:      px(baseRadius),
		gap:         px(baseGap),
		badge:       px(baseBadge),
		badgeGlyph:  px(baseBadgeGlyph),
		switchW:     px(baseSwitchW),
		switchH:     px(baseSwitchH),
		lock:        px(baseLock),
		sliderGlyph: px(baseSliderGlyph),
		trackH:      px(baseTrackH),
		knob:        px(baseKnob),
		button:      px(baseButton),
		buttonGlyph: px(baseButtonGlyph),
		textPt:      baseTextPt * scale,
		sectionPt:   baseSectionPt * scale,
	}
	for k, h := range baseHeights {
		t.heights[k] = px(h)
	}
	return t
}

func (t *Theme) height(k Kind) float64 {
	if k < 0 || k >= kindCount {
		return t.heights[Item]
	}
	return t.heights[k]
}

// The palette is the dock's popup, so the tools read as one family, plus
// macOS's accent blue for what is on.
//
// Colours are straight alpha, as everywhere in this repository.
var (
	colBg        = color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xf4}
	colEdge      = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
	colText      = color.RGBA{R: 0xea, G: 0xea, B: 0xf0, A: 0xff}
	colDim       = color.RGBA{R: 0x9a, G: 0x9a, B: 0xa8, A: 0xff}
	colHover     = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
	colSeparator = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x2b}

	colAccent    = color.RGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0xff}
	colBadgeOff  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x26}
	colSwitchOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x33}
	colKnob      = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colKnobEdge  = color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x50}
	colGlyph     = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

	colTrack     = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x33}
	colTrackFill = color.RGBA{R: 0xea, G: 0xea, B: 0xf0, A: 0xe6}
)
