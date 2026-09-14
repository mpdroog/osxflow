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
// (internal/sni) are D-Bus only. The menu window (internal/menu) is X11.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/netmgr"
	"github.com/mpdroog/osxflow/internal/sni"
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
		verbose   = flag.Bool("v", false, "log registrations, clicks and icon changes")
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
	X    *xgbutil.XUtil
	conn *xgb.Conn
	host *menu.Host

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

	lim     *errlog.Limiter
	verbose bool
}

var _ actions = (*app)(nil)

// callTimeout bounds every call to NetworkManager: a hung daemon should
// cost a log line, not a frozen menu.
const callTimeout = 5 * time.Second

// settle is how long a burst of NetworkManager signals is left to finish
// before the state is read again. A scan updates every access point in
// turn; reading after each would be thirty snapshots in a row.
const settle = 150 * time.Millisecond

// scanSpacing is how often opening the menu may ask for a scan.
// NetworkManager refuses requests closer together than about ten seconds
// anyway, and a refusal is only noise in the log.
const scanSpacing = 30 * time.Second

const (
	logBurst = 5
	logEvery = time.Minute
)

func newApp(scaleOverride float64, verbose bool) (*app, error) {
	xu, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	a := &app{
		X:       xu,
		conn:    xu.Conn(),
		lim:     &errlog.Limiter{Burst: logBurst, Per: logEvery},
		verbose: verbose,
	}
	abandon := func(err error) (*app, error) {
		if closeErr := a.close(); closeErr != nil {
			log.Printf("cleaning up after a failed start: %v", closeErr)
		}
		return nil, err
	}

	if a.host, err = menu.NewHost(xu, scaleOverride, menuWidth); err != nil {
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
			if err := a.host.Handle(ev); err != nil {
				a.lim.Printf("menu", "drawing the menu: %v", err)
			}

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
	if err := a.host.Update(buildRows(&a.state, a)); err != nil {
		log.Printf("redrawing the menu: %v; it has been closed", err)
	}
}

// clicked opens or closes the menu. Any button but the middle one does:
// macOS has one menu for both, and so does this. The wheel does nothing.
func (a *app) clicked(c sni.Click) {
	if c.Kind == sni.SecondaryActivate || c.Kind == sni.Scroll {
		return
	}
	if a.host.IsOpen() {
		a.host.Close()
		return
	}
	x, err := a.host.PointerX()
	if err != nil {
		log.Printf("%v; using the tray's position %d", err, c.X)
		x = c.X
	} else {
		a.debugf("click: tray sent %d,%d; pointer at x=%d", c.X, c.Y, x)
	}
	if err := a.host.Open(x, buildRows(&a.state, a)); err != nil {
		log.Printf("opening the menu: %v", err)
		return
	}
	a.scan()
}

// scan asks for a fresh list of networks, no more than once every
// scanSpacing.
func (a *app) scan() {
	dev := a.state.WifiDevice
	if dev == "" || !a.state.WifiEnabled || time.Since(a.lastScan) < scanSpacing {
		return
	}
	a.lastScan = time.Now()
	a.async(func(ctx context.Context) error { return a.nm.RequestScan(ctx, dev) })
}

// The actions behind the menu's rows. NetworkManager's answer arrives as
// signals, which redraw the menu and the icon; the calls themselves only
// say whether a request was accepted.

func (a *app) setWifi(on bool) {
	a.async(func(ctx context.Context) error { return a.nm.SetWifi(ctx, on) })
}

func (a *app) join(n *netmgr.Network) {
	profile, device := n.Saved, a.state.WifiDevice
	a.async(func(ctx context.Context) error { return a.nm.Activate(ctx, profile, device) })
}

func (a *app) joinOpen(n *netmgr.Network) {
	ssid, device, ap := slices.Clone(n.Raw), a.state.WifiDevice, n.AP
	a.async(func(ctx context.Context) error { return a.nm.JoinOpen(ctx, ssid, device, ap) })
}

func (a *app) toggleVPN(v *netmgr.VPN) {
	if v.On() {
		active := v.Active
		a.async(func(ctx context.Context) error { return a.nm.Deactivate(ctx, active) })
		return
	}
	profile := v.Connection
	a.async(func(ctx context.Context) error { return a.nm.Activate(ctx, profile, "") })
}

func (a *app) settings() {
	editor := &desktop.App{Name: "Network Connections", Argv: []string{"nm-connection-editor"}}
	if err := launch.SpawnDetached(editor); err != nil {
		log.Printf("opening network settings: %v", err)
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
	if a.host != nil {
		errs = append(errs, a.host.Release())
	}
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
	a.conn.Close()
	return errors.Join(errs...)
}
