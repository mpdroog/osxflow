// Package ui draws the launcher window and runs its event loop.
//
// The window is override-redirect, so the window manager does not decorate
// it, position it, or give it focus -- which is why the keyboard is
// grabbed explicitly below. That is the conventional arrangement for a
// launcher: it is not really a window in the desktop sense, it is a
// transient overlay that owns the keyboard until it is dismissed.
package ui

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/keybind"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/search"
)

// OpenFunc is called when the user accepts an application. It is given the
// query that found it, so the caller can remember the pairing.
//
// Returning an error keeps the window open and shows the message, on the
// theory that a launcher which vanishes after failing to launch something
// is indistinct from one that worked. The error is also logged, here, so
// the OpenFunc should return it rather than log it itself.
type OpenFunc func(app *desktop.App, query string) error

// Run shows the launcher. scaleOverride forces a display scale; pass 0 to
// detect one. ranker may be nil.
func Run(apps []desktop.App, onOpen OpenFunc, scaleOverride float64, ranker search.Ranker) (err error) {
	u, err := newUI(apps, onOpen, scaleOverride, ranker)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := u.close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing the window: %w", cerr))
		}
	}()
	return u.loop()
}

// xErrLog reports X errors that arrive in the event loop. It is keyed by
// error type, so a request that fails on every keystroke is one line a
// minute, and a second kind of failure still gets through.
var xErrLog = &errlog.Limiter{Burst: 3, Per: time.Minute}

// errConnClosed ends the event loop when the server goes away.
var errConnClosed = errors.New("the connection to the X server closed")

type ui struct {
	X     *xgbutil.XUtil
	conn  *xgb.Conn
	surf  *surface
	faces *faces
	model *Model
	open  OpenFunc
	mt    *metrics

	// status replaces the result list when set, to report a failure the
	// user has to see.
	status string

	win xproto.Window

	// height is the window height currently configured on the server.
	height int

	// pointerGrabbed records whether click-to-dismiss is available, so
	// close does not ungrab something it never held.
	pointerGrabbed bool

	// connClosed is set when the server has gone. close then sends it
	// nothing: every request would fail, and the server has already freed
	// everything this client held.
	connClosed bool
}

func newUI(apps []desktop.App, onOpen OpenFunc, scaleOverride float64, ranker search.Ranker) (*ui, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	keybind.Initialize(X)

	// The factor is always usable (1 when nothing answers); the error says
	// why it may not match the desktop, which is worth a line but not a
	// failed start.
	factor, scaleErr := DetectScale(X, scaleOverride)
	if scaleErr != nil {
		log.Printf("scale: %v", scaleErr)
	}
	mt := newMetrics(factor)

	faces, err := loadFaces(mt)
	if err != nil {
		X.Conn().Close()
		return nil, err
	}

	u := &ui{
		X:     X,
		conn:  X.Conn(),
		faces: faces,
		mt:    mt,
		model: NewModel(apps, visibleRows, ranker),
		open:  onOpen,
	}
	if err := u.createWindow(); err != nil {
		// Whatever part of the window was created goes with the
		// connection: the server frees a client's resources when it
		// disconnects.
		closeErr := faces.close()
		X.Conn().Close()
		return nil, errors.Join(err, closeErr)
	}
	return u, nil
}

func (u *ui) createWindow() error {
	screen := xproto.Setup(u.conn).DefaultScreen(u.conn)

	// Horizontally centred, and a quarter of the way down rather than
	// vertically centred: a panel in the exact middle of the screen sits
	// lower than it looks like it should.
	x := (int(screen.WidthInPixels) - u.mt.windowWidth) / 2
	y := int(screen.HeightInPixels) / 4

	win, err := xproto.NewWindowId(u.conn)
	if err != nil {
		return fmt.Errorf("allocating a window id: %w", err)
	}
	u.win = win

	// The value list must be in mask-bit order: BackPixel (0x02),
	// OverrideRedirect (0x200), EventMask (0x800).
	mask := uint32(xproto.CwBackPixel | xproto.CwOverrideRedirect | xproto.CwEventMask)
	values := []uint32{
		0x00000000,
		1, // override-redirect: keep the window manager out of this
		uint32(xproto.EventMaskExposure | xproto.EventMaskKeyPress | xproto.EventMaskButtonPress),
	}
	err = xproto.CreateWindowChecked(u.conn, screen.RootDepth, win, screen.Root,
		i16(x), i16(y), u16(u.mt.windowWidth), u16(u.mt.windowHeight), 0,
		xproto.WindowClassInputOutput, screen.RootVisual, mask, values).Check()
	if err != nil {
		return fmt.Errorf("creating the window: %w", err)
	}

	// Even an override-redirect window should say what it is: it shows up
	// in xwininfo and in screen recordings, and an unnamed one is a
	// nuisance to anybody debugging their desktop.
	if nameErr := u.setName(); nameErr != nil {
		return nameErr
	}

	u.surf, err = newSurface(u.conn, win, screen.RootDepth, u.mt.windowWidth, u.mt.windowHeight)
	if err != nil {
		return err
	}
	u.height = u.mt.windowHeight

	if err := xproto.MapWindowChecked(u.conn, win).Check(); err != nil {
		return fmt.Errorf("mapping the window: %w", err)
	}
	if err := u.grabKeyboard(); err != nil {
		return err
	}
	u.grabPointer()
	return nil
}

