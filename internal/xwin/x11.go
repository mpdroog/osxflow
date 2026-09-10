package xwin

// The X11 implementation of Server, over pure-Go xgb/xgbutil. No cgo, and
// no shelling out to xprop or wmctrl either: this process is long-lived
// and holds one connection, so the subprocess-per-query approach gokeyd
// uses for focus tracking would be the wrong trade here.

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/icccm"
)

type x11 struct {
	conn *xgbutil.XUtil

	// owned records whether Close should drop the connection. A Server
	// built on somebody else's connection must not close it out from under
	// them.
	owned bool
}

// NewX11 connects to the display named by DISPLAY.
func NewX11() (Server, error) {
	conn, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	return newX11(conn, true)
}

// NewX11With builds a Server on a connection the caller already has.
//
// The dock holds a connection of its own for drawing and needs the window
// list on the same one: two connections to the same display would work,
// but events and requests on them interleave unpredictably, and the dock
// reacts to a property change by immediately asking what changed.
//
// The returned Server does not close the connection, because it does not
// own it.
func NewX11With(conn *xgbutil.XUtil) (Server, error) {
	return newX11(conn, false)
}

func newX11(conn *xgbutil.XUtil, owned bool) (Server, error) {
	// _NET_CLIENT_LIST is the contract this whole package rests on. If the
	// window manager does not provide it we are not going to be able to
	// find or focus anything, and saying so now beats an empty app list
	// that looks like a matching bug.
	if _, err := ewmh.ClientListGet(conn); err != nil {
		if owned {
			conn.Conn().Close()
		}
		return nil, fmt.Errorf("reading _NET_CLIENT_LIST (is the window manager EWMH-compliant?): %w", err)
	}
	return &x11{conn: conn, owned: owned}, nil
}

// Windows lists managed windows bottom-to-top in stacking order.
//
// The order is load-bearing, not incidental. An app with three windows
// open should focus the one the user last used, and the top of the
// stacking order is the closest thing X offers to that. Falling back to
// _NET_CLIENT_LIST loses the ordering (it is age-ordered) but not the
// matching, which is the right way round to degrade.
func (x *x11) Windows() ([]Window, error) {
	ids, err := ewmh.ClientListStackingGet(x.conn)
	if err != nil {
		if ids, err = ewmh.ClientListGet(x.conn); err != nil {
			return nil, fmt.Errorf("reading _NET_CLIENT_LIST: %w", err)
		}
	}
	wins := make([]Window, 0, len(ids))
	for _, id := range ids {
		wins = append(wins, x.describe(id))
	}
	return wins, nil
}

// describe reads the properties of one window, tolerating every one of
// them being absent.
//
// Nothing here returns an error. A window can be destroyed between
// _NET_CLIENT_LIST and the property read -- closing a window while the
// launcher is open is not exotic, it is Alt+F4 -- and the resulting
// BadWindow must cost that one window, not the whole enumeration.
func (x *x11) describe(id xproto.Window) Window {
	w := Window{ID: uint32(id)}

	// _NET_WM_PID is a CARDINAL, which xgbutil widens to uint. A pid does
	// not exceed 32 bits on Linux (pid_max is capped well below it), but a
	// hostile or broken client can put any 32-bit value in the property,
	// and on a 64-bit build uint is wider than that. Anything that will
	// not fit is treated as absent rather than truncated into a pid that
	// belongs to somebody else.
	if pid, err := ewmh.WmPidGet(x.conn, id); err == nil && pid <= math.MaxUint32 {
		w.PID = uint32(pid)
	}
	if class, err := icccm.WmClassGet(x.conn, id); err == nil && class != nil {
		w.Instance, w.Class = class.Instance, class.Class
	}
	// The window type decides whether this is an application window at
	// all. Only the first atom is read: the property is a list in
	// most-preferred-first order, and the rest are fallbacks for a window
	// manager that does not understand the first.
	if types, err := ewmh.WmWindowTypeGet(x.conn, id); err == nil && len(types) > 0 {
		w.Type = strings.TrimPrefix(types[0], "_NET_WM_WINDOW_TYPE_")
	}
	if states, err := ewmh.WmStateGet(x.conn, id); err == nil {
		for _, state := range states {
			if state == "_NET_WM_STATE_SKIP_TASKBAR" {
				w.SkipTaskbar = true
				break
			}
		}
	}

	// _NET_WM_NAME is the UTF-8 one and what every modern client sets;
	// WM_NAME is the latin-1 fallback for the ones that do not.
	if title, err := ewmh.WmNameGet(x.conn, id); err == nil && title != "" {
		w.Title = title
	} else if title, err := icccm.WmNameGet(x.conn, id); err == nil {
		w.Title = title
	}
	return w
}

// Activate asks the window manager to focus a window.
//
// The request is sent with source indication 2 ("pager"). That is not
// cosmetic: with source 1 ("application") a window manager applying
// focus-stealing prevention is entitled to flash the taskbar entry
// instead of raising the window, which would make the launcher look
// broken exactly when it is doing its job. A pager is by definition
// acting for the user, so the request is honoured.
//
// Raising, deiconifying and switching workspace are all the window
// manager's business once it accepts this; xfwm4 does all three.
func (x *x11) Activate(id uint32) error {
	if err := ewmh.ActiveWindowReqExtra(x.conn, xproto.Window(id), 2, 0, 0); err != nil {
		return fmt.Errorf("activating window 0x%x: %w", id, err)
	}
	// The request is a ClientMessage on the root window; without a flush
	// it sits in the output buffer until something else happens to send.
	x.conn.Sync()
	return nil
}

func (x *x11) Close() error {
	if x.conn == nil {
		return errors.New("already closed")
	}
	if x.owned {
		x.conn.Conn().Close()
	}
	x.conn = nil
	return nil
}
