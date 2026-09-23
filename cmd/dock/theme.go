package main

// Every size and colour the dock draws with.
//
// Sizes are written in logical pixels -- what they would be on an unscaled
// display -- and multiplied by the display scale exactly once, here. That
// is the whole reason this file exists separately: plank draws this dock's
// icons at about 26 physical pixels on a 2560x1600 screen because it
// treats the scale factor as somebody else's problem, and the result is a
// dock you squint at.

import (
	"image/color"

	"github.com/mpdroog/osxflow/internal/dock"
)

// Logical sizes, before scaling.
const (
	baseIcon      = 48
	baseGap       = 8
	basePadding   = 14
	baseSeparator = 15
	baseBottomGap = 7
	baseRadius    = 17

	// Magnification. The influence radius is a little over two icon slots,
	// which makes the swell reach its neighbours without dragging the whole
	// row along with the cursor.
	maxZoom       = 1.55
	baseInfluence = 130

	// The running indicator, below the icon.
	baseDotRadius = 2.3
	baseDotGap    = 6

	// Air above the icons inside the panel, and below the dots.
	baseIconTopPad = 8
	baseDotBottom  = 5

	// The hovered item's name, floating above the icon.
	baseTooltipH       = 27
	baseTooltipGap     = 9
	baseTooltipPadX    = 11
	baseTooltipRadius  = 8
	baseTooltipTextPt  = 11.5
	baseTooltipBaseSub = 9

	// The stack popup.
	basePopupRowH   = 30
	basePopupPadY   = 8
	basePopupPadX   = 12
	basePopupIcon   = 18
	basePopupRadius = 12
	basePopupTextPt = 11
	basePopupMinW   = 190
	basePopupMaxW   = 330
	basePopupGap    = 10
	basePopupSepH   = 1
)

// theme is the scaled layout, in real pixels.
type theme struct {
	scale float64
	m     dock.Metrics

	// iconTopPad is the air between the panel's top edge and an
	// unmagnified icon.
	iconTopPad float64

	// dotBottom is the air below the indicator dots.
	dotBottom float64

	// panelH is the height of the rounded plate.
	panelH float64

	// winH is the window's height: enough for the tooltip, a fully
	// magnified icon, the panel and the gap below it.
	winH int

	tooltipH       float64
	tooltipGap     float64
	tooltipPadX    float64
	tooltipRadius  float64
	tooltipTextPt  float64
	tooltipBaseSub float64

	popupRowH   float64
	popupPadY   float64
	popupPadX   float64
	popupIcon   int
	popupRadius float64
	popupTextPt float64
	popupMinW   float64
	popupMaxW   float64
	popupGap    float64
	popupSepH   float64
}

func newTheme(s float64) *theme {
	if s <= 0 {
		s = 1
	}
	px := func(v float64) float64 { return v * s }

	t := &theme{
		scale: s,
		m: dock.Metrics{
			Scale:      s,
			Icon:       px(baseIcon),
			MaxZoom:    maxZoom,
			Influence:  px(baseInfluence),
			Gap:        px(baseGap),
			Padding:    px(basePadding),
			SeparatorW: px(baseSeparator),
			BottomGap:  px(baseBottomGap),
			DotRadius:  px(baseDotRadius),
			DotGap:     px(baseDotGap),
			Radius:     px(baseRadius),
		},
		iconTopPad:     px(baseIconTopPad),
		dotBottom:      px(baseDotBottom),
		tooltipH:       px(baseTooltipH),
		tooltipGap:     px(baseTooltipGap),
		tooltipPadX:    px(baseTooltipPadX),
		tooltipRadius:  px(baseTooltipRadius),
		tooltipTextPt:  baseTooltipTextPt * s,
		tooltipBaseSub: px(baseTooltipBaseSub),
		popupRowH:      px(basePopupRowH),
		popupPadY:      px(basePopupPadY),
		popupPadX:      px(basePopupPadX),
		popupIcon:      int(px(basePopupIcon)),
		popupRadius:    px(basePopupRadius),
		popupTextPt:    basePopupTextPt * s,
		popupMinW:      px(basePopupMinW),
		popupMaxW:      px(basePopupMaxW),
		popupGap:       px(basePopupGap),
		popupSepH:      max64(px(basePopupSepH), 1),
	}

	// The plate holds an unmagnified icon plus its air and its dot.
	t.panelH = t.iconTopPad + t.m.Icon + t.m.DotGap + t.m.DotRadius + t.dotBottom

	// The window has to contain the tallest thing that can appear: a fully
	// magnified icon with its tooltip above it. The icon grows upward from
	// the baseline, so the overflow above the plate is what the extra zoom
	// adds.
	overflow := t.m.Icon*(maxZoom-1) + t.iconTopPad
	t.winH = int(t.tooltipH + t.tooltipGap + overflow + t.panelH + t.m.BottomGap + 0.5)
	return t
}

