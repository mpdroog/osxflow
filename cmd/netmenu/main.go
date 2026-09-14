// Command netmenu is a network menu in the panel's status tray: a Wi-Fi
// icon that opens a macOS-style menu of networks and VPNs.
//
// It replaces nm-applet's tray icon and GTK menu, and does less on
// purpose. It turns Wi-Fi on and off, joins networks that have a saved
// profile or need no password, and switches saved VPNs. Everything else --
// a new password, a new VPN, editing a profile -- opens
// nm-connection-editor. It is not a secret agent: a profile whose secrets
// are not stored with NetworkManager will fail to connect, and say why in
// the log.
//
// The NetworkManager side (internal/netmgr) and the tray icon
// (internal/sni) are D-Bus only. The menu window here is X11.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/netmgr"
	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/sni"
	"github.com/mpdroog/osxflow/internal/text"
)

func main() {
	log.SetPrefix("netmenu: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

// start is separate from main so that the deferred cleanup actually runs.
func start() error {
	var (
		scaleFlag = flag.Float64("scale", 0, "display scale factor (0 detects it)")
		verbose   = flag.Bool("v", false, "log registrations and state changes")
	)
	flag.Parse()

	a, err := newApp(*scaleFlag, *verbose)
	if err != nil {
		return err
	}
	defer func() {
		// Logged rather than returned: the reason for stopping is the one
		// the exit status should carry.
		if closeErr := a.close(); closeErr != nil {
			log.Printf("shutting down: %v", closeErr)
		}
	}()
	return a.run()
}

type app struct {
	X            *xgbutil.XUtil
	conn         *xgb.Conn
	screen       *xproto.ScreenInfo
	visual       argbVisual
	workareaAtom xproto.Atom
	escape       []xproto.Keycode

	th    *theme
	faces *faces

	sys     *dbus.Conn
	session *dbus.Conn
	nm      *netmgr.Client
	changes <-chan struct{}
	item    *sni.Item

	state netmgr.State
	icon  iconState
	tip   sni.ToolTip

	refreshTimer *time.Timer
	refreshC     <-chan time.Time
	lastScan     time.Time

	menu *menu

	lim     *errlog.Limiter
	verbose bool
}

// callTimeout bounds every call to NetworkManager: a hung daemon should
// cost a log line, not a frozen menu.
const callTimeout = 5 * time.Second

// settle is how long a burst of NetworkManager signals is left to finish
// before the state is read again. A scan updates every access point in
// turn; reading after each would be thirty snapshots in a row.
const settle = 150 * time.Millisecond

const (
	logBurst = 5
	logEvery = time.Minute
)

func newApp(scaleOverride float64, verbose bool) (*app, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	a := &app{
		X:       X,
		conn:    X.Conn(),
		lim:     &errlog.Limiter{Burst: logBurst, Per: logEvery},
		verbose: verbose,
	}
	a.screen = xproto.Setup(a.conn).DefaultScreen(a.conn)

	abandon := func(err error) (*app, error) {
		if closeErr := a.close(); closeErr != nil {
			log.Printf("cleaning up after a failed start: %v", closeErr)
		}
		return nil, err
	}

	visual, ok := findARGB(a.screen)
	if !ok {
		return abandon(errors.New("no 32-bit TrueColor visual; netmenu needs a compositor " +
			"(xfwm4: Settings > Window Manager Tweaks > Compositor)"))
	}
	a.visual = visual
	if a.workareaAtom, err = atom(a.conn, "_NET_WORKAREA"); err != nil {
		return abandon(err)
	}
	if a.escape, err = escapeKeycodes(a.conn); err != nil {
		// The menu still closes on a click elsewhere or on the icon.
		log.Printf("escape key: %v; Escape will not close the menu", err)
	}

	factor, scaleErr := scale.Detect(X, scaleOverride)
	if scaleErr != nil {
		log.Printf("scale: %v", scaleErr)
	}
	a.th = newTheme(factor)
	if a.faces, err = loadFaces(a.th); err != nil {
		return abandon(err)
	}

	if a.sys, err = dbus.ConnectSystemBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the system bus: %w", err))
	}
	a.nm = netmgr.New(a.sys)
	// Watching starts before the first read, so a change in between is
	// not missed.
	if a.changes, err = a.nm.Watch(); err != nil {
		return abandon(err)
	}
	a.readState()

	if a.session, err = dbus.ConnectSessionBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the session bus: %w", err))
	}
	a.icon, a.tip = iconFor(&a.state), tooltipFor(&a.state)
	a.item, err = sni.Export(a.session, &sni.Options{
		ID:       "osxflow-netmenu",
		Title:    "Network",
		Category: "SystemServices",
		Icon:     iconPixmaps(a.icon),
		ToolTip:  a.tip,
	})
	if err != nil {
		return abandon(err)
	}
	return a, nil
}

// run is the main loop. Everything that touches the menu or the state
// happens on it. It ends only in failure: X or a bus going away is how the
// session ends.
func (a *app) run() error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go a.readEvents(events, xGone)
	defer func() {
		if a.refreshTimer != nil {
			a.refreshTimer.Stop()
		}
	}()

	for {
		select {
		case ev := <-events:
			a.handle(ev)

		case err := <-xGone:
			return err

		case c := <-a.item.Clicks():
			a.clicked(c)

		case err := <-a.item.Registrations():
			switch {
			case errors.Is(err, sni.ErrNoWatcher):
				log.Printf("tray: %v; the icon appears when the panel's status tray starts", err)
			case err != nil:
				log.Printf("tray: %v", err)
			default:
				a.debugf("registered with the tray")
			}

		case _, ok := <-a.changes:
			if !ok {
				return errors.New("the NetworkManager signal watch ended")
			}
			if a.refreshC == nil {
				a.refreshTimer = time.NewTimer(settle)
				a.refreshC = a.refreshTimer.C
			}

		case <-a.refreshC:
			a.refreshTimer, a.refreshC = nil, nil
			a.refresh()

		case <-a.sys.Context().Done():
			return fmt.Errorf("lost the system bus: %w", context.Cause(a.sys.Context()))

		case <-a.session.Context().Done():
			return fmt.Errorf("lost the session bus: %w", context.Cause(a.session.Context()))
		}
	}
}

