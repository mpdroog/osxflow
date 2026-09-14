package main

// The X11 plumbing the menu window needs: an ARGB visual, atoms, where the
// panel ends, and which key is Escape.

import (
	"errors"
	"fmt"
	"log"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// argbVisual is a 32-bit TrueColor visual and the depth it belongs to.
type argbVisual struct {
	id    xproto.Visualid
	depth byte
}

// findARGB looks for a 32-bit TrueColor visual. Without one the menu's
// corners would be square and opaque; with xfwm4's compositor on, as here,
// it exists.
func findARGB(screen *xproto.ScreenInfo) (argbVisual, bool) {
	for _, depth := range screen.AllowedDepths {
		if depth.Depth != 32 {
			continue
		}
		for _, v := range depth.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				return argbVisual{id: v.VisualId, depth: depth.Depth}, true
			}
		}
	}
	return argbVisual{}, false
}

func atom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	return reply.Atom, nil
}

func nameWindow(conn *xgb.Conn, win xproto.Window, name string) error {
	if err := xproto.ChangePropertyChecked(conn, xproto.PropModeReplace, win,
		xproto.AtomWmName, xproto.AtomString, 8, geom.U32(len(name)), []byte(name)).Check(); err != nil {
		return fmt.Errorf("setting WM_NAME on %s: %w", name, err)
	}
	class := []byte(name + "\x00Osxflow\x00")
	if err := xproto.ChangePropertyChecked(conn, xproto.PropModeReplace, win,
		xproto.AtomWmClass, xproto.AtomString, 8, geom.U32(len(class)), class).Check(); err != nil {
		return fmt.Errorf("setting WM_CLASS on %s: %w", name, err)
	}
	return nil
}

// menuTop is where the menu's top edge goes before the gap: the top of the
// work area, which is the bottom of the panel.
//
// The first desktop's area is used whatever desktop is current: the panel
// reserves the same strip on all of them, and the top edge is all that is
// needed. The answer is always usable -- the top of the screen when
// nothing better is known -- and what went wrong is logged.
func (a *app) menuTop() int {
	reply, err := xwin.GetProperty(a.conn, a.screen.Root, a.workareaAtom, "_NET_WORKAREA")
	switch {
	case errors.Is(err, xwin.ErrPropUnset):
		// No window manager, or one that reserves nothing for panels.
		return 0
	case err != nil:
		log.Printf("work area: %v; placing the menu at the top of the screen", err)
		return 0
	}
	vals, err := xwin.DecodeCard32s(reply)
	if err != nil {
		log.Printf("_NET_WORKAREA: %v; placing the menu at the top of the screen", err)
		return 0
	}
	if len(vals) < 4 {
		log.Printf("_NET_WORKAREA has %d values, want at least 4; placing the menu at the top of the screen", len(vals))
		return 0
	}
	top := int(vals[1])
	if limit := int(a.screen.HeightInPixels) / 2; top > limit {
		log.Printf("_NET_WORKAREA starts at y=%d, more than half way down; placing the menu at the top of the screen", top)
		return 0
	}
	return top
}

// keysymEscape is XK_Escape.
const keysymEscape = 0xff1b

// escapeKeycodes finds every key that produces Escape. There is normally
// one, but the keymap is the user's to change.
func escapeKeycodes(conn *xgb.Conn) ([]xproto.Keycode, error) {
	setup := xproto.Setup(conn)
	first := setup.MinKeycode
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	reply, err := xproto.GetKeyboardMapping(conn, first, count).Reply()
	if err != nil {
		return nil, fmt.Errorf("reading the keyboard mapping: %w", err)
	}
	per := int(reply.KeysymsPerKeycode)
	var codes []xproto.Keycode
	for i := byte(0); i < count; i++ {
		for j := range per {
			k := int(i)*per + j
			if k < len(reply.Keysyms) && reply.Keysyms[k] == keysymEscape {
				codes = append(codes, first+xproto.Keycode(i))
				break
			}
		}
	}
	if len(codes) == 0 {
		return nil, errors.New("no key produces Escape")
	}
	return codes, nil
}

// grabStatus names a grab status, which X reports as a bare number.
func grabStatus(status byte) string {
	switch status {
	case xproto.GrabStatusAlreadyGrabbed:
		return "another client has it grabbed"
	case xproto.GrabStatusInvalidTime:
		return "invalid time"
	case xproto.GrabStatusNotViewable:
		return "the menu is not viewable"
	case xproto.GrabStatusFrozen:
		return "it is frozen by another grab"
	}
	return fmt.Sprintf("status %d", status)
}