func (u *ui) setName() error {
	const name = "osxflow"
	wmClass := append([]byte("osxflow\x00"), []byte("Osxflow\x00")...)
	if err := xproto.ChangePropertyChecked(u.conn, xproto.PropModeReplace, u.win,
		xproto.AtomWmName, xproto.AtomString, 8, u32(len(name)), []byte(name)).Check(); err != nil {
		return fmt.Errorf("setting WM_NAME: %w", err)
	}
	if err := xproto.ChangePropertyChecked(u.conn, xproto.PropModeReplace, u.win,
		xproto.AtomWmClass, xproto.AtomString, 8, u32(len(wmClass)), wmClass).Check(); err != nil {
		return fmt.Errorf("setting WM_CLASS: %w", err)
	}
	return nil
}

// grabKeyboard takes exclusive keyboard input.
//
// The retry loop is not defensive padding. The launcher is started from a
// global hotkey, and at the moment it runs the key that triggered it may
// still be held, leaving the window manager holding its own grab; the
// server answers AlreadyGrabbed until that clears. Failing immediately
// would make the launcher appear with a dead keyboard perhaps one time in
// twenty, which is exactly the kind of intermittent fault nobody can
// reproduce on request.
func (u *ui) grabKeyboard() error {
	deadline := time.Now().Add(500 * time.Millisecond)
	for attempt := 0; ; attempt++ {
		reply, err := xproto.GrabKeyboard(u.conn, true, u.win, xproto.TimeCurrentTime,
			xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
		if err != nil {
			return fmt.Errorf("grabbing the keyboard: %w", err)
		}
		if reply == nil {
			// xgb's answer when the connection closed before the reply came.
			return fmt.Errorf("grabbing the keyboard: %w", errConnClosed)
		}
		if reply.Status == xproto.GrabStatusSuccess {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not grab the keyboard after %d attempts (status %d); "+
				"something else is holding it", attempt+1, reply.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// grabPointer takes pointer input so that a click anywhere -- including
// outside this window -- is delivered here and can dismiss the launcher.
//
// Failure is not fatal, and is logged rather than returned. Losing the
// pointer grab costs click-to-dismiss; the keyboard still works and Escape
// still closes the window, so refusing to start over it would trade a
// whole launcher for a convenience. It runs once per launcher, so it logs
// at most once.
//
// owner_events is false, which is what makes clicks outside the window
// arrive at all: with it true they would go to whichever window is under
// the cursor and never reach us.
func (u *ui) grabPointer() {
	reply, err := xproto.GrabPointer(u.conn, false, u.win,
		uint16(xproto.EventMaskButtonPress),
		xproto.GrabModeAsync, xproto.GrabModeAsync,
		xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
	switch {
	case err != nil:
		log.Printf("click-to-dismiss unavailable: grabbing the pointer: %v", err)
	case reply == nil:
		log.Printf("click-to-dismiss unavailable: grabbing the pointer: %v", errConnClosed)
	case reply.Status != xproto.GrabStatusSuccess:
		log.Printf("click-to-dismiss unavailable: something else holds the pointer (grab status %d)", reply.Status)
	default:
		u.pointerGrabbed = true
	}
}

// errDismissed is the ordinary way out, and is not reported to the user.
var errDismissed = errors.New("dismissed")

func (u *ui) loop() error {
	if err := u.repaint(); err != nil {
		return err
	}
	for {
		ev, xerr := u.conn.WaitForEvent()
		if xerr != nil {
			// A protocol error here is not fatal: it refers to a request
			// that already failed, and the loop can carry on. Every request
			// this file sends is checked where it is sent, so one arriving
			// here is a surprise, and is logged.
			xErrLog.Printf(fmt.Sprintf("%T", xerr), "X error: %v", xerr)
			continue
		}
		if ev == nil {
			// The connection closed under us -- the X server went away, or
			// the session ended. That is not a dismissal: the launcher did
			// not do what it was opened for, so it is an error.
			u.connClosed = true
			return errConnClosed
		}

		switch e := ev.(type) {
		case xproto.KeyPressEvent:
			done, err := u.key(e)
			if err != nil {
				if errors.Is(err, errDismissed) {
					return nil
				}
				return err
			}
			if done {
				return nil
			}
			if err := u.repaint(); err != nil {
				return err
			}
		case xproto.ButtonPressEvent:
			if u.button(e) {
				return nil
			}
			if err := u.repaint(); err != nil {
				return err
			}
		case xproto.ExposeEvent:
			// Only the last expose of a burst needs acting on; the others
			// describe rectangles of a frame that is about to be redrawn
			// in full anyway.
			if e.Count == 0 {
				if err := u.surf.copyToWindow(); err != nil {
					return err
				}
			}
		}
	}
}

// key applies one key press, reporting whether the interface is finished.
func (u *ui) key(e xproto.KeyPressEvent) (done bool, err error) {
	act, text := interpret(u.X, e.State, e.Detail)

	// Any key clears a previous failure message: the user has moved on.
	if act != actNone {
		u.status = ""
	}

	switch act {
	case actCancel:
		return true, nil
	case actAccept:
		return u.accept()
	case actInsert:
		u.model.Insert(text)
	case actBackspace:
		u.model.Backspace()
	case actDeleteWord:
		u.model.DeleteWord()
	case actClear:
		u.model.Clear()
	case actUp:
		u.model.Move(-1)
	case actDown:
		u.model.Move(1)
	case actPageUp:
		u.model.Move(-visibleRows)
	case actPageDown:
		u.model.Move(visibleRows)
	case actTop:
		u.model.Move(-u.model.TotalRows())
	case actBottom:
		u.model.Move(u.model.TotalRows())
	case actNone:
	}
	return false, nil
}

// button applies a click, reporting whether the interface is finished.
//
// Coordinates are relative to the grab window -- this one -- so a click
// elsewhere on the screen arrives with coordinates outside its bounds.
// That is the whole test.
func (u *ui) button(e xproto.ButtonPressEvent) (done bool) {
	const (
		scrollUp   = 4
		scrollDown = 5
	)
	switch e.Detail {
	case scrollUp:
		u.model.Move(-1)
		return false
	case scrollDown:
		u.model.Move(1)
		return false
	}

	x, y := int(e.EventX), int(e.EventY)
	if !u.mt.contains(x, y, u.height) {
		return true // clicked away: dismiss
	}

	// Inside: move the selection to the row under the cursor. Clicking
	// deliberately does not launch. A launcher is a keyboard tool, the
	// pointer is grabbed while it is open, and a stray click launching
	// something is a worse failure than one that does nothing.
	u.status = ""
	if row := u.mt.rowAt(y); row >= 0 {
		if _, sel := u.model.Rows(); sel >= 0 {
			u.model.Move(row - sel)
		}
	}
	return false
}

func (u *ui) accept() (done bool, err error) {
	app, ok := u.model.Selected()
	if !ok {
		// The calculator row, or an empty list. Neither is something to
		// launch, and closing the window would throw away a result the
		// user is still reading.
		return false, nil
	}
	if u.open == nil {
		return true, nil
	}
	if err := u.open(app, u.model.Query()); err != nil {
		// Deliberately not propagated: a launcher that vanishes after
		// failing to start something is indistinguishable from one that
		// worked. The message is shown and the window stays open. It is
		// logged here too -- this is the one place that does, see
		// OpenFunc -- so it outlives the window.
		log.Print(err)
		u.status = err.Error()
		return false, nil
	}
	return true, nil
}

func (u *ui) repaint() error {
	rows := u.model.TotalRows()
	if u.status != "" {
		rows = max(rows, 1) // the message needs a row to live in
	}
	height := u.mt.heightFor(rows)
	if height != u.height {
		// Checked, so a round trip on the keystrokes that change the
		// number of rows. That is imperceptible, and an unchecked resize
		// that failed would leave rows drawn below the window's edge with
		// nothing said.
		if err := xproto.ConfigureWindowChecked(u.conn, u.win,
			xproto.ConfigWindowHeight, []uint32{u32(height)}).Check(); err != nil {
			return fmt.Errorf("resizing the window to %dpx: %w", height, err)
		}
		u.height = height
	}

	paint(u.surf.image(), u.faces, u.mt, u.model)
	if u.status != "" {
		drawStatus(u.surf.image(), u.faces, u.mt, u.status)
	}
	return u.surf.flush(height)
}

// close releases the grabs and the window, then disconnects.
//
// The server would do all of this itself on disconnect, which follows
// immediately. The requests are sent anyway so the grabs are gone the
// moment the launcher is done rather than whenever the process gets round
// to exiting, and they are checked because a request sent unchecked right
// before Close can never report anything: its error would arrive on a
// connection nobody reads any more.
func (u *ui) close() error {
	var errs []error
	if !u.connClosed {
		if err := xproto.UngrabKeyboardChecked(u.conn, xproto.TimeCurrentTime).Check(); err != nil {
			errs = append(errs, fmt.Errorf("releasing the keyboard: %w", err))
		}
		if u.pointerGrabbed {
			if err := xproto.UngrabPointerChecked(u.conn, xproto.TimeCurrentTime).Check(); err != nil {
				errs = append(errs, fmt.Errorf("releasing the pointer: %w", err))
			}
		}
		if u.surf != nil {
			if err := u.surf.close(); err != nil {
				errs = append(errs, err)
			}
		}
		if u.win != 0 {
			if err := xproto.DestroyWindowChecked(u.conn, u.win).Check(); err != nil {
				errs = append(errs, fmt.Errorf("destroying the window: %w", err))
			}
		}
	}
	if u.faces != nil {
		if err := u.faces.close(); err != nil {
			errs = append(errs, err)
		}
	}
	u.conn.Close()
	return errors.Join(errs...)
}
