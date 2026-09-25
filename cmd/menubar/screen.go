package main

// One bar per monitor.
//
// macOS puts its menu bar on every display, and layer shell makes that the
// natural shape: a surface belongs to one wl_output, is anchored to that
// output's edges, and reserves its strip out of that output's usable area.
//
// What it costs is that nothing measured in pixels can be shared any more.
// This desk has a 3440-wide ultrawide at scale 1 beside a 4K panel at 1.5,
// so the fonts, Tux and every tray icon have to exist at both sizes. They
// are cached by scale rather than held per bar, so that two monitors that
// do agree -- the ordinary case everywhere but here -- go on sharing one
// set between them, and a laptop with one screen pays nothing at all.
//
// Under X11 there is exactly one screen in this slice, holding the dock
// window, and everything below reads the same way.

import (
	"errors"
	"fmt"
	"image"
	"log"

	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/wl"
	"github.com/mpdroog/osxflow/internal/xmon"
)

// artwork is everything rasterised for one scale.
type artwork struct {
	regular, bold font.Face
	tux           *image.RGBA
	close         func()
}

// screen is one monitor's bar: what it draws on, the sizes it draws at,
// and where the pointer is on it.
type screen struct {
	// Exactly one of these is set: bar under Wayland, win under X11.
	bar *wl.Bar
	win *menu.Window

	// name is the connector ("DP-8"), for the log; mon is where that
	// monitor sits in X's coordinates, which is the space the menus this
	// bar opens are placed in.
	name string
	mon  xmon.Rect

	m   *metrics
	art *artwork

	width   int // device pixels, the width of the picture
	slots   []slot
	hover   int
	pointer image.Point
	dirty   bool
}

// frame is the image this screen's bar is drawn into.
func (s *screen) frame() (*image.RGBA, error) {
	if s.bar != nil {
		return s.bar.Frame()
	}
	return s.win.Surf.Image(), nil
}

// flush puts the drawn image on the monitor.
func (s *screen) flush() error {
	if s.bar != nil {
		return s.bar.Flush()
	}
	return s.win.Surf.Flush()
}

// screenX turns an x on this bar into an x on the X screen.
//
// Under Wayland the two are not the same space: the bar's x is a device
// pixel on one monitor, and a menu is an XWayland window placed in the
// layout's logical pixels across every monitor. The connector name is what
// joins them -- the compositor and XWayland's RandR call the monitor by
// the same "DP-8" -- which is the whole reason Output carries a name.
func (s *screen) screenX(x int) int {
	if s.bar == nil {
		return x
	}
	return barToScreen(x, s.mon.X, s.m.scale)
}

// artworkFor is the fonts and Tux at one scale, made once and kept.
//
// Kept even when no bar is using it any more: the set costs a few hundred
// kilobytes, the number of distinct scales on a desk is the number of
// monitors, and a scale that has been seen once tends to come back -- a
// window dragged between two monitors takes the bar's scale with it and
// then gives it back.
func (a *app) artworkFor(scale float64) (*artwork, error) {
	if art, ok := a.art[scale]; ok {
		return art, nil
	}
	m := newMetrics(scale)
	regular, bold, closeFaces, err := loadFaces(m.textPt)
	if err != nil {
		return nil, err
	}
	tux, err := loadTux(m.tux)
	if err != nil {
		closeFaces()
		return nil, err
	}
	art := &artwork{regular: regular, bold: bold, tux: tux, close: closeFaces}
	a.art[scale] = art
	return art, nil
}

// closeArtwork gives back every face, at exit.
func (a *app) closeArtwork() {
	for _, art := range a.art {
		art.close()
	}
	a.art = nil
}

