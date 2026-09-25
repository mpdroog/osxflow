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
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"log"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/xfconf"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// version is what GetServerInformation reports.
const version = "1.0"

func main() {
	// First, before anything can log. Started by the bus, this daemon's
	// stderr lands in the journal among every other activated service's,
	// so each line says whose it is.
	log.SetPrefix("notifyd: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	if err := start(); err != nil {
		log.Fatal(err)
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
	defer func() {
		// Logged rather than returned: by now the daemon has run its course
		// or failed for a reason of its own, and that reason is the one the
		// exit status should carry.
		if closeErr := d.close(); closeErr != nil {
			log.Printf("shutting down: %v", closeErr)
		}
	}()
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

	// server answers which monitor the user is working on -- the
	// compositor under Wayland, the window manager under X11. See
	// onActiveMonitor for why nothing here can work it out for itself.
	//
	// Nil when it could not be opened, which costs the placement and
	// nothing else: banners fall back to the whole screen.
	server xwin.Server

	// area is the part of the screen not reserved by panels, narrowed to
	// the monitor the user is working on, which is what keeps the banners
	// clear of the one along the top and on the screen being looked at.
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

	// failed counts the attempts to put each notification on screen that
	// have failed in a row; see sync.
	failed attempts

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

	// lim is where every error that can repeat is logged: X errors at frame
	// rate, signals to a bus that has gone, whatever a misbehaving client
	// sends.
	lim *errlog.Limiter

	verbose bool
}

var _ notify.Handler = (*daemon)(nil)

// defaultTimeout is xfce4-notifyd's default, for when its setting cannot
// be read.
const defaultTimeout = 5 * time.Second

// A few lines a minute of any one kind of failure is enough to see it and
// what it is about; the rest are counted.
const (
	logBurst = 5
	logEvery = time.Minute
)

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
		failed:         make(attempts),
		defaultTimeout: defaultTimeout,
		lim:            &errlog.Limiter{Burst: logBurst, Per: logEvery},
		verbose:        verbose,
	}
	d.screen = xproto.Setup(d.conn).DefaultScreen(d.conn)

	// Opened before the first updateWorkArea below, which is what uses it.
	// Not closed in close(): under X11 this shares the daemon's own
	// connection, and closing it there would close the connection twice.
	if server, serverErr := xwin.NewServerWith(X); serverErr != nil {
		log.Printf("cannot tell which monitor is active; banners will use the "+
			"whole screen: %v", serverErr)
	} else {
		d.server = server
	}

	// abandon undoes a half-made daemon. What cleaning up turns up is
	// logged: the error being returned is the one that says why.
	abandon := func(err error) (*daemon, error) {
		if closeErr := d.close(); closeErr != nil {
			log.Printf("cleaning up after a failed start: %v", closeErr)
		}
		return nil, err
	}

	visual, ok := findARGB(d.screen)
	if !ok {
		return abandon(errors.New("no 32-bit TrueColor visual; notifyd needs a compositor " +
			"(xfwm4: Settings > Window Manager Tweaks > Compositor)"))
	}
	d.visual = visual

	// The factor is always usable (1 when nothing answers); the error says
	// why it may not match the desktop, which is worth a line but not a
	// failed start.
	factor, scaleErr := scale.Detect(X, scaleOverride)
	if scaleErr != nil {
		log.Printf("scale: %v", scaleErr)
	}
	d.th = newTheme(factor)
	if d.faces, err = loadFaces(d.th); err != nil {
		return abandon(err)
	}
	if setupErr := d.setupX(); setupErr != nil {
		return abandon(setupErr)
	}

	// Once, at startup: a desktop file that cannot be read is an
	// application whose notifications get a placeholder for an icon, and
	// the reason belongs in the log whether or not -v is on.
	apps, problems := desktop.Scan()
	for _, p := range problems {
		log.Printf("desktop files: %v", p)
	}
	d.index = notify.NewIconIndex(apps, icons.Has)
	return d, nil
}

