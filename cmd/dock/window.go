package main

// Creating the two X windows the dock uses, and the ARGB visual that lets
// it be translucent.

import (
	"encoding/binary"
	"fmt"
	"log"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
)

// argbVisual is a 32-bit TrueColor visual and the depth it belongs to.
type argbVisual struct {
	id    xproto.Visualid
	depth byte
}

// findARGB looks for a 32-bit TrueColor visual on the screen.
//
// Without one the dock would have to be drawn on the root visual, where
// the fourth byte of each pixel is padding rather than alpha: the panel
// would get square opaque corners and no translucency at all. xfwm4's
// compositor is on here, so the visual exists; the error path matters
// anyway, because compositing is a setting the user can turn off.
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

// createDockWindow makes the window the dock draws into.
//
// It is override-redirect, which keeps the window manager out of its
// placement and stacking entirely. That is the right trade for a panel
// that slides itself in and out several times a minute: a managed window
// would have xfwm4's own opinions about position and workspace applied on
// top of ours, and the two would disagree during the slide. The cost is
// that nothing keeps it above other windows for us, which is what the
// VisibilityNotify handler in the event loop is for.
func (d *dockApp) createDockWindow(width, height, x, y int) error {
	win, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a window id: %w", err)
	}
	d.win = win

	// A 32-bit window in a 24-bit parent must be given its own colormap and
	// a border pixel; inheriting either is a BadMatch.
	cmap, err := xproto.NewColormapId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a colormap id: %w", err)
	}
	if cmapErr := xproto.CreateColormapChecked(d.conn, xproto.ColormapAllocNone,
		cmap, d.screen.Root, d.visual.id).Check(); cmapErr != nil {
		return fmt.Errorf("creating the colormap: %w", cmapErr)
	}
	d.colormap = cmap

	// The value list must be in mask-bit order: BackPixel (0x02),
	// BorderPixel (0x08), OverrideRedirect (0x200), EventMask (0x800),
	// Colormap (0x2000).
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	values := []uint32{
		0x00000000, // transparent background
		0x00000000,
		1, // override-redirect
		uint32(xproto.EventMaskExposure |
			xproto.EventMaskButtonPress |
			xproto.EventMaskPointerMotion |
			xproto.EventMaskEnterWindow |
			xproto.EventMaskLeaveWindow |
			xproto.EventMaskVisibilityChange),
		uint32(cmap),
	}
	err = xproto.CreateWindowChecked(d.conn, d.visual.depth, win, d.screen.Root,
		geom.I16(x), geom.I16(y), geom.U16(width), geom.U16(height), 0,
		xproto.WindowClassInputOutput, d.visual.id, mask, values).Check()
	if err != nil {
		return fmt.Errorf("creating the dock window: %w", err)
	}

	// Both are for other programs' benefit rather than the dock's, so a
	// failure is worth a line but not a failed start.
	if nameErr := d.nameWindow(win, "dock"); nameErr != nil {
		log.Printf("warning: %v", nameErr)
	}
	if typeErr := d.setDockType(win); typeErr != nil {
		log.Printf("warning: %v", typeErr)
	}
	return nil
}

// createTriggerWindow makes the invisible strip along the bottom of the
// screen that reveals the dock.
//
// It is InputOnly: it has no pixels at all, so it costs no memory in the
// server and can never be drawn. Its only job is to turn "the pointer
// reached the bottom edge" into an event.
//
// It reports the pointer leaving as well as arriving. The dock's own
// window is narrower than the screen, so a reveal from a point off to
// either side of it leaves the pointer on the trigger and nowhere else --
// and without a leave from here, nothing would ever tell the dock the
// pointer had gone, and it would stay up until something else happened to
// mention it.
func (d *dockApp) createTriggerWindow(width, height, x, y int) error {
	win, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a window id: %w", err)
	}
	d.trigger = win

	// InputOnly windows accept neither a background nor a colormap; asking
	// for either is a BadMatch.
	mask := uint32(xproto.CwOverrideRedirect | xproto.CwEventMask)
	values := []uint32{
		1,
		uint32(xproto.EventMaskEnterWindow | xproto.EventMaskLeaveWindow),
	}
	err = xproto.CreateWindowChecked(d.conn, 0, win, d.screen.Root,
		geom.I16(x), geom.I16(y), geom.U16(width), geom.U16(height), 0,
		xproto.WindowClassInputOnly, 0, mask, values).Check()
	if err != nil {
		return fmt.Errorf("creating the reveal trigger: %w", err)
	}
	if nameErr := d.nameWindow(win, "dock-trigger"); nameErr != nil {
		log.Printf("warning: %v", nameErr)
	}
	return nil
}

