// Command menubar is the bar across the top of the screen, the way macOS
// has one: Tux at the left, which opens macmenu, the focused application's
// name beside it, and at the right the tray and the clock.
//
// It replaces xfce4-panel. The tray is its own: menubar is the
// StatusNotifierWatcher items register with, so it draws osxflow's menus,
// ezhours and anything else speaking that protocol, and draws the menus
// items publish over D-Bus in osxflow's style. Running applications are
// not on it; that is the dock's job.
package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/sni"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/wl"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// tuxPNG is cmd/macmenu/macmenu.svg rasterised by librsvg, which is what
// the panel drew it with:
//
//	gdk-pixbuf-thumbnailer -s 128 cmd/macmenu/macmenu.svg cmd/menubar/tux.png
//
//go:embed tux.png
var tuxPNG []byte

func main() {
	log.SetPrefix("menubar: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

type app struct {
	conn  *xgb.Conn
	root  xproto.Window
	host  *menu.Host
	bus   *dbus.Conn
	props *xwin.Props

	// screens is one bar per monitor under Wayland, and exactly one bar
	// under X11. See screen.go for what a monitor of its own costs.
	screens []*screen

	// art is the fonts and Tux, cached by the scale they were rasterised
	// at and shared between monitors that agree on one.
	art map[float64]*artwork

	// wl is the compositor connection, or nil under X11. See wayland.go
	// for why the strip is the one part of menubar that could not stay an
	// X window.
	wl *wl.Conn

	macmenu string

	// calendar is the page the clock opens, or "" for none.
	calendar string

	watcher *sni.Watcher
	items   map[sni.Ref]*trayItem
	refs    []sni.Ref // registration order
	shown   []*trayItem

	windows    xwin.Server
	index      *launch.Index
	activeAtom xproto.Atom
	appName    string
	clock      string

	fetches chan fetched
	menus   chan openMenu
	errs    chan error
}

func start() error {
	scaleFlag := flag.Float64("scale", 0, "display scale factor (0 detects it)")
	calendarFlag := flag.String("calendar", "http://ical.rootdev.nl",
		`page the clock opens in Firefox when clicked ("" for none)`)
	flag.Parse()

	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	defer func() {
		if closeErr := bus.Close(); closeErr != nil {
			log.Printf("closing the session bus: %v", closeErr)
		}
	}()
	watcher, err := sni.NewWatcher(bus)
	if errors.Is(err, sni.ErrTrayRunning) {
		return fmt.Errorf("%w: stop the other tray first (xfce4-panel --quit)", err)
	}
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := watcher.Close(); closeErr != nil {
			log.Printf("closing the tray: %v", closeErr)
		}
	}()

	xu, err := xgbutil.NewConn()
	if err != nil {
		return fmt.Errorf("connecting to X display: %w", err)
	}
	defer xu.Conn().Close()
	host, err := menu.NewHost(xu, *scaleFlag, baseMenuW)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := host.Release(); releaseErr != nil {
			log.Printf("releasing the menus: %v", releaseErr)
		}
	}()

	a := &app{
		conn:     xu.Conn(),
		root:     host.Screen.Root,
		host:     host,
		bus:      bus,
		props:    xwin.NewProps(xu.Conn()),
		art:      map[float64]*artwork{},
		watcher:  watcher,
		calendar: *calendarFlag,
		items:    map[sni.Ref]*trayItem{},
		fetches:  make(chan fetched, 16),
		menus:    make(chan openMenu, 4),
		errs:     make(chan error, 16),
	}
	defer a.closeArtwork()
	if a.macmenu, err = sibling("macmenu"); err != nil {
		return err
	}
	// NewServerWith, not NewX11With: under labwc the X11 implementation
	// fails at the first read, because XWayland has no idea which
	// Wayland-native windows exist and so labwc publishes no
	// _NET_CLIENT_LIST at all. That is what left the focused
	// application's name blank on this desk.
	if a.windows, err = xwin.NewServerWith(xu); err != nil {
		return err
	}
	apps, problems := desktop.Scan()
	for _, p := range problems {
		log.Printf("warning: %v", p)
	}
	a.index = launch.NewIndex(apps)

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if a.wl, err = wl.Dial(); err != nil {
			return err
		}
		defer func() {
			a.closeScreens()
			if closeErr := a.wl.Disconnect(); closeErr != nil {
				log.Printf("disconnecting from the compositor: %v", closeErr)
			}
		}()
		// A bar on every monitor, the way macOS has one on every display.
		a.syncScreens()
		if len(a.screens) == 0 {
			return errNoMonitors
		}
		a.updateApp()
	} else {
		s, err := a.newX11Screen(host.Theme.Scale)
		if err != nil {
			return err
		}
		a.screens = []*screen{s}
		if err := a.watchFocus(); err != nil {
			// The bar works without the application's name.
			log.Printf("%v; the focused application's name will not be shown", err)
		}
	}
	return a.loop()
}

