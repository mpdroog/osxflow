package menu

// The Host: the X connection and what every menu from it shares -- the
// ARGB visual, fonts, the scale, the Escape key -- and the windows it makes.

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"

	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xsurface"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// Host opens menus on one X display. Every method runs on the goroutine
// that reads the display's events.
type Host struct {
	X      *xgbutil.XUtil
	Conn   *xgb.Conn
	Screen *xproto.ScreenInfo
	Theme  *Theme

	visual   visual
	faces    *faces
	escape   []xproto.Keycode
	workarea xproto.Atom

	popup *popup
}

// visual is a 32-bit TrueColor visual and the depth it belongs to.
type visual struct {
	id    xproto.Visualid
	depth byte
}

// NewHost sets up menus width logical pixels wide on X's default screen.
// scaleOverride is the display scale, or 0 to detect it.
//
// xu itself stays the caller's: Release gives back what the Host made, not
// the connection.
func NewHost(xu *xgbutil.XUtil, scaleOverride, width float64) (*Host, error) {
	h := &Host{X: xu, Conn: xu.Conn()}
	h.Screen = xproto.Setup(h.Conn).DefaultScreen(h.Conn)

	v, ok := findARGB(h.Screen)
	if !ok {
		// Without one a menu's corners are square and opaque. xfwm4's
		// compositor provides one, but it is a setting the user can turn
		// off.
		return nil, errors.New("no 32-bit TrueColor visual; menus need a compositor " +
			"(xfwm4: Settings > Window Manager Tweaks > Compositor)")
	}
	h.visual = v

	var err error
	if h.workarea, err = atom(h.Conn, "_NET_WORKAREA"); err != nil {
		return nil, err
	}
	factor, scaleErr := scale.Detect(xu, scaleOverride)
	if scaleErr != nil {
		// Always usable (1 when nothing answers); the error says why it may
		// not match the desktop.
		log.Printf("scale: %v", scaleErr)
	}
	h.Theme = NewTheme(factor, width)
	if h.escape, err = h.Keycodes(keysymEscape); err != nil {
		// A menu still closes on a click elsewhere or on its icon.
		log.Printf("escape key: %v; Escape will not close menus", err)
	}
	if h.faces, err = loadFaces(h.Theme); err != nil {
		return nil, err
	}
	return h, nil
}

// Release closes any open menu and frees the fonts.
func (h *Host) Release() error {
	h.Close()
	if h.faces == nil {
		return nil
	}
	err := h.faces.close()
	h.faces = nil
	return err
}

// PointerX is where the pointer is across the screen, in real pixels. Open
// a menu from a tray icon there: the pointer is on the icon when it is
// clicked, and XFCE's tray reports positions in half-size logical pixels.
func (h *Host) PointerX() (int, error) {
	reply, err := xproto.QueryPointer(h.Conn, h.Screen.Root).Reply()
	if err != nil {
		return 0, fmt.Errorf("finding the pointer: %w", err)
	}
	if reply == nil {
		return 0, errors.New("finding the pointer: no reply from the X server")
	}
	return int(reply.RootX), nil
}

// keysymEscape is XK_Escape.
const keysymEscape = 0xff1b

// Keycodes finds every key that produces keysym, in any column of the
// keymap. There is normally one, but the keymap is the user's to change.
func (h *Host) Keycodes(keysym xproto.Keysym) ([]xproto.Keycode, error) {
	setup := xproto.Setup(h.Conn)
	first := setup.MinKeycode
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	reply, err := xproto.GetKeyboardMapping(h.Conn, first, count).Reply()
	if err != nil {
		return nil, fmt.Errorf("reading the keyboard mapping: %w", err)
	}
	if reply == nil {
		return nil, errors.New("reading the keyboard mapping: no reply from the X server")
	}
	per := int(reply.KeysymsPerKeycode)
	var codes []xproto.Keycode
	for i := byte(0); i < count; i++ {
		for j := range per {
			k := int(i)*per + j
			if k < len(reply.Keysyms) && reply.Keysyms[k] == keysym {
				codes = append(codes, first+xproto.Keycode(i))
				break
			}
		}
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("no key produces keysym %#x", uint32(keysym))
	}
	return codes, nil
}