// baselineY is where the bottom of every icon sits, measured from the top
// of the window. Icons are bottom-aligned there and grow upward, which is
// what makes magnification look like the dock is lifting them out.
func (t *theme) baselineY() float64 {
	return float64(t.winH) - t.m.BottomGap - t.dotBottom - t.m.DotRadius*2 - t.m.DotGap
}

// panelTop and panelBottom bound the rounded plate.
func (t *theme) panelTop() float64    { return t.baselineY() - t.m.Icon - t.iconTopPad }
func (t *theme) panelBottom() float64 { return float64(t.winH) - t.m.BottomGap }

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// The palette is macOS's dark dock: a translucent slate plate with a
// hairline of light along its top edge, which is what stops it reading as
// a flat rectangle sitting on the wallpaper.
var (
	colPanel     = color.RGBA{R: 0x28, G: 0x28, B: 0x2f, A: 0xbe}
	colPanelEdge = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x1c}
	colSeparator = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x2b}
	colDot       = color.RGBA{R: 0xec, G: 0xec, B: 0xf4, A: 0xe8}

	colTooltipBg   = color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xf2}
	colTooltipEdge = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
	colTooltipText = color.RGBA{R: 0xf4, G: 0xf4, B: 0xf8, A: 0xff}

	colPopupBg    = color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xf4}
	colPopupEdge  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
	colPopupText  = color.RGBA{R: 0xea, G: 0xea, B: 0xf0, A: 0xff}
	colPopupDim   = color.RGBA{R: 0x9a, G: 0x9a, B: 0xa8, A: 0xff}
	colPopupHover = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
)

// displayNames shortens the names the .desktop files give.
//
// "Firefox Web Browser" and "Thunar File Manager" are accurate and too
// long for a tooltip that appears under the cursor; macOS would say
// "Firefox" and "Files". This is the one place the dock second-guesses the
// system, and it is a list of five, for one user.
var displayNames = map[string]string{
	"org.mozilla.firefox.desktop":    "Firefox",
	"thunderbird.desktop":            "Thunderbird",
	"thunar.desktop":                 "Files",
	"com.mitchellh.ghostty.desktop":  "Ghostty",
	"com.discordapp.Discord.desktop": "Discord",
}

// pinnedIDs is the dock's contents, in order, taken from what plank was
// showing. Changing the dock means changing this list and rebuilding,
// which is the whole configuration story: this is a dock for one person on
// one machine.
var pinnedIDs = []string{
	"thunderbird.desktop",
	// Firefox here is the flatpak, so the id is its app id and not the
	// distro package's "firefox.desktop" that plank was pinned to on Mint.
	// The old id matches nothing on this machine, so the pin was silently
	// skipped and Firefox could only ever appear as a transient icon while
	// it happened to be running.
	"org.mozilla.firefox.desktop",
	"com.mitchellh.ghostty.desktop",
	"thunar.desktop",
	"com.discordapp.Discord.desktop",
}
