package xwin

// The X11 implementation of Server, over pure-Go xgb/xgbutil. No cgo, and
// no shelling out to xprop or wmctrl either: this process is long-lived
// and holds one connection, so the subprocess-per-query approach gokeyd
// uses for focus tracking would be the wrong trade here.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"

	"github.com/mpdroog/osxflow/internal/xmon"
)

// propReader is the part of Props that describe needs, split out so the
// sorting of property failures can be tested against canned replies rather
// than a live display.
type propReader interface {
	Get(win xproto.Window, name string) (*xproto.GetPropertyReply, error)
	Atom(name string) (xproto.Atom, error)
	AtomName(atom xproto.Atom) (string, error)
}

type x11 struct {
	conn  *xgbutil.XUtil
	props propReader
	root  xproto.Window

	// owned records whether Close should drop the connection. A Server
	// built on somebody else's connection must not close it out from under
	// them.
	owned bool

	// logf reports the failures that are not worth failing a whole
	// enumeration for. It is log.Printf outside tests.
	logf func(format string, args ...any)

	mu sync.Mutex

	// reported holds the (window, property) pairs already logged as
	// malformed. A broken client breaks the same way every time it is
	// asked, and the dock asks on every window-list change, so each pair is
	// logged once for as long as the window exists. Windows prunes it to
	// the windows still present, which keeps it bounded and means a reused
	// window id is reported afresh.
	reported map[propKey]bool

	// stackingReported records that a broken _NET_CLIENT_LIST_STACKING has
	// been logged. It is a property of the window manager, not of any one
	// window, so once per process says everything there is to say.
	stackingReported bool
}

type propKey struct {
	win  xproto.Window
	name string
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
	x := &x11{
		conn:     conn,
		props:    NewProps(conn.Conn()),
		root:     conn.RootWin(),
		owned:    owned,
		logf:     log.Printf,
		reported: make(map[propKey]bool),
	}
	// _NET_CLIENT_LIST is the contract this whole package rests on. If the
	// window manager does not provide it we are not going to be able to
	// find or focus anything, and saying so now beats an empty app list
	// that looks like a matching bug.
	if _, err := x.windowList("_NET_CLIENT_LIST"); err != nil {
		if owned {
			conn.Conn().Close()
		}
		return nil, fmt.Errorf("is the window manager EWMH-compliant? %w", err)
	}
	return x, nil
}

// Windows lists managed windows bottom-to-top in stacking order.
//
// The order is load-bearing, not incidental. An app with three windows
// open should focus the one the user last used, and the top of the
// stacking order is the closest thing X offers to that. Falling back to
// _NET_CLIENT_LIST loses the ordering (it is age-ordered) but not the
// matching, which is the right way round to degrade.
//
// A window destroyed during the enumeration is left out. A window whose
// properties are malformed is listed with the ones that parsed, and the
// rest are logged rather than returned: the launcher reads any error from
// here as "cannot tell what is running" and starts a second copy of the
// app, which one broken client must not be able to cause. Only a failure
// of X itself comes back as an error.
func (x *x11) Windows() ([]Window, error) {
	ids, err := x.clientIDs()
	if err != nil {
		return nil, err
	}
	wins := make([]Window, 0, len(ids))
	for _, id := range ids {
		w, err := x.describe(id)
		if errors.Is(err, ErrWindowGone) {
			// Closed between _NET_CLIENT_LIST and the property read. It is
			// no longer open, so leaving it out is the correct answer.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("describing window 0x%x: %w", id, err)
		}
		wins = append(wins, w)
	}
	x.prune(ids)
	return wins, nil
}

// clientIDs reads the stacking-ordered client list, or the age-ordered one
// when the window manager does not keep the former.
func (x *x11) clientIDs() ([]xproto.Window, error) {
	ids, stackErr := x.windowList("_NET_CLIENT_LIST_STACKING")
	if stackErr == nil {
		return ids, nil
	}
	ids, listErr := x.windowList("_NET_CLIENT_LIST")
	if listErr != nil {
		return nil, errors.Join(stackErr, listErr)
	}
	// Not keeping a stacking list at all is the window manager's right.
	// Keeping a broken one is worth saying, once.
	if !errors.Is(stackErr, ErrPropUnset) {
		x.mu.Lock()
		first := !x.stackingReported
		x.stackingReported = true
		x.mu.Unlock()
		if first {
			x.logf("xwin: %v; using _NET_CLIENT_LIST, which is not in stacking order", stackErr)
		}
	}
	return ids, nil
}