// menuTop is where a menu's top edge goes before the gap: the top of the
// work area, which is the bottom of the panel.
//
// The first desktop's area is used whatever desktop is current: the panel
// reserves the same strip on all of them, and the top edge is all that is
// needed. The answer is always usable -- the top of the screen when
// nothing better is known -- and what went wrong is logged.
func (h *Host) menuTop() int {
	reply, err := xwin.GetProperty(h.Conn, h.Screen.Root, h.workarea, "_NET_WORKAREA")
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
	if limit := int(h.Screen.HeightInPixels) / 2; top > limit {
		log.Printf("_NET_WORKAREA starts at y=%d, more than half way down; placing the menu at the top of the screen", top)
		return 0
	}
	return top
}

// Window is an override-redirect, translucent window with a surface to
// draw into: a menu, or a tool's own overlay.
type Window struct {
	host     *Host
	ID       xproto.Window
	colormap xproto.Colormap
	Surf     *xsurface.Surface
	W, H     int
}

// NewWindow makes a window at x, y, unmapped, delivering the events in
// events (an xproto.EventMask).
//
// Override-redirect: a menu or an overlay is placed and dismissed by its
// owner, not decorated, focused and listed in the taskbar by the window
// manager.
func (h *Host) NewWindow(x, y, width, height int, name string, events uint32) (*Window, error) {
	w := &Window{host: h, W: width, H: height}
	if err := w.create(x, y, name, events); err != nil {
		// Whatever create got as far as making would otherwise stay on the
		// server until the tool exits.
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *Window) create(x, y int, name string, events uint32) error {
	h := w.host
	cmap, err := xproto.NewColormapId(h.Conn)
	if err != nil {
		return fmt.Errorf("allocating a colormap id: %w", err)
	}
	// A 32-bit window in a 24-bit parent needs a colormap of its own.
	if cmapErr := xproto.CreateColormapChecked(h.Conn, xproto.ColormapAllocNone,
		cmap, h.Screen.Root, h.visual.id).Check(); cmapErr != nil {
		return fmt.Errorf("creating the colormap: %w", cmapErr)
	}
	w.colormap = cmap

	id, err := xproto.NewWindowId(h.Conn)
	if err != nil {
		return fmt.Errorf("allocating a window id: %w", err)
	}
	// In mask-bit order: BackPixel, BorderPixel, OverrideRedirect,
	// EventMask, Colormap.
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	values := []uint32{0, 0, 1, events, uint32(cmap)}
	if createErr := xproto.CreateWindowChecked(h.Conn, h.visual.depth, id, h.Screen.Root,
		geom.I16(x), geom.I16(y), geom.U16(w.W), geom.U16(w.H), 0,
		xproto.WindowClassInputOutput, h.visual.id, mask, values).Check(); createErr != nil {
		return fmt.Errorf("creating the %s window: %w", name, createErr)
	}
	// Only now: Close destroys w.ID, and a window never created is not one
	// to clean up.
	w.ID = id
	if nameErr := nameWindow(h.Conn, id, name); nameErr != nil {
		log.Printf("warning: %v", nameErr)
	}
	if w.Surf, err = xsurface.New(h.Conn, id, h.visual.depth, w.W, w.H); err != nil {
		return err
	}
	return nil
}

// Map shows the window above everything else.
func (w *Window) Map() error {
	if err := xproto.MapWindowChecked(w.host.Conn, w.ID).Check(); err != nil {
		return fmt.Errorf("mapping a window: %w", err)
	}
	return w.Raise()
}

// Raise puts the window back on top, as nothing else will for an
// override-redirect window.
func (w *Window) Raise() error {
	if err := xproto.ConfigureWindowChecked(w.host.Conn, w.ID,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check(); err != nil {
		return fmt.Errorf("raising a window: %w", err)
	}
	return nil
}

// Unmap hides the window without destroying it.
func (w *Window) Unmap() error {
	if err := xproto.UnmapWindowChecked(w.host.Conn, w.ID).Check(); err != nil {
		return fmt.Errorf("unmapping a window: %w", err)
	}
	return nil
}

// Resize changes the window's size and replaces its surface, whose old
// contents are gone.
func (w *Window) Resize(width, height int) error {
	if err := xproto.ConfigureWindowChecked(w.host.Conn, w.ID,
		xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{geom.U32(width), geom.U32(height)}).Check(); err != nil {
		return fmt.Errorf("resizing a window: %w", err)
	}
	if w.Surf != nil {
		if err := w.Surf.Close(); err != nil {
			log.Printf("releasing an old surface: %v", err)
		}
		w.Surf = nil
	}
	surf, err := xsurface.New(w.host.Conn, w.ID, w.host.visual.depth, width, height)
	if err != nil {
		return err
	}
	w.Surf, w.W, w.H = surf, width, height
	return nil
}

// Close releases what the window holds on the server. It is safe on a
// window that create only got part of the way through. Failures are
// logged: the window is going away either way.
func (w *Window) Close() {
	h := w.host
	if w.Surf != nil {
		if err := w.Surf.Close(); err != nil {
			log.Printf("releasing a surface: %v", err)
		}
		w.Surf = nil
	}
	if w.ID != 0 {
		if err := xproto.DestroyWindowChecked(h.Conn, w.ID).Check(); err != nil {
			log.Printf("destroying a window: %v", err)
		}
		w.ID = 0
	}
	if w.colormap != 0 {
		if err := xproto.FreeColormapChecked(h.Conn, w.colormap).Check(); err != nil {
			log.Printf("freeing a colormap: %v", err)
		}
		w.colormap = 0
	}
}

func findARGB(screen *xproto.ScreenInfo) (visual, bool) {
	for _, depth := range screen.AllowedDepths {
		if depth.Depth != 32 {
			continue
		}
		for _, v := range depth.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				return visual{id: v.VisualId, depth: depth.Depth}, true
			}
		}
	}
	return visual{}, false
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

// Grabs are retried for a moment: a tray may deliver the click while its
// own implicit grab from the button press is still in place.
const (
	grabAttempts = 50
	grabRetry    = 10 * time.Millisecond
)

func retryGrab(grab func() (byte, error)) (byte, error) {
	for attempt := 1; ; attempt++ {
		status, err := grab()
		if err != nil {
			return 0, err
		}
		busy := status == xproto.GrabStatusAlreadyGrabbed || status == xproto.GrabStatusFrozen
		if !busy || attempt == grabAttempts {
			return status, nil
		}
		time.Sleep(grabRetry)
	}
}

// grabStatus names a grab status, which X reports as a bare number.
func grabStatus(status byte) string {
	switch status {
	case xproto.GrabStatusAlreadyGrabbed:
		return "another client has it grabbed"
	case xproto.GrabStatusInvalidTime:
		return "invalid time"
	case xproto.GrabStatusNotViewable:
		return "the window is not viewable"
	case xproto.GrabStatusFrozen:
		return "it is frozen by another grab"
	}
	return fmt.Sprintf("status %d", status)
}

type faces struct {
	text, bold, section font.Face
}

func loadFaces(t *Theme) (*faces, error) {
	regular, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, fmt.Errorf("loading regular font: %w", err)
	}
	bold, _, boldErr := text.Load(text.BoldCandidates)
	if boldErr != nil {
		log.Printf("no bold font, using regular: %v", boldErr)
		bold = regular
	}
	f := &faces{}
	for _, spec := range []struct {
		name string
		dst  *font.Face
		font *sfnt.Font
		size float64
	}{
		{"text", &f.text, regular, t.textPt},
		{"bold", &f.bold, bold, t.textPt},
		{"section", &f.section, bold, t.sectionPt},
	} {
		face, faceErr := text.Face(spec.font, spec.size)
		if faceErr != nil {
			return nil, errors.Join(fmt.Errorf("%s face: %w", spec.name, faceErr), f.close())
		}
		*spec.dst = face
	}
	return f, nil
}

func (f *faces) close() error {
	var errs []error
	for _, face := range []font.Face{f.text, f.bold, f.section} {
		if face == nil {
			continue
		}
		if err := face.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing a font face: %w", err))
		}
	}
	return errors.Join(errs...)
}
