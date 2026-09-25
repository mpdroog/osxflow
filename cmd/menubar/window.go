package main

// The bar's X window: a dock the window manager keeps on every workspace,
// above other windows, with the strip it covers reserved so that maximised
// windows stop below it.

import (
	"encoding/binary"
	"fmt"
	"image"

	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/menu"
)

// barEvents is what the bar window listens to.
const barEvents = xproto.EventMaskExposure | xproto.EventMaskButtonPress |
	xproto.EventMaskPointerMotion | xproto.EventMaskLeaveWindow

// allDesktops is _NET_WM_DESKTOP's value for "on every one".
const allDesktops = 0xffffffff

// newX11Screen makes and maps the bar window across the top of the screen,
// and returns the one screen an X11 session has.
//
// It is a managed window, unlike the menus and the dock, because the
// strut only means something to the window manager for a window it
// manages: xfwm4 reads _NET_WM_STRUT_PARTIAL from its clients, and an
// override-redirect window is not one.
func (a *app) newX11Screen(scale float64) (*screen, error) {
	art, err := a.artworkFor(scale)
	if err != nil {
		return nil, err
	}
	sc := &screen{m: newMetrics(scale), art: art, hover: -1, pointer: image.Pt(-1, -1)}

	width, height := int(a.host.Screen.WidthInPixels), sc.m.height
	win, err := a.host.NewManagedWindow(0, 0, width, height, namespace, uint32(barEvents))
	if err != nil {
		return nil, err
	}
	sc.win, sc.width = win, width
	a.relayout(sc)

	h := geom.U32(height)
	right := geom.U32(width - 1)
	for _, p := range []struct {
		name, typ string
		values    []string
		cards     []uint32
	}{
		{name: "_NET_WM_WINDOW_TYPE", typ: "ATOM", values: []string{"_NET_WM_WINDOW_TYPE_DOCK"}},
		{name: "_NET_WM_STATE", typ: "ATOM", values: []string{
			"_NET_WM_STATE_STICKY", "_NET_WM_STATE_ABOVE",
			"_NET_WM_STATE_SKIP_TASKBAR", "_NET_WM_STATE_SKIP_PAGER",
		}},
		{name: "_NET_WM_DESKTOP", typ: "CARDINAL", cards: []uint32{allDesktops}},
		// left, right, top, bottom
		{name: "_NET_WM_STRUT", typ: "CARDINAL", cards: []uint32{0, 0, h, 0}},
		// ... then the start and end of each: left y, right y, top x,
		// bottom x. Only the top one is used, and spans the screen.
		{name: "_NET_WM_STRUT_PARTIAL", typ: "CARDINAL", cards: []uint32{0, 0, h, 0, 0, 0, 0, 0, 0, right, 0, 0}},
	} {
		cards := p.cards
		if p.values != nil {
			if cards, err = a.atoms(p.values); err != nil {
				return nil, err
			}
		}
		if err := a.setProp32(win, p.name, p.typ, cards); err != nil {
			return nil, err
		}
	}

	if err := win.Map(); err != nil {
		return nil, err
	}
	// A window manager may place a new window as it sees fit; a dock
	// usually keeps its position, but asking again costs nothing.
	if err := xproto.ConfigureWindowChecked(a.conn, win.ID,
		xproto.ConfigWindowX|xproto.ConfigWindowY, []uint32{0, 0}).Check(); err != nil {
		return nil, fmt.Errorf("placing the bar: %w", err)
	}
	return sc, nil
}

// atoms interns names.
func (a *app) atoms(names []string) ([]uint32, error) {
	out := make([]uint32, len(names))
	for i, n := range names {
		atom, err := a.props.Atom(n)
		if err != nil {
			return nil, err
		}
		out[i] = uint32(atom)
	}
	return out, nil
}

// setProp32 sets a property of 32-bit values on the bar window.
func (a *app) setProp32(win *menu.Window, name, typ string, values []uint32) error {
	prop, err := a.props.Atom(name)
	if err != nil {
		return err
	}
	typeAtom, err := a.props.Atom(typ)
	if err != nil {
		return err
	}
	data := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(data[4*i:], v)
	}
	if err := xproto.ChangePropertyChecked(a.conn, xproto.PropModeReplace, win.ID, prop, typeAtom,
		32, geom.U32(len(values)), data).Check(); err != nil {
		return fmt.Errorf("setting %s on the bar: %w", name, err)
	}
	return nil
}
