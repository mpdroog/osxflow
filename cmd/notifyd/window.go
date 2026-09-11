package main

// The banners' X windows, and what the daemon needs to know about the
// screen they sit on.

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil/ewmh"

	"github.com/mpdroog/osxflow/internal/geom"
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
	workarea     xproto.Atom
	windowType   xproto.Atom
	notification xproto.Atom
}

// setupX interns the atoms, creates the colormap every banner shares, and
// reads the work area.
func (d *daemon) setupX() error {
	for _, a := range []struct {
		dst  *xproto.Atom
		name string
	}{
		{&d.atoms.workarea, "_NET_WORKAREA"},
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

	d.area = d.workArea()
	// The root window reports the work area changing -- a panel added,
	// moved or resized -- so that banners follow it. Without that they
	// stay where the panel used to end, which is survivable.
	if watchErr := xproto.ChangeWindowAttributesChecked(d.conn, d.screen.Root,
		xproto.CwEventMask, []uint32{uint32(xproto.EventMaskPropertyChange)}).Check(); watchErr != nil {
		d.logf("warning: not watching the work area: %v", watchErr)
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
	xproto.ChangeProperty(d.conn, xproto.PropModeReplace, win,
		xproto.AtomWmName, xproto.AtomString, 8, geom.U32(len(name)), []byte(name))
	class := []byte(name + "\x00Osxflow\x00")
	xproto.ChangeProperty(d.conn, xproto.PropModeReplace, win,
		xproto.AtomWmClass, xproto.AtomString, 8, geom.U32(len(class)), class)
	typ := make([]byte, 4)
	binary.LittleEndian.PutUint32(typ, uint32(d.atoms.notification))
	xproto.ChangeProperty(d.conn, xproto.PropModeReplace, win,
		d.atoms.windowType, xproto.AtomAtom, 32, 1, typ)
	return win, nil
}

func atom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	return reply.Atom, nil
}

func (d *daemon) raise(win xproto.Window) {
	xproto.ConfigureWindow(d.conn, win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
}

func (d *daemon) moveWindow(win xproto.Window, x, y int) {
	xproto.ConfigureWindow(d.conn, win, xproto.ConfigWindowX|xproto.ConfigWindowY,
		[]uint32{geom.I32AsU32(x), geom.I32AsU32(y)})
}

func (d *daemon) resizeWindow(win xproto.Window, w, h int) {
	xproto.ConfigureWindow(d.conn, win, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{geom.U32(w), geom.U32(h)})
}

// workArea is the screen minus what panels reserve, for the current
// desktop; the whole screen when the window manager does not say.
func (d *daemon) workArea() image.Rectangle {
	full := image.Rect(0, 0, int(d.screen.WidthInPixels), int(d.screen.HeightInPixels))
	areas, err := ewmh.WorkareaGet(d.X)
	if err != nil || len(areas) == 0 {
		return full
	}
	i := 0
	if cur, curErr := ewmh.CurrentDesktopGet(d.X); curErr == nil && cur < uint(len(areas)) {
		i = toInt(cur)
	}
	a := areas[i]
	r := image.Rect(a.X, a.Y, a.X+toInt(a.Width), a.Y+toInt(a.Height)).Intersect(full)
	if r.Empty() {
		return full
	}
	return r
}

func toInt(u uint) int {
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
func (d *daemon) coveredByOther(b *banner) bool {
	tree, err := xproto.QueryTree(d.conn, d.screen.Root).Reply()
	if err != nil {
		return false
	}
	var (
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
		attrs = append(attrs, xproto.GetWindowAttributes(d.conn, w))
		geoms = append(geoms, xproto.GetGeometry(d.conn, xproto.Drawable(w)))
	}

	mine := image.Rect(b.curX, b.curY, b.curX+b.lay.size.X, b.curY+b.lay.size.Y)
	covered := false
	for i := range attrs {
		// Every reply is read, even after the answer is known, or it would
		// sit in xgb's queue for good.
		a, attrErr := attrs[i].Reply()
		g, geomErr := geoms[i].Reply()
		if attrErr != nil || geomErr != nil || a.MapState != xproto.MapStateViewable {
			continue
		}
		border := 2 * int(g.BorderWidth)
		r := image.Rect(int(g.X), int(g.Y), int(g.X)+int(g.Width)+border, int(g.Y)+int(g.Height)+border)
		if r.Overlaps(mine) {
			covered = true
		}
	}
	return covered
}
