// Command notifyd shows desktop notifications the way macOS does:
// translucent banners that slide in at the top right of the screen, stack
// up, and fade away.
//
// It replaces xfce4-notifyd. It is the server end of the freedesktop.org
// notification specification, so everything that notifies -- through
// libnotify, GLib, or D-Bus directly -- works with it unchanged. The
// protocol and all of the bookkeeping live in internal/notify; this
// command draws.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/xfconf"
)

// version is what GetServerInformation reports.
const version = "1.0"

func main() {
	if err := start(); err != nil {
		fmt.Fprintln(os.Stderr, "notifyd:", err)
		os.Exit(1)
	}
}

// start is separate from main so that the deferred cleanup actually runs.
func start() error {
	var (
		scaleFlag = flag.Float64("scale", 0, "display scale factor (0 detects it)")
		replace   = flag.Bool("replace", false, "take over from a running notification daemon, if it allows that")
		verbose   = flag.Bool("v", false, "log every notification and what became of it")
	)
	flag.Parse()

	d, err := newDaemon(*scaleFlag, *verbose)
	if err != nil {
		return err
	}
	defer d.close()
	if serveErr := d.serve(*replace); serveErr != nil {
		return serveErr
	}
	return d.run()
}

type daemon struct {
	X        *xgbutil.XUtil
	conn     *xgb.Conn
	screen   *xproto.ScreenInfo
	visual   argbVisual
	colormap xproto.Colormap
	atoms    atoms

	// area is the part of the screen not reserved by panels, which is what
	// keeps the banners clear of the one along the top.
	area image.Rectangle

	th    *theme
	faces *faces
	set   *icons.Set
	index *notify.IconIndex

	// fallbacks caches placeholder tiles by label, at the icon size.
	fallbacks map[string]*image.RGBA

	bus      *dbus.Conn
	srv      *notify.Server
	settings <-chan xfconf.Change

	q       *notify.Queue
	banners map[uint32]*banner
	byWin   map[xproto.Window]*banner

	// defaultTimeout is what a notification that leaves the choice to the
	// server gets: xfce4-notifyd's own setting, so the one the user already
	// chose carries over.
	defaultTimeout time.Duration

	expiry   *time.Timer
	expiryC  <-chan time.Time
	expiryAt time.Time

	ticker   *time.Ticker
	tickC    <-chan time.Time
	lastTick time.Time

	verbose bool
}

var _ notify.Handler = (*daemon)(nil)

// defaultTimeout is xfce4-notifyd's default, for when its setting cannot
// be read.
const defaultTimeout = 5 * time.Second

func newDaemon(scaleOverride float64, verbose bool) (*daemon, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	d := &daemon{
		X:              X,
		conn:           X.Conn(),
		set:            icons.New(),
		fallbacks:      make(map[string]*image.RGBA),
		q:              notify.NewQueue(maxShown),
		banners:        make(map[uint32]*banner),
		byWin:          make(map[xproto.Window]*banner),
		defaultTimeout: defaultTimeout,
		verbose:        verbose,
	}
	d.screen = xproto.Setup(d.conn).DefaultScreen(d.conn)

	visual, ok := findARGB(d.screen)
	if !ok {
		d.close()
		return nil, errors.New("no 32-bit TrueColor visual; notifyd needs a compositor " +
			"(xfwm4: Settings > Window Manager Tweaks > Compositor)")
	}
	d.visual = visual

	d.th = newTheme(scale.Detect(X, scaleOverride))
	if d.faces, err = loadFaces(d.th); err != nil {
		d.close()
		return nil, err
	}
	if setupErr := d.setupX(); setupErr != nil {
		d.close()
		return nil, setupErr
	}

	apps, problems := desktop.Scan()
	for _, p := range problems {
		d.logf("warning: %v", p)
	}
	d.index = notify.NewIconIndex(apps, icons.Has)
	return d, nil
}

// serve connects to the session bus and claims the notification name.
//
// The settings are read first, so that the very first notification --
// which may be the one whose arrival started this daemon -- already obeys
// do-not-disturb.
func (d *daemon) serve(replace bool) error {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	d.bus = bus
	d.loadSettings()

	info := notify.Info{Name: "osxflow notifyd", Vendor: "osxflow", Version: version}
	srv, err := notify.Serve(bus, d, info, replace)
	if err != nil {
		return err
	}
	d.srv = srv
	return nil
}

// settingsChannel is xfce4-notifyd's xfconf channel. Reading it rather
// than keeping settings of our own means the panel's do-not-disturb
// toggle, which writes there, works unchanged.
const settingsChannel = "xfce4-notifyd"

