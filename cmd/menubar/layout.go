package main

// Where everything on the bar goes, and what a point on it is over. Kept
// apart from X and D-Bus so that it can be tested as the geometry it is.

import (
	"image"
	"image/color"
	"slices"
)

// Logical sizes, before scaling.
const (
	// baseHeight is the panel's: 58 real pixels at 2x, so that replacing
	// it leaves every window's work area where it was.
	baseHeight = 29

	// baseEdge is the space between the screen's ends and the first
	// highlight; basePad the space between a highlight's edge and what it
	// holds, and between slots.
	baseEdge = 8
	basePad  = 7
	baseGap  = 2

	baseIcon    = 18
	baseTux     = 17
	baseRadius  = 5
	baseInsetY  = 3
	baseTextPt  = 10.5
	baseMenuW   = 240
	baseAppGapX = 6
)

// Colours are straight alpha, as everywhere in this repository. The bar is
// translucent over the wallpaper the way macOS's is, without the blur: dark
// enough that the text stays readable over any picture.
var (
	colBar       = color.RGBA{R: 0x16, G: 0x16, B: 0x1c, A: 0xc8}
	colHighlight = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x26}
	colText      = color.RGBA{R: 0xf2, G: 0xf2, B: 0xf5, A: 0xff}
)

// metrics is the bar's layout in real pixels.
type metrics struct {
	scale                  float64
	height, edge, pad, gap int
	icon, tux, radius      int
	insetY, appGap, menuW  int
	textPt                 float64
}

func newMetrics(scale float64) *metrics {
	if scale <= 0 {
		scale = 1
	}
	px := func(v float64) int { return int(v*scale + 0.5) }
	return &metrics{
		scale:  scale,
		height: px(baseHeight),
		edge:   px(baseEdge),
		pad:    px(basePad),
		gap:    px(baseGap),
		icon:   px(baseIcon),
		tux:    px(baseTux),
		radius: px(baseRadius),
		insetY: px(baseInsetY),
		appGap: px(baseAppGapX),
		menuW:  baseMenuW,
		textPt: baseTextPt * scale,
	}
}

// slotKind is what a slot on the bar holds.
type slotKind int

const (
	slotMenu  slotKind = iota // Tux, which opens macmenu
	slotApp                   // the focused application's name
	slotItem                  // a tray item
	slotClock                 // the time, which opens the calendar
)

// slot is one thing on the bar. r is its whole clickable, highlightable
// area, padding included, full height of the bar less the highlight inset.
type slot struct {
	kind slotKind
	item int // which tray item, for slotItem
	r    image.Rectangle
}

// clickable reports whether pressing the slot does something.
func (s *slot) clickable() bool { return s.kind != slotApp }

// layoutBar places the slots on a bar width pixels wide: Tux and the
// application's name from the left, the clock and then the tray items from
// the right. appW and clockW are the widths of their text, and items the
// number of tray items shown. Items are laid out right to left: item 0
// sits next to the clock.
//
// When the two ends would overlap, the application name gives way: it is
// cut to whatever room is left, down to nothing.
func layoutBar(width int, m *metrics, appW, clockW, items int) []slot {
	top, bottom := m.insetY, m.height-m.insetY
	slots := make([]slot, 0, 3+items)

	x := width - m.edge
	if clockW > 0 {
		r := image.Rect(x-clockW-2*m.pad, top, x, bottom)
		slots = append(slots, slot{kind: slotClock, r: r})
		x = r.Min.X - m.gap
	}
	for i := range items {
		r := image.Rect(x-m.icon-2*m.pad, top, x, bottom)
		slots = append(slots, slot{kind: slotItem, item: i, r: r})
		x = r.Min.X - m.gap
	}
	right := x

	menu := image.Rect(m.edge, top, m.edge+m.tux+2*m.pad, bottom)
	slots = append(slots, slot{kind: slotMenu, r: menu})
	appX := menu.Max.X + m.appGap
	if room := right - m.appGap - appX - 2*m.pad; appW > 0 && room > 0 {
		slots = append(slots, slot{kind: slotApp, r: image.Rect(appX, top, appX+min(appW, room)+2*m.pad, bottom)})
	}
	return slots
}

// at finds the slot a point on a bar height pixels tall is over, or -1.
// Only the x matters within the bar: a click at the very top edge of the
// screen, which is where a flung pointer stops, lands on what is below it.
func at(slots []slot, p image.Point, height int) int {
	if p.Y < 0 || p.Y >= height {
		return -1
	}
	for i := range slots {
		lo := slots[i].r.Min.X
		if slots[i].kind == slotMenu {
			// The same for the screen's corner: Tux takes everything to
			// its left.
			lo = 0
		}
		if p.X >= lo && p.X < slots[i].r.Max.X {
			return i
		}
	}
	return -1
}

// ownItems are osxflow's tray tools, in the order they sit leftwards from
// the clock -- macOS's order for battery, Wi-Fi, Bluetooth and sound.
var ownItems = []string{"osxflow-powermenu", "osxflow-netmenu", "osxflow-bluemenu", "osxflow-soundmenu"}

// order sorts tray items for the bar, given their ids in the order they
// registered: osxflow's own in a fixed order next to the clock, whatever
// order they happened to start in at login, then everything else in the
// order it registered. It returns indexes into ids, the first nearest the
// clock.
func order(ids []string) []int {
	out := make([]int, 0, len(ids))
	for _, own := range ownItems {
		for i, id := range ids {
			if id == own {
				out = append(out, i)
			}
		}
	}
	for i, id := range ids {
		if !slices.Contains(ownItems, id) {
			out = append(out, i)
		}
	}
	return out
}
