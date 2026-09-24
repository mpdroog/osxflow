package main

// Drawing the bar. It is redrawn whole whenever anything on it changes:
// a strip of the screen, a few times a minute, is not worth tracking
// damage for.

import (
	"image"

	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
)

// shownItems is the tray items to draw, in bar order: those that have
// something to draw and are not asking to be hidden.
func (a *app) shownItems() []*trayItem {
	ids := make([]string, len(a.refs))
	for i, ref := range a.refs {
		ids[i] = a.items[ref].state.ID
	}
	var out []*trayItem
	for _, i := range order(ids) {
		it := a.items[a.refs[i]]
		if it.ready && !it.state.Hidden() {
			out = append(out, it)
		}
	}
	return out
}

// relayout works out the slots again, after anything that changes what is
// on the bar, and marks it for drawing.
func (a *app) relayout() {
	a.shown = a.shownItems()
	a.slots = layoutBar(a.width, a.m, text.Width(a.bold, a.appName), text.Width(a.regular, a.clock), len(a.shown))
	a.hover = -1
	if a.pointer.In(image.Rect(0, 0, a.width, a.m.height)) {
		a.hover = at(a.slots, a.pointer, a.m.height)
	}
	a.dirty = true
}

func (a *app) paint() error {
	a.dirty = false
	img := a.win.Surf.Image()
	paint.Clear(img)
	paint.FillBlend(img, img.Bounds(), colBar)
	mid := a.m.height / 2
	for i := range a.slots {
		s := &a.slots[i]
		if i == a.hover && s.clickable() {
			paint.RoundRect(img, s.r, float64(a.m.radius), colHighlight)
		}
		switch s.kind {
		case slotMenu:
			centre(img, a.tux, s.r, mid)
		case slotItem:
			if icon := a.shown[s.item].icon; icon != nil {
				centre(img, icon, s.r, mid)
			}
		case slotApp:
			label(img, a.bold, s.r.Min.X+a.m.pad, mid, a.appName, s.r.Dx()-2*a.m.pad)
		case slotClock:
			label(img, a.regular, s.r.Min.X+a.m.pad, mid, a.clock, s.r.Dx()-2*a.m.pad+1)
		}
	}
	return a.win.Surf.Flush()
}

// centre draws src centred in r's width and on the bar's middle.
func centre(dst, src *image.RGBA, r image.Rectangle, mid int) {
	b := src.Bounds()
	paint.Over(dst, src, r.Min.X+(r.Dx()-b.Dx())/2, mid-b.Dy()/2)
}

// label draws one line of text vertically centred on mid.
func label(img *image.RGBA, face font.Face, x, mid int, s string, width int) {
	if width < 1 || s == "" {
		return
	}
	m := face.Metrics()
	baseline := mid + (m.Ascent-m.Descent).Round()/2
	text.Draw(img, face, colText, x, baseline, s, width)
}