// loadFaces rasterises the regular and bold faces at one size, and
// returns what closes them.
func loadFaces(pt float64) (regularFace, boldFace font.Face, closeFaces func(), err error) {
	regular, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading regular font: %w", err)
	}
	bold, _, err := text.Load(text.BoldCandidates)
	if err != nil {
		log.Printf("no bold font, using regular: %v", err)
		bold = regular
	}
	if regularFace, err = text.Face(regular, pt); err != nil {
		return nil, nil, nil, fmt.Errorf("regular face: %w", err)
	}
	if boldFace, err = text.Face(bold, pt); err != nil {
		if closeErr := regularFace.Close(); closeErr != nil {
			log.Printf("closing the regular face: %v", closeErr)
		}
		return nil, nil, nil, fmt.Errorf("bold face: %w", err)
	}
	return regularFace, boldFace, func() {
		for _, f := range []font.Face{regularFace, boldFace} {
			if err := f.Close(); err != nil {
				log.Printf("closing a font face: %v", err)
			}
		}
	}, nil
}

func loadTux(size int) (*image.RGBA, error) {
	img, err := png.Decode(bytes.NewReader(tuxPNG))
	if err != nil {
		return nil, fmt.Errorf("decoding the embedded Tux: %w", err)
	}
	rgba := image.NewRGBA(img.Bounds())
	xdraw.Copy(rgba, rgba.Bounds().Min, img, img.Bounds(), xdraw.Src, nil)
	return fit(rgba, size), nil
}

// sibling is the path of another osxflow tool, installed beside this one.
func sibling(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding this executable, to find %s beside it: %w", name, err)
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

// watchFocus follows _NET_ACTIVE_WINDOW, and reads it once now.
func (a *app) watchFocus() error {
	var err error
	if a.activeAtom, err = a.props.Atom("_NET_ACTIVE_WINDOW"); err != nil {
		return err
	}
	if err := xproto.ChangeWindowAttributesChecked(a.conn, a.root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return fmt.Errorf("watching the root window: %w", err)
	}
	a.updateApp()
	return nil
}

// updateApp shows the name of whatever application has focus now.
func (a *app) updateApp() {
	name, err := a.focusedApp()
	if err != nil {
		log.Printf("finding the focused application: %v", err)
	}
	if name != a.appName {
		a.appName = name
		a.relayoutAll()
	}
}

// focusedApp is the name of the application whose window has focus: the
// name from its .desktop entry, or its WM_CLASS when it has none, or ""
// when the focus is on the desktop, a panel or nothing.
func (a *app) focusedApp() (string, error) {
	id, err := a.activeWindow()
	if err != nil || id == 0 {
		return "", err
	}
	wins, err := a.windows.Windows()
	if err != nil {
		return "", err
	}
	for i := range wins {
		w := &wins[i]
		if w.ID != id {
			continue
		}
		if !w.Listable() {
			return "", nil
		}
		if owner, _ := a.index.Owner(w, launch.ProcExe); owner != nil {
			return owner.Name, nil
		}
		return w.Class, nil
	}
	// Focus on something that is not a managed top-level window: the
	// desktop, typically.
	return "", nil
}

// activeWindow is the id of the window that has the keyboard, or 0 when
// nothing does.
//
// The two display servers answer this from different places and neither
// can answer for the other: _NET_ACTIVE_WINDOW names an X window, and
// under labwc it can only ever name an XWayland one, so a Wayland-native
// window being focused looks exactly like nothing being focused. The
// compositor's own answer covers both, since XWayland's windows are
// toplevels to it like any other.
func (a *app) activeWindow() (uint32, error) {
	if a.wl != nil {
		t, ok := a.wl.Active()
		if !ok {
			return 0, nil
		}
		return t.ID, nil
	}
	reply, err := xwin.GetProperty(a.conn, a.root, a.activeAtom, "_NET_ACTIVE_WINDOW")
	if errors.Is(err, xwin.ErrPropUnset) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := xwin.DecodeCard32(reply)
	if err != nil {
		return 0, fmt.Errorf("_NET_ACTIVE_WINDOW: %w", err)
	}
	return id, nil
}

// tick updates the clock and says when to next look.
func (a *app) tick(now time.Time) time.Duration {
	if s := now.Format("Mon 2006-01-02 15:04"); s != a.clock {
		a.clock = s
		a.relayoutAll()
	}
	// Timers stop during suspend; a short cap puts the time right soon
	// after waking rather than up to a minute later.
	return min(now.Truncate(time.Minute).Add(time.Minute).Sub(now), 5*time.Second)
}

func (a *app) loop() error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go readEvents(a.conn, events, xGone)

	if err := a.bus.AddMatchSignal(sni.ItemSignals()...); err != nil {
		return fmt.Errorf("listening for tray items' changes: %w", err)
	}
	signals := make(chan *dbus.Signal, 64)
	a.bus.Signal(signals)
	defer a.bus.RemoveSignal(signals)

	a.syncItems()
	clock := time.NewTimer(a.tick(time.Now()))
	defer clock.Stop()
	for {
		select {
		case ev := <-events:
			a.handleX(ev)
		case e := <-a.wlEvents():
			a.handleWayland(e)
		case err := <-xGone:
			return err
		case <-a.watcher.Changes():
			a.syncItems()
		case err := <-a.watcher.Errors():
			log.Print(err)
		case sig := <-signals:
			if sni.Changed(sig) {
				a.itemChanged(sig.Sender, string(sig.Path))
			}
		case res := <-a.fetches:
			a.applyFetch(&res)
		case m := <-a.menus:
			a.showMenu(&m)
		case err := <-a.errs:
			log.Print(err)
		case now := <-clock.C:
			clock.Reset(a.tick(now))
		}
		a.paintDirty()
	}
}