// readState takes a fresh snapshot. When NetworkManager cannot be read at
// all the previous state stays: an empty menu would claim there are no
// networks, which is not what anyone knows.
func (a *app) readState() {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	st, err := a.nm.Snapshot(ctx)
	if err != nil {
		a.lim.Printf("snapshot", "reading NetworkManager: %v", err)
		if errors.Is(err, netmgr.ErrNoState) {
			return
		}
	}
	a.state = st
}

// refresh reads the state and brings the icon, the tooltip and any open
// menu up to date with it.
func (a *app) refresh() {
	a.readState()

	if icon := iconFor(&a.state); icon != a.icon {
		a.debugf("icon: %+v", icon)
		a.icon = icon
		if err := a.item.SetIcon(iconPixmaps(icon)); err != nil {
			a.lim.Printf("icon", "updating the icon: %v", err)
		}
	}
	if tip := tooltipFor(&a.state); tip.Title != a.tip.Title || tip.Text != a.tip.Text {
		a.tip = tip
		if err := a.item.SetToolTip(tip); err != nil {
			a.lim.Printf("tooltip", "updating the tooltip: %v", err)
		}
	}
	if a.menu != nil {
		if err := a.menu.update(a); err != nil {
			log.Printf("redrawing the menu: %v; closing it", err)
			a.closeMenu()
		}
	}
}

// clicked opens or closes the menu. Any button but the middle one does:
// macOS has one menu for both, and so does this.
func (a *app) clicked(c sni.Click) {
	if c.Kind == sni.SecondaryActivate {
		return
	}
	if a.menu != nil {
		a.closeMenu()
		return
	}
	// Placed under the pointer, which is on the icon when it is clicked,
	// rather than where the tray says. XFCE's status tray is GTK and sends
	// logical pixels: at 2x a click at 2103,42 arrives as 1051,21, and a
	// menu placed there opens a screen's width to the left. Other trays
	// send 0, 0. The pointer is in real pixels whatever the tray thinks.
	x := c.X
	reply, err := xproto.QueryPointer(a.conn, a.screen.Root).Reply()
	if err != nil {
		log.Printf("finding the pointer: %v; using the tray's position %d, %d", err, c.X, c.Y)
	} else {
		x = int(reply.RootX)
		a.debugf("click: tray sent %d,%d; pointer at %d,%d", c.X, c.Y, reply.RootX, reply.RootY)
	}
	if err := a.openMenu(x); err != nil {
		log.Printf("opening the menu: %v", err)
	}
}

func (a *app) handle(ev xgb.Event) {
	m := a.menu
	if m == nil {
		return
	}
	var err error
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Window == m.win {
			err = m.surf.Copy()
		}
	case xproto.MotionNotifyEvent:
		err = m.motion(a, int(e.EventX), int(e.EventY))
	case xproto.ButtonPressEvent:
		a.menuClick(e)
	case xproto.KeyPressEvent:
		for _, code := range a.escape {
			if e.Detail == code {
				a.closeMenu()
				break
			}
		}
	}
	if err != nil {
		a.lim.Printf("menu", "drawing the menu: %v", err)
	}
}

// async runs a NetworkManager request off the main loop, so a slow answer
// -- a polkit check, say -- does not freeze the menu. Its only output is a
// failure, logged here; success shows up as signals.
func (a *app) async(f func(context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		if err := f(ctx); err != nil {
			a.lim.Printf("request", "%v", err)
		}
	}()
}

// readEvents pumps X events into a channel, so the main loop can wait on
// them and the buses at once.
func (a *app) readEvents(out chan<- xgb.Event, gone chan<- error) {
	for {
		ev, err := a.conn.WaitForEvent()
		if err != nil {
			a.lim.Printf("x:"+fmt.Sprintf("%T", err), "X error: %v", err)
			continue
		}
		if ev == nil {
			gone <- errors.New("the X connection closed")
			return
		}
		out <- ev
	}
}

func (a *app) debugf(format string, args ...any) {
	if a.verbose {
		log.Printf(format, args...)
	}
}

// close releases what the app holds and copes with one only half set up.
func (a *app) close() error {
	var errs []error
	a.closeMenu()
	if a.item != nil && a.session.Connected() {
		errs = append(errs, a.item.Close())
	}
	for _, bus := range []struct {
		name string
		conn *dbus.Conn
	}{{"session", a.session}, {"system", a.sys}} {
		if bus.conn == nil || !bus.conn.Connected() {
			continue
		}
		if err := bus.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing the %s bus: %w", bus.name, err))
		}
	}
	if a.faces != nil {
		errs = append(errs, a.faces.close())
	}
	a.conn.Close()
	return errors.Join(errs...)
}

type faces struct {
	text, bold, section font.Face
}

func loadFaces(th *theme) (*faces, error) {
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
	if f.text, err = text.Face(regular, th.textPt); err != nil {
		return nil, fmt.Errorf("text face: %w", err)
	}
	if f.bold, err = text.Face(bold, th.textPt); err != nil {
		return nil, errors.Join(fmt.Errorf("bold face: %w", err), f.close())
	}
	if f.section, err = text.Face(bold, th.sectionPt); err != nil {
		return nil, errors.Join(fmt.Errorf("section face: %w", err), f.close())
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
