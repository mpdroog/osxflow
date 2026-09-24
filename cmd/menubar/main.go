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
	m     *metrics

	win   *menu.Window
	width int

	regular, bold font.Face
	tux           *image.RGBA
	macmenu       string

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

	slots   []slot
	hover   int
	pointer image.Point
	dirty   bool

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
		m:        newMetrics(host.Theme.Scale),
		watcher:  watcher,
		calendar: *calendarFlag,
		items:    map[sni.Ref]*trayItem{},
		hover:    -1,
		pointer:  image.Pt(-1, -1),
		fetches:  make(chan fetched, 16),
		menus:    make(chan openMenu, 4),
		errs:     make(chan error, 16),
	}
	closeFaces, err := a.loadFaces()
	if err != nil {
		return err
	}
	defer closeFaces()
	if a.tux, err = loadTux(a.m.tux); err != nil {
		return err
	}
	if a.macmenu, err = sibling("macmenu"); err != nil {
		return err
	}
	if a.windows, err = xwin.NewX11With(xu); err != nil {
		return err
	}
	apps, problems := desktop.Scan()
	for _, p := range problems {
		log.Printf("warning: %v", p)
	}
	a.index = launch.NewIndex(apps)

	if err := a.createBar(); err != nil {
		return err
	}
	if err := a.watchFocus(); err != nil {
		// The bar works without the application's name.
		log.Printf("%v; the focused application's name will not be shown", err)
	}
	return a.loop()
}

// loadFaces loads the regular and bold faces, and returns what closes
// them.
func (a *app) loadFaces() (func(), error) {
	regular, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, fmt.Errorf("loading regular font: %w", err)
	}
	bold, _, err := text.Load(text.BoldCandidates)
	if err != nil {
		log.Printf("no bold font, using regular: %v", err)
		bold = regular
	}
	if a.regular, err = text.Face(regular, a.m.textPt); err != nil {
		return nil, fmt.Errorf("regular face: %w", err)
	}
	if a.bold, err = text.Face(bold, a.m.textPt); err != nil {
		if closeErr := a.regular.Close(); closeErr != nil {
			log.Printf("closing the regular face: %v", closeErr)
		}
		return nil, fmt.Errorf("bold face: %w", err)
	}
	return func() {
		for _, f := range []font.Face{a.regular, a.bold} {
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
		a.relayout()
	}
}

// focusedApp is the name of the application whose window has focus: the
// name from its .desktop entry, or its WM_CLASS when it has none, or ""
// when the focus is on the desktop, a panel or nothing.
func (a *app) focusedApp() (string, error) {
	reply, err := xwin.GetProperty(a.conn, a.root, a.activeAtom, "_NET_ACTIVE_WINDOW")
	if errors.Is(err, xwin.ErrPropUnset) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id, err := xwin.DecodeCard32(reply)
	if err != nil {
		return "", fmt.Errorf("_NET_ACTIVE_WINDOW: %w", err)
	}
	if id == 0 {
		return "", nil
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

// tick updates the clock and says when to next look.
func (a *app) tick(now time.Time) time.Duration {
	if s := now.Format("Mon 2006-01-02 15:04"); s != a.clock {
		a.clock = s
		a.relayout()
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
		if a.dirty {
			if err := a.paint(); err != nil {
				log.Printf("drawing the bar: %v", err)
			}
		}
	}
}

func (a *app) handleX(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Window != a.win.ID {
			a.handleMenu(ev)
			return
		}
		if e.Count == 0 {
			if err := a.win.Surf.Copy(); err != nil {
				log.Printf("drawing the bar: %v", err)
			}
		}
	case xproto.PropertyNotifyEvent:
		if e.Window == a.root && e.Atom == a.activeAtom {
			a.updateApp()
		}
	case xproto.MotionNotifyEvent:
		if a.host.IsOpen() {
			a.handleMenu(ev)
			return
		}
		a.hoverAt(image.Pt(int(e.EventX), int(e.EventY)))
	case xproto.LeaveNotifyEvent:
		a.hoverAt(image.Pt(-1, -1))
	case xproto.ButtonPressEvent:
		if a.host.IsOpen() {
			a.handleMenu(ev)
			return
		}
		a.press(e)
	case xproto.ButtonReleaseEvent, xproto.KeyPressEvent:
		a.handleMenu(ev)
	}
}

func (a *app) handleMenu(ev xgb.Event) {
	if err := a.host.Handle(ev); err != nil {
		log.Printf("drawing a menu: %v", err)
	}
}

func (a *app) hoverAt(p image.Point) {
	a.pointer = p
	hover := at(a.slots, p, a.m.height)
	if hover != a.hover {
		a.hover = hover
		a.dirty = true
	}
}

func (a *app) press(e xproto.ButtonPressEvent) {
	i := at(a.slots, image.Pt(int(e.EventX), int(e.EventY)), a.m.height)
	if i < 0 {
		return
	}
	s := &a.slots[i]
	switch s.kind {
	case slotMenu:
		if e.Detail != xproto.ButtonIndex1 {
			return
		}
		app := &desktop.App{Name: "macmenu", Argv: []string{a.macmenu}}
		if err := launch.SpawnDetached(app); err != nil {
			log.Printf("opening macmenu: %v", err)
		}
	case slotItem:
		a.clickItem(a.shown[s.item], e.Detail, s.r.Min.X+s.r.Dx()/2)
	case slotClock:
		if e.Detail != xproto.ButtonIndex1 || a.calendar == "" {
			return
		}
		app := &desktop.App{Name: "firefox", Argv: []string{"firefox", a.calendar}}
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
