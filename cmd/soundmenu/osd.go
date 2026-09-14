package main

// The on-screen overlay a volume key shows: a translucent square with a
// speaker (or a microphone) and a row of level segments, as macOS draws
// it, gone again a moment after the last key press.

import (
	"image"
	"image/color"
	"log"
	"math"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/paint"
)

type osdKind int

const (
	osdVolume osdKind = iota
	osdMic
)

// osdView is what the overlay shows.
type osdView struct {
	kind  osdKind
	level float64
	muted bool
}

const (
	// osdSegments is macOS's count, which is also why sixteen key presses
	// take it from silent to full there. Here a press is 5%, so twenty.
	osdSegments = 16

	// baseOSD is the overlay's side in logical pixels.
	baseOSD = 200

	// osdShown is how long the overlay stays after the last press.
	osdShown = 1500 * time.Millisecond
)

var (
	colOSDBg  = color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xd8}
	colOSDOn  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xf0}
	colOSDOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x40}
)

// segmentsLit is how many level segments to light: none when muted, and
// the level rounded to the nearest segment otherwise.
func segmentsLit(level float64, muted bool) int {
	if muted || math.IsNaN(level) {
		return 0
	}
	n := int(math.Round(math.Max(0, math.Min(1, level)) * osdSegments))
	return max(0, min(osdSegments, n))
}

// renderOSD draws the overlay into img, which is square.
func renderOSD(img *image.RGBA, scale float64, v osdView) {
	b := img.Bounds()
	w := float64(b.Dx())
	paint.Clear(img)
	paint.RoundRectOnClear(img, b, 18*scale, colOSDBg)

	gs := w * 0.42
	gx, gy := w/2-gs/2, w*0.14
	switch v.kind {
	case osdMic:
		if v.muted {
			glyph.Slashed(img, gx, gy, gs, colOSDOn, glyph.Mic(gx, gy, gs))
		} else {
			glyph.Fill(img, glyph.Area(gx, gy, gs, gs), colOSDOn, glyph.Mic(gx, gy, gs))
		}
	case osdVolume:
		glyph.Speaker(img, gx, gy, gs, wavesFor(v.level, v.muted), v.muted, colOSDOn, colOSDOff)
	}

	lit := segmentsLit(v.level, v.muted)
	barW := w * 0.7
	x0 := (w - barW) / 2
	gap := w * 0.008
	segW := (barW - gap*(osdSegments-1)) / osdSegments
	h, y := w*0.035, w*0.76
	for i := range osdSegments {
		col := colOSDOff
		if i < lit {
			col = colOSDOn
		}
		x := x0 + float64(i)*(segW+gap)
		glyph.Fill(img, glyph.Area(x, y, segW, h), col, glyph.Box(x+segW/2, y+h/2, segW/2, h/2, 0))
	}
}

// osd is the overlay window, made on first use and then only mapped and
// unmapped.
type osd struct {
	win    *menu.Window
	mapped bool
	timer  *time.Timer
	hideC  <-chan time.Time
}

// show draws v and shows the overlay for osdShown from now. A failure
// costs only the overlay, so it is logged rather than returned.
func (o *osd) show(h *menu.Host, v osdView) {
	if o.win == nil {
		size := int(baseOSD*h.Theme.Scale + 0.5)
		sw, sh := int(h.Screen.WidthInPixels), int(h.Screen.HeightInPixels)
		// Centred, three quarters of the way down: clear of the menus under
		// the panel, and above where the dock slides up.
		win, err := h.NewWindow((sw-size)/2, sh*3/4-size/2, size, size, "osxflow-osd",
			uint32(xproto.EventMaskExposure))
		if err != nil {
			log.Printf("volume overlay: %v", err)
			return
		}
		o.win = win
	}
	renderOSD(o.win.Surf.Image(), h.Theme.Scale, v)
	if err := o.win.Surf.Flush(); err != nil {
		log.Printf("drawing the volume overlay: %v", err)
	}
	if o.mapped {
		if err := o.win.Raise(); err != nil {
			log.Printf("volume overlay: %v", err)
		}
	} else {
		if err := o.win.Map(); err != nil {
			log.Printf("volume overlay: %v", err)
			return
		}
		o.mapped = true
	}
	if o.timer != nil {
		o.timer.Stop()
	}
	o.timer = time.NewTimer(osdShown)
	o.hideC = o.timer.C
}

// hide takes the overlay down once its time is up.
func (o *osd) hide() {
	o.timer, o.hideC = nil, nil
	if !o.mapped {
		return
	}
	if err := o.win.Unmap(); err != nil {
		log.Printf("volume overlay: %v", err)
	}
	o.mapped = false
}

// expose repaints the overlay when X asks. It reports whether the event
// was the overlay's.
func (o *osd) expose(e xproto.ExposeEvent) bool {
	if o.win == nil || e.Window != o.win.ID {
		return false
	}
	if err := o.win.Surf.Copy(); err != nil {
		log.Printf("volume overlay: %v", err)
	}
	return true
}

func (o *osd) close() {
	if o.timer != nil {
		o.timer.Stop()
	}
	if o.win != nil {
		o.win.Close()
		o.win = nil
	}
}