func (d *daemon) loadSettings() {
	client := xfconf.New(d.bus)
	for _, prop := range [...]string{"/do-not-disturb", "/expire-timeout"} {
		v, err := client.Get(settingsChannel, prop)
		switch {
		case errors.Is(err, xfconf.ErrNotSet):
		case err != nil:
			// No xfconfd means no XFCE, and the defaults are right.
			d.logf("settings: %v", err)
			return
		default:
			d.applySetting(prop, v, false)
		}
	}
	ch, err := client.Watch(settingsChannel)
	if err != nil {
		d.logf("settings: %v", err)
		return
	}
	d.settings = ch
}

func (d *daemon) applySetting(prop string, v any, removed bool) {
	switch prop {
	case "/do-not-disturb":
		on, ok := v.(bool)
		d.q.DoNotDisturb = ok && on && !removed
		d.logf("do not disturb: %t", d.q.DoNotDisturb)
	case "/expire-timeout":
		d.defaultTimeout = defaultTimeout
		if secs, ok := v.(int32); ok && !removed && secs > 0 {
			d.defaultTimeout = time.Duration(secs) * time.Second
		}
	}
}

// frameInterval paces animation, which runs only while something is
// moving: an idle daemon wakes for nothing.
const frameInterval = 10 * time.Millisecond

func (d *daemon) run() error {
	events := make(chan xgb.Event, 64)
	go d.readEvents(events)
	defer d.stopTimers()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil // the X server went away: the session is ending
			}
			d.handle(ev)

		case call := <-d.srv.Calls():
			call()

		case ch, ok := <-d.settings:
			if ok {
				d.applySetting(ch.Property, ch.Value, ch.Removed)
			} else {
				d.settings = nil
			}

		case now := <-d.expiryC:
			d.expiryC = nil
			d.emitClosed(d.q.Expire(now))
			d.sync()

		case now := <-d.tickC:
			d.step(now)

		case <-d.bus.Context().Done():
			return errors.New("lost the session bus")
		}
		d.armExpiry()
		d.animate(!d.settled())
	}
}

// Notify is a Notify call, run on the main loop.
func (d *daemon) Notify(req *notify.Request) uint32 {
	n := notify.Parse(req, d.defaultTimeout)
	id := d.q.Notify(&n, req.ReplacesID, time.Now())
	d.logf("notify %d from %q: %q", id, n.AppName, n.Summary)
	d.sync()
	return id
}

// CloseNotification is a CloseNotification call, run on the main loop.
func (d *daemon) CloseNotification(id uint32) {
	d.emitClosed(d.q.Close(id, notify.ReasonClosed, time.Now()))
	d.sync()
}

func (d *daemon) emitClosed(closed []notify.Closure) {
	for _, c := range closed {
		d.logf("closed %d (reason %d)", c.ID, c.Reason)
		if err := d.srv.EmitClosed(c); err != nil {
			d.logf("%v", err)
		}
	}
}

// armExpiry points the timer at the next notification due to expire.
// It runs after every event, so it only touches the timer when the answer
// has changed.
func (d *daemon) armExpiry() {
	at, ok := d.q.NextDeadline()
	if ok == (d.expiryC != nil) && at.Equal(d.expiryAt) {
		return
	}
	if d.expiry != nil {
		d.expiry.Stop()
	}
	d.expiry, d.expiryC, d.expiryAt = nil, nil, time.Time{}
	if !ok {
		return
	}
	d.expiry = time.NewTimer(time.Until(at))
	d.expiryC, d.expiryAt = d.expiry.C, at
}

func (d *daemon) animate(on bool) {
	switch {
	case on && d.ticker == nil:
		d.ticker = time.NewTicker(frameInterval)
		d.tickC = d.ticker.C
		d.lastTick = time.Now()
	case !on && d.ticker != nil:
		d.ticker.Stop()
		d.ticker, d.tickC = nil, nil
	}
}

func (d *daemon) stopTimers() {
	if d.expiry != nil {
		d.expiry.Stop()
	}
	if d.ticker != nil {
		d.ticker.Stop()
	}
}

// readEvents pumps X events into a channel so the main loop can wait on
// them, the bus and its timers at once.
func (d *daemon) readEvents(out chan<- xgb.Event) {
	defer close(out)
	for {
		ev, err := d.conn.WaitForEvent()
		if err != nil {
			// A protocol error refers to a request that already failed; the
			// connection is still good.
			d.logf("x error: %v", err)
			continue
		}
		if ev == nil {
			return
		}
		out <- ev
	}
}

func (d *daemon) logf(format string, args ...any) {
	if d.verbose {
		fmt.Fprintf(os.Stderr, "notifyd: "+format+"\n", args...)
	}
}

// close releases everything, and copes with a daemon only half set up.
func (d *daemon) close() {
	if d.srv != nil {
		d.srv.Close()
	}
	if d.bus != nil {
		_ = d.bus.Close() //nolint:errcheck // exiting; the bus notices either way
	}
	for _, b := range d.banners {
		d.destroy(b)
	}
	if d.colormap != 0 {
		xproto.FreeColormap(d.conn, d.colormap)
	}
	if d.faces != nil {
		d.faces.close()
	}
	d.conn.Close()
}