func (a *app) handleX(ev xgb.Event) {
	// Under Wayland every one of these but the menus' own events belongs
	// to a window this process no longer has.
	bar := a.x11Screen()
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if bar == nil || e.Window != bar.win.ID {
			a.handleMenu(ev)
			return
		}
		if e.Count == 0 {
			if err := bar.win.Surf.Copy(); err != nil {
				log.Printf("drawing the bar: %v", err)
			}
		}
	case xproto.PropertyNotifyEvent:
		if e.Window == a.root && e.Atom == a.activeAtom {
			a.updateApp()
		}
	case xproto.MotionNotifyEvent:
		if a.host.IsOpen() || bar == nil {
			a.handleMenu(ev)
			return
		}
		a.hoverAt(bar, image.Pt(int(e.EventX), int(e.EventY)))
	case xproto.LeaveNotifyEvent:
		if bar != nil {
			a.hoverAt(bar, image.Pt(-1, -1))
		}
	case xproto.ButtonPressEvent:
		if a.host.IsOpen() || bar == nil {
			a.handleMenu(ev)
			return
		}
		a.press(bar, image.Pt(int(e.EventX), int(e.EventY)), e.Detail)
	case xproto.ButtonReleaseEvent, xproto.KeyPressEvent:
		a.handleMenu(ev)
	}
}

func (a *app) handleMenu(ev xgb.Event) {
	if err := a.host.Handle(ev); err != nil {
		log.Printf("drawing a menu: %v", err)
	}
}

// x11Screen is the single X11 bar, or nil in a Wayland session.
func (a *app) x11Screen() *screen {
	if len(a.screens) == 1 && a.screens[0].win != nil {
		return a.screens[0]
	}
	return nil
}

func (a *app) hoverAt(s *screen, p image.Point) {
	s.pointer = p
	hover := at(s.slots, p, s.m.height)
	if hover != s.hover {
		s.hover = hover
		s.dirty = true
	}
}

func (a *app) press(sc *screen, p image.Point, detail xproto.Button) {
	i := at(sc.slots, p, sc.m.height)
	if i < 0 {
		return
	}
	s := &sc.slots[i]
	switch s.kind {
	case slotMenu:
		if detail != xproto.ButtonIndex1 {
			return
		}
		// Tux's own x, so the menu opens under the bar that was clicked.
		// macmenu is a separate process with no idea which monitor it was
		// summoned from, and under Wayland it cannot find out: the
		// pointer it would otherwise ask stopped being meaningful when
		// the cursor last left an X window.
		x := sc.screenX(s.r.Min.X + s.r.Dx()/2)
		app := &desktop.App{Name: "macmenu", Argv: []string{a.macmenu, "-x", strconv.Itoa(x)}}
		if err := launch.SpawnDetached(app); err != nil {
			log.Printf("opening macmenu: %v", err)
		}
	case slotItem:
		a.clickItem(a.shown[s.item], detail, sc, sc.screenX(s.r.Min.X+s.r.Dx()/2))
	case slotClock:
		if detail != xproto.ButtonIndex1 || a.calendar == "" {
			return
		}
		// xdg-open, not firefox: Firefox here is a flatpak, so there is no
		// firefox binary in PATH and naming one failed the click silently
		// but for a line in a log nobody reads. The handler the desktop is
		// configured with is the right answer anyway, and it is what the
		// dock already uses to open a file.
		app := &desktop.App{Name: "calendar", Argv: []string{"xdg-open", a.calendar}}
		if err := launch.SpawnDetached(app); err != nil {
			log.Printf("opening the calendar: %v", err)
		}
	case slotApp:
	}
}

func readEvents(conn *xgb.Conn, out chan<- xgb.Event, gone chan<- error) {
	for {
		ev, err := conn.WaitForEvent()
		if err != nil {
			log.Printf("X error: %v", err)
			continue
		}
		if ev == nil {
			gone <- errors.New("the X connection closed")
			return
		}
		out <- ev
	}
}
