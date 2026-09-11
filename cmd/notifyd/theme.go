package main

// Every size and colour a banner is drawn with.
//
// Sizes are logical pixels, multiplied by the display scale once, here --
// the arrangement cmd/dock uses, for the reason it gives: X11 knows nothing
// of scaling, and a tool that forgets to apply it draws at half size.

import "image/color"

// Logical sizes, before scaling.
const (
	baseWidth   = 360
	basePad     = 13
	baseIcon    = 40
	baseIconGap = 11
	baseRadius  = 16

	// Stacking: the space between banners and from the screen's edges. The
	// top margin is measured from the work area, so it is below the panel.
	baseGap     = 10
	baseMarginR = 12
	baseMarginT = 10

	// The transparent margin around each plate, for its shadow and for the
	// close button, which overhangs the top-left corner.
	baseWinPadX = 12
	baseWinPadY = 5
	baseShadow  = 5

	baseTitleH = 18
	baseLineH  = 17

	// The level meter volume and brightness popups carry.
	baseBarTop = 8
	baseBarH   = 5

	baseButtonTop    = 11
	baseButtonH      = 26
	baseButtonGap    = 8
	baseButtonPadX   = 8
	baseButtonRadius = 8

	baseCloseR     = 9.5
	baseCloseInset = 5

	// How far a closing banner drifts right while it fades.
	baseCloseSlide = 70

	baseTitlePt  = 10.5
	baseBodyPt   = 10
	baseButtonPt = 9.5
	baseClosePt  = 10
)

// Counts, which do not scale.
const (
	bodyLines  = 4
	maxButtons = 3

	// maxShown is how many banners are on screen at once. More than this
	// is a wall rather than a notification, and the rest wait their turn.
	maxShown = 5
)

// theme is the scaled layout, in real pixels.
type theme struct {
	scale float64

	width, pad, icon, iconGap int
	radius                    float64

	gap, marginR, marginT     int
	winPadX, winPadY, shadow  int
	titleH, lineH             int
	barTop, barH              int
	buttonTop, buttonH        int
	buttonGap, buttonPadX     int
	buttonRadius              float64
	closeR                    float64
	closeInset                int
	closeSlide                float64
	titlePt, bodyPt, buttonPt float64
	closePt                   float64
}

func newTheme(s float64) *theme {
	if s <= 0 {
		s = 1
	}
	px := func(v float64) int { return int(v*s + 0.5) }
	t := &theme{
		scale:        s,
		width:        px(baseWidth),
		pad:          px(basePad),
		icon:         px(baseIcon),
		iconGap:      px(baseIconGap),
		radius:       baseRadius * s,
		gap:          px(baseGap),
		marginR:      px(baseMarginR),
		marginT:      px(baseMarginT),
		winPadX:      px(baseWinPadX),
		winPadY:      px(baseWinPadY),
		shadow:       px(baseShadow),
		titleH:       px(baseTitleH),
		lineH:        px(baseLineH),
		barTop:       px(baseBarTop),
		barH:         px(baseBarH),
		buttonTop:    px(baseButtonTop),
		buttonH:      px(baseButtonH),
		buttonGap:    px(baseButtonGap),
		buttonPadX:   px(baseButtonPadX),
		buttonRadius: baseButtonRadius * s,
		closeR:       baseCloseR * s,
		closeInset:   px(baseCloseInset),
		closeSlide:   baseCloseSlide * s,
		titlePt:      baseTitlePt * s,
		bodyPt:       baseBodyPt * s,
		buttonPt:     baseButtonPt * s,
		closePt:      baseClosePt * s,
	}
	// Resting banners' windows must not overlap, or the transparent margin
	// of one would lie over the plate of the next and take its clicks.
	t.gap = max(t.gap, 2*t.winPadY)
	// Nor may the shadow reach past the margin: a shadow cut off square by
	// the window edge is worse than a smaller one.
	t.shadow = min(t.shadow, t.winPadY)
	return t
}

// The palette is the dock's: a translucent slate plate with a hairline of
// light along its top edge. Critical notifications swap the hairline for
// red, which is enough to mark them without shouting.
var (
	colPlate        = color.RGBA{R: 0x26, G: 0x26, B: 0x2c, A: 0xeb}
	colEdge         = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x1c}
	colEdgeCritical = color.RGBA{R: 0xff, G: 0x5f, B: 0x57, A: 0x80}
	colShadow       = color.RGBA{A: 0x0c}

	colTitle = color.RGBA{R: 0xf4, G: 0xf4, B: 0xf8, A: 0xff}
	colBody  = color.RGBA{R: 0xcf, G: 0xcf, B: 0xd8, A: 0xff}

	colBarTrack = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x26}
	colBarFill  = color.RGBA{R: 0xec, G: 0xec, B: 0xf4, A: 0xf0}

	colButton      = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x1a}
	colButtonHover = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x48}
	colButtonText  = color.RGBA{R: 0xee, G: 0xee, B: 0xf4, A: 0xff}

	colClose      = color.RGBA{R: 0x55, G: 0x55, B: 0x5e, A: 0xf6}
	colCloseHover = color.RGBA{R: 0x72, G: 0x72, B: 0x7c, A: 0xf8}
	colCloseText  = color.RGBA{R: 0xf4, G: 0xf4, B: 0xf8, A: 0xff}
)
