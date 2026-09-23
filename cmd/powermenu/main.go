// Command powermenu is a battery menu in the panel's status tray: a battery
// icon that opens a macOS-style menu with the charge, the time left,
// battery health, screen and keyboard brightness, and presentation mode.
//
// It replaces the XFCE panel's power manager plugin, and only the plugin.
// xfce4-power-manager keeps running: it is what handles the lid, suspend,
// the brightness keys and screen blanking. Presentation mode is its
// setting, which this reads and writes through xfconf, so the two always
// agree.
//
// Battery state comes from UPower and brightness is set through logind,
// both D-Bus and neither needing root. The menu window (internal/menu) is
// X11.
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
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/backlight"
	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/sni"
	"github.com/mpdroog/osxflow/internal/upower"
	"github.com/mpdroog/osxflow/internal/xfconf"
)

func main() {
	log.SetPrefix("powermenu: ")
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
		if closeErr := a.close(); closeErr != nil {
			log.Printf("shutting down: %v", closeErr)
		}
	}()
	return a.run()
}

const (
	menuWidth = 300

	// callTimeout bounds a call to UPower or xfconf; brightnessTimeout a
	// brightness change, which a drag makes many of in a row.
	callTimeout       = 5 * time.Second
	brightnessTimeout = time.Second

	// settle is how long a burst of UPower signals is left to finish
	// before the battery is read again.
	settle = 150 * time.Millisecond

	// pollEvery is how often the brightness is re-read while the menu is
	// open. Nothing announces a change -- the brightness keys go through
	// xfce4-power-manager straight to sysfs -- and a slider that ignores
	// the keys looks broken. With the menu closed nothing is polled.
	pollEvery = time.Second

	logBurst = 5
	logEvery = time.Minute
)

// The xfconf channel and property of xfce4-power-manager's presentation
// mode, as its panel plugin uses them.
const (
	powerChannel     = "xfce4-power-manager"
	presentationProp = "/xfce4-power-manager/presentation-mode"
)

type app struct {
	X    *xgbutil.XUtil
	conn *xgb.Conn
	host *menu.Host

	sys     *dbus.Conn
	session *dbus.Conn
	up      *upower.Client
	upDone  <-chan struct{}
	sysfs   backlight.Sysfs
	setter  *backlight.Setter
	xf      *xfconf.Client
	xfWatch <-chan xfconf.Change
	item    *sni.Item

	view view
	icon iconState
	tip  sni.ToolTip

	refreshTimer *time.Timer
	refreshC     <-chan time.Time
	poll         *time.Ticker
	pollC        <-chan time.Time

	lim     *errlog.Limiter
	verbose bool
}

var _ actions = (*app)(nil)

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
	a.up = upower.New(a.sys)
	// Watching starts before the first read, so a change in between is
	// not missed.
	if a.upDone, err = a.up.Watch(); err != nil {
		return abandon(err)
	}
	a.readBattery()
	a.setter = backlight.NewSetter(a.sys)
	a.findLights()

	if a.session, err = dbus.ConnectSessionBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the session bus: %w", err))
	}
	a.xf = xfconf.New(a.session)
	a.loadPresentation()

	a.icon, a.tip = iconFor(&a.view), tooltipFor(&a.view)
	a.item, err = sni.Export(a.session, &sni.Options{
		ID:       "osxflow-powermenu",
		Title:    "Battery",
		Category: "Hardware",
		Icon:     iconPixmaps(a.icon),
		ToolTip:  a.tip,
	})
	if err != nil {
		return abandon(err)
	}
	return a, nil
}

// findLights looks for the screen's and the keyboard's backlight once, at
// startup: they do not come and go. A light that cannot be found is simply
// not in the menu.
func (a *app) findLights() {
	screen, err := a.sysfs.Screen()
	if err != nil && !errors.Is(err, backlight.ErrNotFound) {
		log.Printf("screen brightness: %v", err)
	}
	keyboard, err := a.sysfs.Keyboard()
	if err != nil && !errors.Is(err, backlight.ErrNotFound) {
		log.Printf("keyboard brightness: %v", err)
	}
	a.view.screen, a.view.keyboard = screen, keyboard
}