// windowList reads one of the root window's client lists.
func (x *x11) windowList(name string) ([]xproto.Window, error) {
	r, err := x.props.Get(x.root, name)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	ids, err := DecodeWindows(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	return ids, nil
}

// describe reads the properties of one window, tolerating every one of
// them being absent.
//
// Each property can fail in three ways, and they are told apart because
// they want different answers. Absent is normal and leaves the field
// zero. Malformed is the client's fault: it is logged, that field stays
// zero, and every other field is still read. A BadWindow means the window
// was destroyed under us, which costs that one window, not the whole
// enumeration -- closing a window while the launcher is open is not
// exotic, it is Alt+F4 -- and comes back as ErrWindowGone. Anything else
// is X itself failing, and is returned.
func (x *x11) describe(id xproto.Window) (Window, error) {
	w := Window{ID: uint32(id)}

	// _NET_WM_PID is a 32-bit CARDINAL and is decoded as exactly that, so
	// no out-of-range value can be truncated into somebody else's pid.
	if err := x.read(id, "_NET_WM_PID", func(r *xproto.GetPropertyReply) error {
		pid, err := DecodeCard32(r)
		w.PID = pid
		return err
	}); err != nil {
		return w, err
	}

	if err := x.read(id, "WM_CLASS", func(r *xproto.GetPropertyReply) error {
		class, err := DecodeStrings(r)
		if err != nil {
			return err
		}
		if len(class) != 2 {
			return fmt.Errorf("%w: WM_CLASS is two strings, got %d (%q)", ErrPropMalformed, len(class), class)
		}
		w.Instance, w.Class = class[0], class[1]
		return nil
	}); err != nil {
		return w, err
	}

	// The window type decides whether this is an application window at
	// all. Only the first atom is read: the property is a list in
	// most-preferred-first order, and the rest are fallbacks for a window
	// manager that does not understand the first.
	if err := x.read(id, "_NET_WM_WINDOW_TYPE", func(r *xproto.GetPropertyReply) error {
		types, err := DecodeAtoms(r)
		if err != nil {
			return err
		}
		if len(types) == 0 {
			return nil // declared, but empty: the same as no type
		}
		name, err := x.props.AtomName(types[0])
		var bogus xproto.AtomError
		if errors.As(err, &bogus) {
			// The server has never heard of the atom, so the client wrote
			// a number that is not one.
			return fmt.Errorf("%w: %w", ErrPropMalformed, err)
		}
		if err != nil {
			return err
		}
		w.Type = strings.TrimPrefix(name, "_NET_WM_WINDOW_TYPE_")
		return nil
	}); err != nil {
		return w, err
	}

	skip, err := x.props.Atom("_NET_WM_STATE_SKIP_TASKBAR")
	if err != nil {
		return w, err
	}
	if err := x.read(id, "_NET_WM_STATE", func(r *xproto.GetPropertyReply) error {
		states, err := DecodeAtoms(r)
		if err != nil {
			return err
		}
		for _, state := range states {
			if state == skip {
				w.SkipTaskbar = true
				break
			}
		}
		return nil
	}); err != nil {
		return w, err
	}

	// _NET_WM_NAME is the UTF-8 one and what every modern client sets;
	// WM_NAME is the latin-1 fallback for the ones that do not. A
	// _NET_WM_NAME that is empty or malformed (and logged) falls back too:
	// the title is only for display, and some title beats none.
	if err := x.read(id, "_NET_WM_NAME", func(r *xproto.GetPropertyReply) error {
		title, err := DecodeString(r)
		w.Title = title
		return err
	}); err != nil {
		return w, err
	}
	if w.Title == "" {
		if err := x.read(id, "WM_NAME", func(r *xproto.GetPropertyReply) error {
			title, err := DecodeString(r)
			w.Title = title
			return err
		}); err != nil {
			return w, err
		}
	}
	return w, nil
}

// read fetches one property of a window and hands it to decode.
//
// An absent property returns nil without calling decode, and a malformed
// one is logged and also returns nil, so that describe carries on to the
// next property; only the errors describe must act on -- ErrWindowGone
// and failures of X itself -- are returned.
func (x *x11) read(id xproto.Window, name string, decode func(*xproto.GetPropertyReply) error) error {
	r, err := x.props.Get(id, name)
	if errors.Is(err, ErrPropUnset) {
		return nil
	}
	if err != nil {
		return err
	}
	err = decode(r)
	if errors.Is(err, ErrPropMalformed) {
		x.malformed(id, name, err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("decoding %s on window 0x%x: %w", name, id, err)
	}
	return nil
}

// malformed logs a broken property, once per window and property.
func (x *x11) malformed(id xproto.Window, name string, err error) {
	key := propKey{win: id, name: name}
	x.mu.Lock()
	seen := x.reported[key]
	if x.reported == nil {
		x.reported = make(map[propKey]bool)
	}
	x.reported[key] = true
	x.mu.Unlock()
	if !seen {
		x.logf("xwin: window 0x%x: %s: %v", id, name, err)
	}
}

// prune forgets the malformed-property reports for windows that are gone.
func (x *x11) prune(present []xproto.Window) {
	live := make(map[xproto.Window]bool, len(present))
	for _, id := range present {
		live[id] = true
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for key := range x.reported {
		if !live[key.win] {
			delete(x.reported, key)
		}
	}
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
	// The ClientMessage goes out as a checked SendEvent, and checking it
	// is a round trip: by the time this returns, the request has been
	// flushed and the server has accepted or rejected it. No separate
	// flush is needed.
	if err := ewmh.ActiveWindowReqExtra(x.conn, xproto.Window(id), 2, 0, 0); err != nil {
		return fmt.Errorf("activating window 0x%x: %w", id, err)
	}
	return nil
}

// ActiveMonitor names the monitor holding the focused window.
//
// _NET_ACTIVE_WINDOW rather than the pointer, and the window's centre
// rather than its origin: a window straddling two monitors belongs to the
// one showing most of it, which is the one its middle is on.
//
// Every way of not knowing answers "" and no error, because they are all
// ordinary. Nothing is focused on a freshly started desktop; the focused
// window may be destroyed between the two requests; a window manager need
// not keep the property at all. Only X itself failing is an error.
func (x *x11) ActiveMonitor() (string, error) {
	ids, err := x.windowList("_NET_ACTIVE_WINDOW")
	if errors.Is(err, ErrPropUnset) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(ids) == 0 || ids[0] == 0 {
		return "", nil // None: the desktop itself has the keyboard
	}
	cx, cy, err := x.centre(ids[0])
	if errors.Is(err, ErrWindowGone) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	conn := x.conn.Conn()
	m, ok := xmon.Containing(xmon.All(conn, xproto.Setup(conn).DefaultScreen(conn)), cx, cy)
	if !ok {
		return "", nil // off the side of every monitor, which RandR allows
	}
	return m.Name, nil
}

// centre is the middle of a window, in root coordinates.
//
// The geometry a window reports is relative to its parent, which under a
// reparenting window manager is the frame and not the root, so the origin
// has to be translated rather than read. A window destroyed in between
// comes back wrapping ErrWindowGone.
func (x *x11) centre(win xproto.Window) (int, int, error) {
	conn := x.conn.Conn()
	geom, err := xproto.GetGeometry(conn, xproto.Drawable(win)).Reply()
	if err != nil {
		return 0, 0, replyError("the geometry", win, err)
	}
	if geom == nil {
		return 0, 0, fmt.Errorf("reading the geometry of window 0x%x: no reply from the X server", win)
	}
	at, err := xproto.TranslateCoordinates(conn, win, x.root, 0, 0).Reply()
	if err != nil {
		return 0, 0, replyError("the position", win, err)
	}
	if at == nil {
		return 0, 0, fmt.Errorf("reading the position of window 0x%x: no reply from the X server", win)
	}
	return int(at.DstX) + int(geom.Width)/2, int(at.DstY) + int(geom.Height)/2, nil
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