// serve connects to the session bus and claims the notification name.
//
// The settings are read first, so that the very first notification --
// which may be the one whose arrival started this daemon -- already obeys
// do-not-disturb. Failing to read them is not failing to start: the
// defaults are what a desktop without xfconf gets anyway.
func (d *daemon) serve(replace bool) error {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	d.bus = bus
	if settingsErr := d.loadSettings(); settingsErr != nil {
		log.Printf("settings: %v", settingsErr)
	}

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

// settingsProps are the properties read at startup and followed after.
var settingsProps = [...]string{"/do-not-disturb", "/expire-timeout"}

// loadSettings reads the settings and starts following them.
//
// Following starts even when a read fails: a property xfconfd could not
// return this once is no reason to miss the next time the user flips the
// toggle. Every failure is returned together, once the watch is set up.
func (d *daemon) loadSettings() error {
	client := xfconf.New(d.bus)
	var errs []error
read:
	for _, prop := range settingsProps {
		v, err := client.Get(settingsChannel, prop)
		switch {
		case errors.Is(err, xfconf.ErrNotSet):
			// Never set, so it holds xfce4-notifyd's default, which is ours.
		case errors.Is(err, xfconf.ErrNoXfconf):
			// No xfconfd means no XFCE, and the defaults are right. The
			// other properties would only say the same.
			log.Printf("settings: no xfconfd on the session bus; using the defaults")
			break read
		case err != nil:
			errs = append(errs, err)
		default:
			if applyErr := d.applySetting(prop, v, false); applyErr != nil {
				errs = append(errs, applyErr)
			}
		}
	}
	ch, err := client.Watch(settingsChannel)
	if err != nil {
		errs = append(errs, fmt.Errorf("%w; setting changes will not be followed", err))
	} else {
		d.settings = ch
	}
	return errors.Join(errs...)
}

// applySetting puts one setting into effect. A removed property goes back
// to its default. A value of the wrong type changes nothing and is
// returned as an error: guessing would mean, say, reading a do-not-disturb
// that is not a boolean as "off", and interrupting somebody who asked not
// to be.
func (d *daemon) applySetting(prop string, v any, removed bool) error {
	switch prop {
	case "/do-not-disturb":
		on := false
		if !removed {
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("%s is %T, want bool; do-not-disturb stays %t", prop, v, d.q.DoNotDisturb)
			}
			on = b
		}
		d.q.DoNotDisturb = on
		d.debugf("do not disturb: %t", on)
	case "/expire-timeout":
		if removed {
			d.defaultTimeout = defaultTimeout
			return nil
		}
		secs, ok := v.(int32)
		switch {
		case !ok:
			return fmt.Errorf("%s is %T, want int32; the timeout stays %v", prop, v, d.defaultTimeout)
		case secs <= 0:
			return fmt.Errorf("%s is %d, want a positive number of seconds; the timeout stays %v", prop, secs, d.defaultTimeout)
		}
		d.defaultTimeout = time.Duration(secs) * time.Second
	}
	return nil
}

// frameInterval paces animation, which runs only while something is
// moving: an idle daemon wakes for nothing.
const frameInterval = 10 * time.Millisecond

// run is the main loop. It ends only in failure: X or the bus going away
// is how the session ends, and a daemon that exits 0 then would look like
// one that finished its work.
func (d *daemon) run() error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go d.readEvents(events, xGone)
	defer d.stopTimers()

	for {
		select {
		case ev := <-events:
			d.handle(ev)

		case err := <-xGone:
			return err

		case call := <-d.srv.Calls():
			call()

		case ch, ok := <-d.settings:
			switch {
			case !ok:
				// godbus ends the watch when the connection goes, and so
				// does the bus case below, a moment later.
				log.Print("settings: watch ended; setting changes are no longer followed")
				d.settings = nil
			case ch.Err != nil:
				d.lim.Printf("settings", "settings: %v", ch.Err)
			default:
				if err := d.applySetting(ch.Property, ch.Value, ch.Removed); err != nil {
					d.lim.Printf("settings", "settings: %v", err)
				}
			}

		case now := <-d.expiryC:
			d.expiryC = nil
			d.emitClosed(d.q.Expire(now))
			d.sync()

		case now := <-d.tickC:
			d.step(now)

		case <-d.bus.Context().Done():
			return fmt.Errorf("lost the session bus: %w", context.Cause(d.bus.Context()))
		}
		d.armExpiry()
		d.animate(!d.settled())
	}
}