// loadPresentation reads presentation mode and starts following it, so the
// switch moves when xfce4-power-manager's own settings change it.
func (a *app) loadPresentation() {
	v, err := a.xf.Get(powerChannel, presentationProp)
	switch {
	case errors.Is(err, xfconf.ErrNotSet):
		// Never set, so off: xfce4-power-manager's default.
	case err != nil:
		log.Printf("presentation mode: %v", err)
	default:
		a.applyPresentation(v, false)
	}
	if a.xfWatch, err = a.xf.Watch(powerChannel); err != nil {
		log.Printf("presentation mode: %v; changes made elsewhere will not show", err)
	}
}

func (a *app) applyPresentation(v any, removed bool) {
	if removed {
		a.view.presentation = false
		return
	}
	on, ok := v.(bool)
	if !ok {
		a.lim.Printf("presentation", "presentation mode: %s is %T, want bool; leaving it %t", presentationProp, v, a.view.presentation)
		return
	}
	a.view.presentation = on
}

// run is the main loop. Everything that touches the menu or the view
// happens on it. It ends only in failure: X or a bus going away is how the
// session ends.
func (a *app) run() error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go a.readEvents(events, xGone)
	defer a.stopTimers()

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

		case _, ok := <-a.upDone:
			if !ok {
				return errors.New("the UPower signal watch ended")
			}
			if a.refreshC == nil {
				a.refreshTimer = time.NewTimer(settle)
				a.refreshC = a.refreshTimer.C
			}

		case ch, ok := <-a.xfWatch:
			switch {
			case !ok:
				// godbus ends the watch when the connection goes, and the
				// session bus case below says so a moment later.
				log.Print("presentation mode: watch ended; changes made elsewhere will not show")
				a.xfWatch = nil
			case ch.Err != nil:
				a.lim.Printf("xfconf", "presentation mode: %v", ch.Err)
			case ch.Property == presentationProp:
				a.applyPresentation(ch.Value, ch.Removed)
				a.updateMenu()
			}

		case <-a.refreshC:
			a.refreshTimer, a.refreshC = nil, nil
			a.readBattery()
			a.updateIcon()
			a.updateMenu()

		case <-a.pollC:
			a.pollLights()

		case <-a.sys.Context().Done():
			return fmt.Errorf("lost the system bus: %w", context.Cause(a.sys.Context()))

		case <-a.session.Context().Done():
			return fmt.Errorf("lost the session bus: %w", context.Cause(a.session.Context()))
		}
	}
}

// readBattery takes a fresh snapshot. When UPower cannot be read at all
// the previous battery stays rather than the icon claiming there is none.
func (a *app) readBattery() {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := a.up.Snapshot(ctx)
	switch {
	case errors.Is(err, upower.ErrNoBattery):
		a.view.hasBattery = false
		return
	case errors.Is(err, upower.ErrNoState):
		a.lim.Printf("battery", "reading UPower: %v", err)
		return
	case err != nil:
		// Part of it could not be read -- the battery's health, say -- and
		// the rest is still worth showing.
		a.lim.Printf("battery", "reading UPower: %v", err)
	}
	a.view.battery, a.view.hasBattery = b, true
}

func (a *app) readLights() {
	for _, d := range []*backlight.Device{a.view.screen, a.view.keyboard} {
		if d == nil {
			continue
		}
		if err := a.sysfs.Read(d); err != nil {
			a.lim.Printf("light:"+d.Name, "reading %s brightness: %v", d.Name, err)
		}
	}
}

// pollLights re-reads the brightness while the menu is open, and stops
// polling once it has closed.
func (a *app) pollLights() {
	if !a.host.IsOpen() {
		a.stopPoll()
		return
	}
	before := a.lightLevels()
	a.readLights()
	if a.lightLevels() != before {
		a.updateMenu()
	}
}

