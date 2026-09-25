package main

// The banners' X windows, and what the daemon needs to know about the
// screen they sit on.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log"
	"math"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/xmon"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// argbVisual is a 32-bit TrueColor visual and the depth it belongs to.
type argbVisual struct {
	id    xproto.Visualid
	depth byte
}

// findARGB looks for a 32-bit TrueColor visual, which is what makes a
// translucent banner with rounded corners possible at all: on the root
// visual the fourth byte of a pixel is padding, not alpha.
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

type atoms struct {
	workarea       xproto.Atom
	currentDesktop xproto.Atom
	windowType     xproto.Atom
	notification   xproto.Atom
}

// setupX interns the atoms, creates the colormap every banner shares, and
// reads the work area.
func (d *daemon) setupX() error {
	for _, a := range []struct {
		dst  *xproto.Atom
		name string
	}{
		{&d.atoms.workarea, "_NET_WORKAREA"},
		{&d.atoms.currentDesktop, "_NET_CURRENT_DESKTOP"},
		{&d.atoms.windowType, "_NET_WM_WINDOW_TYPE"},
		{&d.atoms.notification, "_NET_WM_WINDOW_TYPE_NOTIFICATION"},
	} {
		id, err := atom(d.conn, a.name)
		if err != nil {
			return err
		}
		*a.dst = id
	}

	// One colormap for every banner: a 32-bit window in a 24-bit parent
	// needs one of its own, but nothing says it cannot be shared.
	cmap, err := xproto.NewColormapId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a colormap id: %w", err)
	}
	if cmapErr := xproto.CreateColormapChecked(d.conn, xproto.ColormapAllocNone,
		cmap, d.screen.Root, d.visual.id).Check(); cmapErr != nil {
		return fmt.Errorf("creating the colormap: %w", cmapErr)
	}
	d.colormap = cmap

	d.updateWorkArea()
	// The root window reports the work area changing -- a panel added,
	// moved or resized -- so that banners follow it. Without that they
	// stay where the panel used to end, which is survivable, and so worth
	// a line rather than a failed start.
	if watchErr := xproto.ChangeWindowAttributesChecked(d.conn, d.screen.Root,
		xproto.CwEventMask, []uint32{uint32(xproto.EventMaskPropertyChange)}).Check(); watchErr != nil {
		log.Printf("not watching the work area; banners will not follow panel changes: %v", watchErr)
	}
	return nil
}

// createWindow makes one banner's window.
//
// Override-redirect, like the dock: the window manager has no business
// placing, decorating or focusing a notification, and a managed window
// would get all three, plus a slot in the taskbar. It declares itself a
// notification anyway, for the benefit of anything else that asks.
func (d *daemon) createWindow(x, y, w, h int) (xproto.Window, error) {
	win, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return 0, fmt.Errorf("allocating a window id: %w", err)
	}
	// In mask-bit order: BackPixel, BorderPixel, OverrideRedirect,
	// EventMask, Colormap.
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	values := []uint32{
		0x00000000,
		0x00000000,
		1,
		uint32(xproto.EventMaskExposure |
			xproto.EventMaskButtonPress |
			xproto.EventMaskPointerMotion |
			xproto.EventMaskEnterWindow |
			xproto.EventMaskLeaveWindow |
			xproto.EventMaskVisibilityChange),
		uint32(d.colormap),
	}
	err = xproto.CreateWindowChecked(d.conn, d.visual.depth, win, d.screen.Root,
		geom.I16(x), geom.I16(y), geom.U16(w), geom.U16(h), 0,
		xproto.WindowClassInputOutput, d.visual.id, mask, values).Check()
	if err != nil {
		return 0, fmt.Errorf("creating a banner window: %w", err)
	}

	const name = "notifyd"
	class := []byte(name + "\x00Osxflow\x00")
	typ := make([]byte, 4)
	binary.LittleEndian.PutUint32(typ, uint32(d.atoms.notification))
	// All three go out before any is checked, so the checks cost one round
	// trip rather than three.
	cookies := [...]struct {
		name   string
		cookie xproto.ChangePropertyCookie
	}{
		{"WM_NAME", xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
			xproto.AtomWmName, xproto.AtomString, 8, geom.U32(len(name)), []byte(name))},
		{"WM_CLASS", xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
			xproto.AtomWmClass, xproto.AtomString, 8, geom.U32(len(class)), class)},
		{"_NET_WM_WINDOW_TYPE", xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, win,
			d.atoms.windowType, xproto.AtomAtom, 32, 1, typ)},
	}
	var errs []error
	for _, c := range cookies {
		if err := c.cookie.Check(); err != nil {
			errs = append(errs, fmt.Errorf("setting %s on a banner window: %w", c.name, err))
		}
	}
	if len(errs) > 0 {
		if destroyErr := xproto.DestroyWindowChecked(d.conn, win).Check(); destroyErr != nil {
			errs = append(errs, fmt.Errorf("destroying the half-made window: %w", destroyErr))
		}
		return 0, errors.Join(errs...)
	}
	return win, nil
}