// newScreen makes the bar for one monitor.
func (a *app) newScreen(out wl.Output) (*screen, error) {
	bar, err := a.wl.NewBar(out.ID, baseHeight, namespace)
	if err != nil {
		return nil, fmt.Errorf("bar on %s: %w", out.Name, err)
	}
	s := &screen{bar: bar, name: out.Name, hover: -1, pointer: image.Pt(-1, -1)}
	if err := a.rescale(s, bar.Scale()); err != nil {
		if closeErr := bar.Close(); closeErr != nil {
			log.Printf("closing the bar on %s: %v", out.Name, closeErr)
		}
		return nil, err
	}
	// The monitor's place in X's coordinates, so that a menu opened from
	// this bar lands on this monitor. A name XWayland does not know leaves
	// the offset at zero, which is right for one monitor and wrong only in
	// where a menu opens on a second -- a line in the log, not a reason to
	// refuse to start.
	if out.Name != "" {
		if s.mon = xmon.For(a.conn, a.host.Screen, out.Name); s.mon.Name == "" {
			log.Printf("XWayland does not report a monitor called %s, so menus from that bar may open on the wrong screen", out.Name)
		}
	}
	w, h := bar.Size()
	log.Printf("bar on %s: %dx%d logical at scale %.3g, %d device pixels wide", out.Name, w, h, bar.Scale(), s.width)
	return s, nil
}

// rescale points a screen at the artwork for a scale and re-measures it.
//
// It is what a monitor changing scale needs, and also what every bar needs
// once: wp_fractional_scale_v1 only says what a surface's real scale is
// after it has been mapped, so the first frame of a bar on a 1.5 monitor
// is the one that finds out.
func (a *app) rescale(s *screen, scale float64) error {
	art, err := a.artworkFor(scale)
	if err != nil {
		return err
	}
	s.m, s.art = newMetrics(scale), art
	if s.bar != nil {
		w, _ := s.bar.Size()
		s.width = int(float64(w)*scale + 0.5)
	}
	a.relayout(s)
	return nil
}

// syncScreens makes the set of bars match the set of monitors.
//
// Monitors come and go -- a screen is switched off, a laptop is
// undocked -- and the compositor says so. A bar left behind for a monitor
// that is gone is not merely useless: its surface is one the compositor
// has already closed, so every frame drawn on it fails.
func (a *app) syncScreens() {
	outs := a.wl.Outputs()
	have := make(map[uint32]bool, len(a.screens))
	for _, s := range a.screens {
		have[s.bar.Output()] = true
	}
	for _, out := range outs {
		if have[out.ID] {
			continue
		}
		s, err := a.newScreen(out)
		if err != nil {
			log.Printf("%v", err)
			continue
		}
		a.screens = append(a.screens, s)
	}

	want := make(map[uint32]bool, len(outs))
	for _, out := range outs {
		want[out.ID] = true
	}
	kept := a.screens[:0]
	for _, s := range a.screens {
		if want[s.bar.Output()] {
			kept = append(kept, s)
			continue
		}
		log.Printf("the monitor %s is gone; closing its bar", s.name)
		a.dropScreen(s)
	}
	a.screens = kept
}

// screenFor finds the bar an event is about.
func (a *app) screenFor(bar *wl.Bar) *screen {
	for _, s := range a.screens {
		if s.bar == bar {
			return s
		}
	}
	return nil
}

// dropScreen closes one bar's surface. The screen itself is dropped from
// the slice by the caller, which knows where it is.
func (a *app) dropScreen(s *screen) {
	if s.bar == nil {
		return
	}
	if err := s.bar.Close(); err != nil {
		log.Printf("closing the bar on %s: %v", s.name, err)
	}
}

// closeScreens takes every bar down, at exit.
func (a *app) closeScreens() {
	for _, s := range a.screens {
		a.dropScreen(s)
	}
	a.screens = nil
}

// relayoutAll re-measures every bar, after something all of them show
// changes: the clock, the focused application, the set of tray items.
func (a *app) relayoutAll() {
	for _, s := range a.screens {
		a.relayout(s)
	}
}

// paintDirty draws whichever bars have something new to show.
func (a *app) paintDirty() {
	for _, s := range a.screens {
		if !s.dirty {
			continue
		}
		if err := a.paint(s); err != nil {
			log.Printf("drawing the bar on %s: %v", s.name, err)
		}
	}
}

// errNoMonitors is what starting with nothing to draw on looks like.
var errNoMonitors = errors.New("the compositor reports no monitors, so there is nowhere to put a bar")
