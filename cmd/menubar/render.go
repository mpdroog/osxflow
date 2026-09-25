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

// relayout works out one bar's slots again, after anything that changes
// what is on it, and marks it for drawing.
func (a *app) relayout(s *screen) {
	a.shown = a.shownItems()
	s.slots = layoutBar(s.width, s.m,
		text.Width(s.art.bold, a.appName), text.Width(s.art.regular, a.clock), len(a.shown))
	s.hover = -1
	if s.pointer.In(image.Rect(0, 0, s.width, s.m.height)) {
		s.hover = at(s.slots, s.pointer, s.m.height)
	}
	s.dirty = true
}

func (a *app) paint(s *screen) error {
	s.dirty = false
	img, err := s.frame()
	if err != nil {
		return err
	}
	paint.Clear(img)
	paint.FillBlend(img, img.Bounds(), colBar)
	mid := s.m.height / 2
	for i := range s.slots {
		sl := &s.slots[i]
		if i == s.hover && sl.clickable() {
			paint.RoundRect(img, sl.r, float64(s.m.radius), colHighlight)
		}
		switch sl.kind {
		case slotMenu:
			centre(img, s.art.tux, sl.r, mid)
		case slotItem:
			if icon := a.itemIcon(a.shown[sl.item], s); icon != nil {
				centre(img, icon, sl.r, mid)
			}
		case slotApp:
			label(img, s.art.bold, sl.r.Min.X+s.m.pad, mid, a.appName, sl.r.Dx()-2*s.m.pad)
		case slotClock:
			label(img, s.art.regular, sl.r.Min.X+s.m.pad, mid, a.clock, sl.r.Dx()-2*s.m.pad+1)
		}
	}
	return s.flush()
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
