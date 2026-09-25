package main

// The bar under Wayland.
//
// Everything else in menubar is unchanged there -- the tray is D-Bus, the
// menus are drawn through XWayland like every other osxflow menu, and the
// fonts and painting never knew what a display server was. Only the strip
// itself moves, because an X11 dock window is the one part that a Wayland
// compositor cannot give an honest answer to.
//
// It is worth being exact about what was wrong with it, because the
// obvious complaint is not the one that matters. labwc does honour
// _NET_WM_STRUT_PARTIAL from an XWayland dock: with the X11 bar up,
// _NET_WORKAREA on the XWayland root read 0,29,6000,1411, which is the
// strip reserved. What it cannot do is give that one window a sensible
// shape. The X screen is the union of every monitor -- 6000
// logical pixels across a 3440-wide ultrawide at scale 1 and a 4K panel at
// 1.5 -- so the bar is one strip at one scale spanning both, and in
// practice it was drawn across the left-hand monitor only, ending dead at
// that monitor's edge with the clock and the tray off the end of it.
//
// A layer-shell surface is per monitor by construction: it belongs to one
// wl_output, it is anchored to that output's edges, and its exclusive zone
// comes out of that output's usable area. wp_fractional_scale_v1 then says
// what scale the monitor really wants, so the picture is drawn at 1.5 and
// lands on the pixel grid instead of being stretched there by XWayland.

import (
	"image"
	"log"
	"slices"

	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/wl"
)

// namespace is what the compositor calls the bar in its own logs and
// configuration.
const namespace = "osxflow-menubar"

// wlEvents is the compositor's event stream, or nil when there is no
// compositor.
//
// A nil channel blocks forever in a select, which is exactly the wanted
// behaviour on X11: the case is present and never fires.
func (a *app) wlEvents() <-chan wl.Event {
	if a.wl == nil {
		return nil
	}
	return a.wl.Events()
}

// handleWayland does for the compositor's events what handleX does for X's.
//
// Every one of them but Focus and Monitor is about one bar, and is dropped
// when it names a bar that has already been closed -- which is ordinary
// rather than exceptional, since a monitor being unplugged and the events
// still in flight for it cross on the way.
func (a *app) handleWayland(e wl.Event) {
	switch e := e.(type) {
	case wl.Motion:
		// A menu open in front of the bar is an X window with its own
		// pointer grab; the bar must not also be highlighting under it.
		if s := a.screenFor(e.Bar); s != nil && !a.host.IsOpen() {
			a.hoverAt(s, image.Pt(e.X, e.Y))
		}
	case wl.Leave:
		if s := a.screenFor(e.Bar); s != nil {
			a.hoverAt(s, image.Pt(-1, -1))
		}
	case wl.Button:
		if !e.Pressed || a.host.IsOpen() {
			return
		}
		if s := a.screenFor(e.Bar); s != nil {
			a.press(s, image.Pt(e.X, e.Y), button(e.Button))
		}
	case wl.Configure:
		if s := a.screenFor(e.Bar); s != nil {
			// Width in device pixels: the configure is logical, and
			// everything laid out here is in the pixels drawn in.
			s.width = int(float64(e.Width)*s.m.scale + 0.5)
			a.relayout(s)
		}
	case wl.Scale:
		s := a.screenFor(e.Bar)
		if s == nil || e.Scale == s.m.scale {
			return
		}
		if err := a.rescale(s, e.Scale); err != nil {
			log.Printf("following %s to scale %.3g: %v", s.name, e.Scale, err)
		}
	case wl.Focus:
		a.updateApp()
	case wl.Monitor:
		a.syncScreens()
	case wl.Closed:
		s := a.screenFor(e.Bar)
		if s == nil {
			return
		}
		// The compositor has taken this one away. The others are still
		// there, and menubar is only finished when the last one goes.
		log.Printf("the compositor closed the bar on %s", s.name)
		a.dropScreen(s)
		a.screens = slices.DeleteFunc(a.screens, func(o *screen) bool { return o == s })
		if len(a.screens) == 0 {
			a.errs <- errNoMonitors
		}
	}
}

// button turns a kernel input code into the X button number the rest of
// menubar speaks, because everything a click leads to -- the menus, the
// tray's own protocol -- is X11 or D-Bus and counts buttons X's way.
func button(code uint32) xproto.Button {
	switch code {
	case wl.ButtonLeft:
		return xproto.ButtonIndex1
	case wl.ButtonLeft + 1: // BTN_RIGHT
		return xproto.ButtonIndex3
	case wl.ButtonLeft + 2: // BTN_MIDDLE
		return xproto.ButtonIndex2
	}
	return 0
}

// barToScreen is screenX's arithmetic: out of the bar's device pixels,
// back to logical ones, and across to where that monitor starts.
func barToScreen(x, monX int, scale float64) int {
	if scale <= 0 {
		scale = 1
	}
	return monX + int(float64(x)/scale+0.5)
}