func (a *app) lightLevels() [2]int {
	var levels [2]int
	for i, d := range []*backlight.Device{a.view.screen, a.view.keyboard} {
		if d != nil {
			levels[i] = d.Brightness
		}
	}
	return levels
}

func (a *app) updateIcon() {
	if icon := iconFor(&a.view); icon != a.icon {
		a.debugf("icon: %+v", icon)
		a.icon = icon
		if err := a.item.SetIcon(iconPixmaps(icon)); err != nil {
			a.lim.Printf("icon", "updating the icon: %v", err)
		}
	}
	if tip := tooltipFor(&a.view); tip.Title != a.tip.Title || tip.Text != a.tip.Text {
		a.tip = tip
		if err := a.item.SetToolTip(tip); err != nil {
			a.lim.Printf("tooltip", "updating the tooltip: %v", err)
		}
	}
}

func (a *app) updateMenu() {
	if err := a.host.Update(buildRows(&a.view, a)); err != nil {
		log.Printf("redrawing the menu: %v; it has been closed", err)
	}
}

// clicked opens or closes the menu. The middle button and the wheel do
// nothing.
func (a *app) clicked(c sni.Click) {
	if c.Kind == sni.SecondaryActivate || c.Kind == sni.Scroll {
		return
	}
	if a.host.IsOpen() {
		a.host.Close()
		a.stopPoll()
		return
	}
	a.host.AnchorY = c.Y
	x, err := a.host.MenuX(c.X)
	if err != nil {
		log.Printf("%v; using the tray's position %d", err, c.X)
		x = c.X
	} else {
		a.debugf("click: tray sent %d,%d; pointer at x=%d", c.X, c.Y, x)
	}
	a.readLights()
	if err := a.host.Open(x, buildRows(&a.view, a)); err != nil {
		log.Printf("opening the menu: %v", err)
		return
	}
	if a.poll == nil {
		a.poll = time.NewTicker(pollEvery)
		a.pollC = a.poll.C
	}
}

func (a *app) stopPoll() {
	if a.poll != nil {
		a.poll.Stop()
		a.poll, a.pollC = nil, nil
	}
}

func (a *app) stopTimers() {
	if a.refreshTimer != nil {
		a.refreshTimer.Stop()
	}
	a.stopPoll()
}

// The actions behind the menu's rows.

func (a *app) setScreen(value float64, final bool) {
	a.setLight(a.view.screen, value, final)
}

func (a *app) setKeyboard(value float64, final bool) {
	a.setLight(a.view.keyboard, value, final)
}

// setLight sets a backlight from a slider. It runs on the main loop, one
// call at a time, so a drag's changes arrive in order and the last one
// wins; a call that would change nothing is not made at all.
func (a *app) setLight(d *backlight.Device, value float64, final bool) {
	if d == nil {
		return
	}
	if raw := d.RawFor(value); raw != d.Brightness {
		ctx, cancel := context.WithTimeout(context.Background(), brightnessTimeout)
		err := a.setter.Set(ctx, d, raw)
		cancel()
		if err != nil {
			a.lim.Printf("light:"+d.Name, "setting %s brightness: %v", d.Name, err)
		}
	}
	// While dragging the menu shows the pointer's position; once let go it
	// shows what the light is really at.
	if final {
		a.updateMenu()
	}
}

func (a *app) setPresentation(on bool) {
	if err := a.xf.Set(powerChannel, presentationProp, on); err != nil {
		log.Printf("turning presentation mode %s: %v", onOff(on), err)
		return
	}
	// xfconf will echo the change back through the watch; showing it now
	// saves the switch a visible hesitation.
	a.view.presentation = on
	a.updateMenu()
}

func (a *app) settings() {
	app := &desktop.App{Name: "Power Manager", Argv: []string{"xfce4-power-manager-settings"}}
	if err := launch.SpawnDetached(app); err != nil {
		log.Printf("opening power settings: %v", err)
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
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