// Notify is a Notify call, run on the main loop.
func (d *daemon) Notify(req *notify.Request) uint32 {
	n, problems := notify.Parse(req, d.defaultTimeout)
	for _, p := range problems {
		// Limited per sender: this is another program's input, and one
		// stuck in a loop should not be able to fill the journal.
		d.lim.Printf("notify:"+n.AppName, "notification from %q: %v", n.AppName, p)
	}
	id := d.q.Notify(&n, req.ReplacesID, time.Now())
	d.debugf("notify %d from %q: %q", id, n.AppName, n.Summary)
	// A new stack starts on whichever monitor is active now. A stack that
	// is already up stays where it is: moving banners to another screen
	// underneath a reply the user is part-way through reading would be the
	// wrong kind of helpful.
	if len(d.banners) == 0 {
		d.updateWorkArea()
	}
	d.sync()
	return id
}

// CloseNotification is a CloseNotification call, run on the main loop. It
// reports whether the notification was open to close.
func (d *daemon) CloseNotification(id uint32) bool {
	closed := d.q.Close(id, notify.ReasonClosed, time.Now())
	d.emitClosed(closed)
	d.sync()
	return len(closed) > 0
}

func (d *daemon) emitClosed(closed []notify.Closure) {
	for _, c := range closed {
		d.debugf("closed %d (reason %d)", c.ID, c.Reason)
		if err := d.srv.EmitClosed(c); err != nil {
			// A bus that has gone fails every one of these until the main
			// loop notices, which is what the limit is for.
			d.lim.Printf("emit", "notification %d closed: %v", c.ID, err)
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
// them, the bus and its timers at once. When the connection ends it says
// so on gone, once, and stops.
func (d *daemon) readEvents(out chan<- xgb.Event, gone chan<- error) {
	for {
		ev, err := d.conn.WaitForEvent()
		if err != nil {
			// A protocol error refers to a request that already failed -- a
			// frame moving a banner whose window has just been destroyed,
			// say -- and the connection is still good. When these come they
			// come at frame rate, so they are limited by kind.
			d.lim.Printf("x:"+fmt.Sprintf("%T", err), "X error: %v", err)
			continue
		}
		if ev == nil {
			// xgb reports the end of the connection as neither an event nor
			// an error. It logs the read failure behind it itself and keeps
			// no cause to hand on.
			gone <- errors.New("the X connection closed")
			return
		}
		out <- ev
	}
}

// debugf traces what happens to notifications, for -v. Errors never go
// through it: they are logged whatever the flag says.
func (d *daemon) debugf(format string, args ...any) {
	if d.verbose {
		log.Printf(format, args...)
	}
}

// close releases what the daemon holds, and copes with a daemon only half
// set up.
//
// Nothing is freed on the X server first. The server frees everything a
// client made -- the banners' windows and pixmaps, the colormap -- when its
// connection closes, which is the last thing here, and requests sent just
// before that could never report an error anyway.
func (d *daemon) close() error {
	var errs []error
	// A bus already lost has been reported as the reason for stopping;
	// releasing the name on it and closing it again could only fail again.
	busUp := d.bus != nil && d.bus.Connected()
	if d.srv != nil && busUp {
		if err := d.srv.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if busUp {
		if err := d.bus.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing the session bus: %w", err))
		}
	}
	if d.faces != nil {
		if err := d.faces.close(); err != nil {
			errs = append(errs, err)
		}
	}
	d.conn.Close()
	return errors.Join(errs...)
}