// nameWindow sets WM_NAME and WM_CLASS.
//
// An override-redirect window is invisible to the window manager, but not
// to xwininfo, xprop or a screen recorder, and an unnamed window is a
// nuisance to anybody debugging their own desktop.
func (d *dockApp) nameWindow(win xproto.Window, name string) error {
	if err := xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
		xproto.AtomWmName, xproto.AtomString, 8, geom.U32(len(name)), []byte(name)).Check(); err != nil {
		return fmt.Errorf("setting WM_NAME on %s: %w", name, err)
	}
	class := append([]byte(name+"\x00"), []byte("Osxflow\x00")...)
	if err := xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
		xproto.AtomWmClass, xproto.AtomString, 8, geom.U32(len(class)), class).Check(); err != nil {
		return fmt.Errorf("setting WM_CLASS on %s: %w", name, err)
	}
	return nil
}

// setDockType declares _NET_WM_WINDOW_TYPE_DOCK.
//
// The window manager ignores this on an override-redirect window, so it
// changes no behaviour. It is set because other software reads it: a
// screen-sharing tool or an accessibility client asking what this window
// is should get "a dock" rather than nothing.
func (d *dockApp) setDockType(win xproto.Window) error {
	typeAtom, err := atom(d.conn, "_NET_WM_WINDOW_TYPE")
	if err != nil {
		return fmt.Errorf("setting the window type: %w", err)
	}
	dockAtom, err := atom(d.conn, "_NET_WM_WINDOW_TYPE_DOCK")
	if err != nil {
		return fmt.Errorf("setting the window type: %w", err)
	}
	if err := xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
		typeAtom, xproto.AtomAtom, 32, 1, atomBytes(dockAtom)).Check(); err != nil {
		return fmt.Errorf("setting _NET_WM_WINDOW_TYPE: %w", err)
	}
	return nil
}

func atom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	if reply == nil {
		return 0, fmt.Errorf("interning %s: no reply from the X server", name)
	}
	return reply.Atom, nil
}

// atomBytes encodes an atom as the 32-bit little-endian word a property
// of format 32 carries.
func atomBytes(a xproto.Atom) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(a))
	return buf
}

// raise puts a window at the top of the stacking order. Override-redirect
// windows are not kept there by anybody else.
//
// Unchecked, because it is sent from the event handlers on the path the
// user is waiting on; a failure arrives later as an X error, which the
// event loop logs.
func (d *dockApp) raise(win xproto.Window) {
	xproto.ConfigureWindow(d.conn, win,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
}

// raiseNow is raise for a one-off, where waiting for the answer costs
// nothing and a failure is best reported where it happened.
func (d *dockApp) raiseNow(win xproto.Window) error {
	return xproto.ConfigureWindowChecked(d.conn, win,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check()
}

// moveWindow repositions a window without redrawing it, which is how the
// slide works: the panel's pixels do not change as it comes and goes, only
// where they are. Unchecked for the same reason as raise, and more so: it
// is sent on every frame of the slide.
func (d *dockApp) moveWindow(win xproto.Window, x, y int) {
	xproto.ConfigureWindow(d.conn, win,
		xproto.ConfigWindowX|xproto.ConfigWindowY,
		[]uint32{geom.I32AsU32(x), geom.I32AsU32(y)})
}
