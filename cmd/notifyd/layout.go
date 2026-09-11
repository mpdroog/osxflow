package main

// Where everything in a banner goes. Kept apart from the drawing so it can
// be tested without a display: this is the part with arithmetic in it.

import (
	"image"

	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/text"
)

type partKind uint8

const (
	partNone partKind = iota
	partBody
	partClose
	partButton
)

// part is what a point in a banner lands on.
type part struct {
	kind  partKind
	index int // which button, for partButton
}

type button struct {
	rect  image.Rectangle
	label string
	key   string
}

// layout is where everything in one banner goes, in window coordinates.
type layout struct {
	// size is the window's: the plate plus the transparent margin around it
	// that holds its shadow and the overhang of the close button.
	size  image.Point
	plate image.Rectangle
	icon  image.Rectangle

	textX     int
	textW     int
	title     string
	titleBase int
	body      []string
	bodyBase  []int

	// bar is the level meter, empty when there is no value to show.
	bar image.Rectangle

	buttons []button

	// The close button is a circle straddling the plate's top-left corner,
	// where macOS puts it.
	closeX, closeY int
	closeR         float64
}

// measure lays out a notification.
func measure(th *theme, f *faces, n *notify.Notification) layout {
	var l layout
	left := th.winPadX + th.pad
	top := th.winPadY + th.pad
	l.icon = image.Rect(left, top, left+th.icon, top+th.icon)
	l.textX = left + th.icon + th.iconGap
	l.textW = th.width - 2*th.pad - th.icon - th.iconGap

	if t := n.Title(); t != "" {
		l.title = text.Truncate(f.title, t, l.textW)
	}
	l.body = text.Wrap(f.body, n.Body, l.textW, bodyLines)

	textH := len(l.body) * th.lineH
	if l.title != "" {
		textH += th.titleH
	}
	if n.Value >= 0 {
		textH += th.barTop + th.barH
	}

	// Text shorter than the icon is centred against it rather than hung
	// from its top edge: a one-line notification level with the top of the
	// icon looks as if it has lost its second line.
	y := top
	if textH < th.icon {
		y += (th.icon - textH) / 2
	}
	if l.title != "" {
		l.titleBase = baseline(f.title, y, th.titleH)
		y += th.titleH
	}
	l.bodyBase = make([]int, len(l.body))
	for i := range l.body {
		l.bodyBase[i] = baseline(f.body, y, th.lineH)
		y += th.lineH
	}
	if n.Value >= 0 {
		y += th.barTop
		l.bar = image.Rect(l.textX, y, l.textX+l.textW, y+th.barH)
	}

	bottom := top + max(th.icon, textH)
	if actions := n.Buttons(); len(actions) > 0 {
		actions = actions[:min(len(actions), maxButtons)]
		bottom += th.buttonTop
		rowW := th.width - 2*th.pad
		w := (rowW - th.buttonGap*(len(actions)-1)) / len(actions)
		l.buttons = make([]button, len(actions))
		for i, a := range actions {
			x := left + i*(w+th.buttonGap)
			l.buttons[i] = button{
				rect:  image.Rect(x, bottom, x+w, bottom+th.buttonH),
				label: text.Truncate(f.button, a.Label, w-2*th.buttonPadX),
				key:   a.Key,
			}
		}
		bottom += th.buttonH
	}

	l.plate = image.Rect(th.winPadX, th.winPadY, th.winPadX+th.width, bottom+th.pad)
	l.size = image.Pt(th.width+2*th.winPadX, l.plate.Max.Y+th.winPadY)
	l.closeX, l.closeY = l.plate.Min.X+th.closeInset, l.plate.Min.Y+th.closeInset
	l.closeR = th.closeR
	return l
}

// hit reports what a point in the window lands on.
//
// The close button is tested first because it overlaps the plate's corner,
// and it is given a pixel of slack all round: it is small, and a click that
// just misses it would otherwise land on the body and invoke the default
// action, which is the opposite of what was meant.
func (l *layout) hit(x, y int) part {
	dx, dy := float64(x-l.closeX), float64(y-l.closeY)
	if r := l.closeR + 1; dx*dx+dy*dy <= r*r {
		return part{kind: partClose}
	}
	pt := image.Pt(x, y)
	for i := range l.buttons {
		if pt.In(l.buttons[i].rect) {
			return part{kind: partButton, index: i}
		}
	}
	if pt.In(l.plate) {
		return part{kind: partBody}
	}
	return part{}
}

// baseline places text vertically centred in a band: the band's middle
// lies halfway between the font's ascent and descent.
func baseline(face font.Face, top, height int) int {
	m := face.Metrics()
	return top + (height+m.Ascent.Round()-m.Descent.Round())/2
}
