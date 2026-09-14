package main

// Every size and colour the menu draws with.
//
// Sizes are logical pixels, multiplied by the display scale once, here --
// as in the dock, and for the same reason: at 2x a menu that ignores the
// scale is a menu you squint at.

import (
	"image/color"
)

// Logical sizes, before scaling.
const (
	baseMenuW    = 300
	baseMenuPadY = 6
	baseMenuPadX = 14
	baseRadius   = 12

	// baseGap is the air between the panel and the menu.
	baseGap = 6

	baseRowH     = 34
	baseToggleH  = 38
	baseSectionH = 24
	baseNoteH    = 28
	baseSepH     = 11
	baseActionH  = 30

	// The round badge in front of a network or VPN, and the glyph in it.
	baseBadge      = 26
	baseBadgeGlyph = 15

	baseSwitchW = 34
	baseSwitchH = 20
	baseLock    = 11

	baseTextPt    = 11
	baseSectionPt = 9
)

// rowHeights is how tall each kind of row is, in real pixels.
type rowHeights struct {
	row, toggle, section, note, sep, action float64
}

// theme is the scaled layout, in real pixels.
type theme struct {
	scale float64

	menuW  int
	padY   float64
	padX   float64
	radius float64
	gap    float64

	heights rowHeights

	badge      float64
	badgeGlyph float64
	switchW    float64
	switchH    float64
	lock       float64

	textPt    float64
	sectionPt float64
}

func newTheme(s float64) *theme {
	if s <= 0 {
		s = 1
	}
	px := func(v float64) float64 { return v * s }
	return &theme{
		scale:  s,
		menuW:  int(px(baseMenuW) + 0.5),
		padY:   px(baseMenuPadY),
		padX:   px(baseMenuPadX),
		radius: px(baseRadius),
		gap:    px(baseGap),
		heights: rowHeights{
			row:     px(baseRowH),
			toggle:  px(baseToggleH),
			section: px(baseSectionH),
			note:    px(baseNoteH),
			sep:     px(baseSepH),
			action:  px(baseActionH),
		},
		badge:      px(baseBadge),
		badgeGlyph: px(baseBadgeGlyph),
		switchW:    px(baseSwitchW),
		switchH:    px(baseSwitchH),
		lock:       px(baseLock),
		textPt:     baseTextPt * s,
		sectionPt:  baseSectionPt * s,
	}
}

// The palette is the dock's popup, so the two read as one family, plus
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
	colGlyph     = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colGlyphDim  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x55}

	// The tray icon sits on the dark panel.
	colIconOn  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colIconDim = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x5a}
	colIconOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x30}
)