func atom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	return reply.Atom, nil
}

// raise and moveWindow are unchecked: they are sent per frame or per
// event, and a check would be a round trip each. A failure arrives as an X
// error in the event loop, which logs it.
func (d *daemon) raise(win xproto.Window) {
	xproto.ConfigureWindow(d.conn, win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
}

func (d *daemon) moveWindow(win xproto.Window, x, y int) {
	xproto.ConfigureWindow(d.conn, win, xproto.ConfigWindowX|xproto.ConfigWindowY,
		[]uint32{geom.I32AsU32(x), geom.I32AsU32(y)})
}

// resizeWindow is checked, unlike moving: it happens once per replaced
// notification, and the surface made for the new size next is only right
// if the window really has that size.
func (d *daemon) resizeWindow(win xproto.Window, w, h int) error {
	if err := xproto.ConfigureWindowChecked(d.conn, win, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{geom.U32(w), geom.U32(h)}).Check(); err != nil {
		return fmt.Errorf("resizing window to %dx%d: %w", w, h, err)
	}
	return nil
}

// updateWorkArea rereads the work area, at startup and whenever the root
// window says it changed.
func (d *daemon) updateWorkArea() {
	area, err := d.workArea()
	if err != nil {
		log.Printf("work area: %v; using %v", err, area)
	}
	d.area = d.onActiveMonitor(area)
}

// onActiveMonitor narrows a work area that spans every monitor down to the
// one the user is working on.
//
// _NET_WORKAREA describes the X screen, and the X screen is the union of
// every monitor: on this desk that is 6000x1440, so its right-hand edge --
// where a banner anchors itself -- is the right-hand edge of the *other*
// display. The banners were drawn correctly and slid in perfectly, on a
// screen nobody was looking at, which is indistinguishable from
// notifications never arriving.
//
// Which monitor is active cannot be worked out here. The pointer is the
// usual stand-in and is wrong twice over: the mouse is wherever it was
// last left, which on two monitors is regularly not the one being typed
// on, and under XWayland its position stops being updated the moment the
// cursor leaves an X11 surface. So the display server is asked -- the
// compositor under Wayland -- exactly as the launcher asks it, and the
// connector name it answers with is resolved against RandR.
//
// Every failure falls back to the area it was given. A banner on the wrong
// monitor is worth more than no banner.
func (d *daemon) onActiveMonitor(area image.Rectangle) image.Rectangle {
	if d.server == nil {
		return area
	}
	name, err := d.server.ActiveMonitor()
	if err != nil {
		d.lim.Printf("monitor", "which monitor is active: %v; using the whole screen", err)
		return area
	}
	m := xmon.For(d.conn, d.screen, name)
	if m.W <= 0 || m.H <= 0 {
		return area
	}
	// Intersected rather than replaced, so whatever the panel reserves
	// along the top is still honoured on the monitor that was picked.
	r := area.Intersect(image.Rect(m.X, m.Y, m.X+m.W, m.Y+m.H))
	if r.Empty() {
		return area
	}
	return r
}

// workArea is the screen minus what panels reserve, for the current
// desktop; the whole screen when the window manager does not say.
//
// The rectangle is always usable. The error says why it may not be the
// right one: a property that could not be read, or one that makes no
// sense.
func (d *daemon) workArea() (image.Rectangle, error) {
	full := image.Rect(0, 0, int(d.screen.WidthInPixels), int(d.screen.HeightInPixels))
	reply, err := xwin.GetProperty(d.conn, d.screen.Root, d.atoms.workarea, "_NET_WORKAREA")
	switch {
	case errors.Is(err, xwin.ErrPropUnset):
		// No window manager, or one that reserves nothing for panels.
		return full, nil
	case err != nil:
		return full, err
	}
	vals, err := xwin.DecodeCard32s(reply)
	if err != nil {
		return full, fmt.Errorf("_NET_WORKAREA: %w", err)
	}

	var desktop uint32
	var desktopErr error
	cur, err := xwin.GetProperty(d.conn, d.screen.Root, d.atoms.currentDesktop, "_NET_CURRENT_DESKTOP")
	switch {
	case errors.Is(err, xwin.ErrPropUnset):
		// A window manager without desktops: there is only the first.
	case err != nil:
		desktopErr = fmt.Errorf("%w; using the first desktop's area", err)
	default:
		if desktop, err = xwin.DecodeCard32(cur); err != nil {
			desktopErr = fmt.Errorf("_NET_CURRENT_DESKTOP: %w; using the first desktop's area", err)
		}
	}
	r, areaErr := workAreaFrom(full, vals, desktop)
	return r, errors.Join(desktopErr, areaErr)
}

// workAreaFrom picks one desktop's area out of _NET_WORKAREA's values --
// x, y, width and height for each desktop in turn -- and keeps it on the
// screen. Whatever does not add up is reported and worked around: a
// desktop beyond the list gets the first desktop's area, and an area off
// the screen gets the whole screen.
func workAreaFrom(full image.Rectangle, vals []uint32, desktop uint32) (image.Rectangle, error) {
	n := len(vals) / 4
	if n == 0 {
		return full, fmt.Errorf("_NET_WORKAREA has %d values, want four per desktop", len(vals))
	}
	var errs []error
	if len(vals)%4 != 0 {
		errs = append(errs, fmt.Errorf("_NET_WORKAREA has %d values, not four per desktop; the extra are ignored", len(vals)))
	}
	if uint64(desktop) >= uint64(n) {
		errs = append(errs, fmt.Errorf("current desktop is %d, but _NET_WORKAREA covers %d; using the first", desktop, n))
		desktop = 0
	}
	a := vals[4*desktop : 4*desktop+4]
	x, y := toInt(a[0]), toInt(a[1])
	r := image.Rect(x, y, x+toInt(a[2]), y+toInt(a[3])).Intersect(full)
	if r.Empty() {
		errs = append(errs, fmt.Errorf("_NET_WORKAREA for desktop %d is %v, which misses the %v screen",
			desktop, a, full.Size()))
		return full, errors.Join(errs...)
	}
	return r, errors.Join(errs...)
}

func toInt(u uint32) int {
	if u > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(u)
}

// coveredByOther reports whether a window that is not a banner lies over
// this banner.
//
// The root window's children come back in stacking order, bottom first, so
// the candidates are the windows after this one. Most are unmapped --
// every application keeps a few hidden windows -- so only viewable ones
// that actually overlap count. The requests for all of them go out before
// any reply is read, which makes this one round trip rather than one per
// window.
//
// It answers false when the tree cannot be read. A window it could not
// inspect is left out and reported in the error, unless it had simply been
// destroyed since the tree was read, which happens all the time and is no
// fault.
func (d *daemon) coveredByOther(b *banner) (bool, error) {
	tree, err := xproto.QueryTree(d.conn, d.screen.Root).Reply()
	if err != nil {
		return false, fmt.Errorf("listing windows: %w", err)
	}
	var (
		wins  []xproto.Window
		attrs []xproto.GetWindowAttributesCookie
		geoms []xproto.GetGeometryCookie
		above bool
	)
	for _, w := range tree.Children {
		if w == b.win {
			above = true
			continue
		}
		if !above || d.byWin[w] != nil {
			continue
		}
		wins = append(wins, w)
		attrs = append(attrs, xproto.GetWindowAttributes(d.conn, w))
		geoms = append(geoms, xproto.GetGeometry(d.conn, xproto.Drawable(w)))
	}

	mine := image.Rect(b.curX, b.curY, b.curX+b.lay.size.X, b.curY+b.lay.size.Y)
	covered := false
	var errs []error
	for i := range attrs {
		// Every reply is read, even after the answer is known, or it would
		// sit in xgb's queue for good.
		a, attrErr := attrs[i].Reply()
		g, geomErr := geoms[i].Reply()
		usable := true
		for _, err := range [...]error{attrErr, geomErr} {
			switch {
			case err == nil:
			case destroyed(err):
				usable = false
			default:
				usable = false
				errs = append(errs, fmt.Errorf("inspecting window 0x%x: %w", wins[i], err))
			}
		}
		if usable && (a == nil || g == nil) {
			usable = false
			errs = append(errs, fmt.Errorf("inspecting window 0x%x: no reply from the X server", wins[i]))
		}
		if !usable || a.MapState != xproto.MapStateViewable {
			continue
		}
		border := 2 * int(g.BorderWidth)
		r := image.Rect(int(g.X), int(g.Y), int(g.X)+int(g.Width)+border, int(g.Y)+int(g.Height)+border)
		if r.Overlaps(mine) {
			covered = true
		}
	}
	return covered, errors.Join(errs...)
}

// destroyed reports whether an X error means only that the window it was
// about no longer exists.
func destroyed(err error) bool {
	var window xproto.WindowError
	var drawable xproto.DrawableError
	return errors.As(err, &window) || errors.As(err, &drawable)
}
