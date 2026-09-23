// Command bluemenu is a Bluetooth menu in the panel's status tray: a
// Bluetooth icon that opens a macOS-style menu with the radio switch and
// the paired devices, and a small prompt when a device asks to pair.
//
// It replaces blueman: its tray icon, and its pairing agent. Pairing a new
// device is started from blueman-manager ("Bluetooth Settings…") or
// bluetoothctl; the confirmation it needs arrives here.
//
// BlueZ is D-Bus (internal/bluez) and the radio switch is /dev/rfkill
// (internal/rfkill), neither needing root. The menu is X11 (internal/menu).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/bluez"
	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/rfkill"
	"github.com/mpdroog/osxflow/internal/sni"
)

func main() {
	log.SetPrefix("bluemenu: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

// start is separate from main so that the deferred cleanup actually runs.
func start() error {
	var (
		scaleFlag = flag.Float64("scale", 0, "display scale factor (0 detects it)")
		verbose   = flag.Bool("v", false, "log registrations, clicks, agent requests and icon changes")
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

	// callTimeout bounds a read or a power change; connectTimeout a
	// connect or disconnect, which waits for the device to answer.
	callTimeout    = 5 * time.Second
	connectTimeout = 30 * time.Second

	// settle is how long a burst of BlueZ signals is left to finish before
	// the state is read again.
	settle = 150 * time.Millisecond

	// agentPath is where the pairing agent is exported.
	agentPath = dbus.ObjectPath("/org/osxflow/bluemenu/agent")

	logBurst = 5
	logEvery = time.Minute
)

type app struct {
	X    *xgbutil.XUtil
	conn *xgb.Conn
	host *menu.Host

	sys     *dbus.Conn
	session *dbus.Conn

	// closeMenus carries "another osxflow menu opened"; see menu.Exclusive.
	closeMenus <-chan struct{}
	bt         *bluez.Client
	btC        <-chan struct{}
	agent      *bluez.Agent
	agentReq   <-chan *bluez.Pending
	radio      *rfkill.Watcher
	radioC     <-chan rfkill.Event
	switches   rfkill.Switches
	item       *sni.Item

	view view
	icon iconState
	tip  sni.ToolTip

	// pending is the agent request the prompt is showing, and pendingC
	// closes when BlueZ is done with it -- cancelled, timed out, replaced.
	pending  *bluez.Pending
	pendingC <-chan struct{}

	// lastX is where the icon was last clicked, which is where a prompt
	// opens: a request arrives with no click to place it by.
	lastX int

	// reloading is set while a driver reload is waiting on its password
	// dialog, so a second click does not open a second dialog.
	reloading atomic.Bool

	refreshTimer *time.Timer
	refreshC     <-chan time.Time

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
		X:        xu,
		conn:     xu.Conn(),
		switches: make(rfkill.Switches),
		lim:      &errlog.Limiter{Burst: logBurst, Per: logEvery},
		verbose:  verbose,
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
	// Until the icon is clicked, a prompt opens at the right-hand end of
	// the panel, where the tray is.
	a.lastX = int(a.host.Screen.WidthInPixels) - a.host.Theme.Width

	if a.sys, err = dbus.ConnectSystemBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the system bus: %w", err))
	}
	a.bt = bluez.New(a.sys)
	if a.btC, err = a.bt.Watch(); err != nil {
		return abandon(err)
	}
	a.readState()

	if a.radio, err = rfkill.Watch(rfkill.DefaultPath); err != nil {
		// The menu still works; it just cannot tell a radio switched off
		// from one that is missing, or switch it.
		log.Printf("radio switch: %v", err)
	} else {
		a.radioC = a.radio.Events()
	}

	if a.session, err = dbus.ConnectSessionBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the session bus: %w", err))
	}
	// One menu at a time across all of them. Not fatal: without it they
	// merely overlap, as they did before.
	if a.closeMenus, err = a.host.Exclusive(a.session); err != nil {
		log.Printf("menu exclusivity unavailable: %v", err)
	}

	a.icon, a.tip = iconFor(&a.view), tooltipFor(&a.view)
	a.item, err = sni.Export(a.session, &sni.Options{
		ID:       "osxflow-bluemenu",
		Title:    "Bluetooth",
		Category: "Hardware",
		Icon:     iconPixmaps(a.icon),
		ToolTip:  a.tip,
	})
	if err != nil {
		return abandon(err)
	}
	return a, nil
}

// run is the main loop. Everything that touches the menu, the prompt or
// the view happens on it. It ends only in failure: X or a bus going away
// is how the session ends.
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

		case <-a.closeMenus:
			// Another osxflow menu opened.
			a.host.Close()

		case err := <-a.item.Registrations():
			switch {
			case errors.Is(err, sni.ErrNoWatcher):
				log.Printf("tray: %v; the icon appears when the panel's status tray starts", err)
			case err != nil:
				log.Printf("tray: %v", err)
			default:
				a.debugf("registered with the tray")
			}

		case _, ok := <-a.btC:
			if !ok {
				return errors.New("the BlueZ signal watch ended")
			}
			a.armRefresh()

		case <-a.refreshC:
			a.refreshTimer, a.refreshC = nil, nil
			a.readState()
			a.refreshShown()

		case ev, ok := <-a.radioC:
			if !ok {
				if err := a.radio.Err(); err != nil {
					log.Printf("radio switch: %v; its state is no longer followed", err)
				}
				a.radioC = nil
				continue
			}
			a.switches.Apply(&ev)
			a.view.present, a.view.soft, a.view.hard = a.switches.State(rfkill.TypeBluetooth)
			// The adapter comes and goes with the switch; BlueZ says so a
			// moment later, and reading then catches it.
			a.refreshShown()
			a.armRefresh()

		case p, ok := <-a.agentReq:
			if !ok {
				log.Print("pairing agent: requests ended")
				a.agentReq = nil
				continue
			}
			a.request(p)

		case <-a.pendingC:
			// BlueZ is done with the request: cancelled, timed out, or
			// replaced by the next one.
			a.pendingC = nil
			if a.pending != nil {
				a.pending = nil
				a.host.Close()
			}

		case <-a.sys.Context().Done():
			return fmt.Errorf("lost the system bus: %w", context.Cause(a.sys.Context()))

		case <-a.session.Context().Done():
			return fmt.Errorf("lost the session bus: %w", context.Cause(a.session.Context()))
		}
		a.checkPrompt()
	}
}

func (a *app) armRefresh() {
	if a.refreshC == nil {
		a.refreshTimer = time.NewTimer(settle)
		a.refreshC = a.refreshTimer.C
	}
}

// readState takes a fresh snapshot, and registers the pairing agent
// whenever bluetoothd has (re)appeared: an agent does not survive a
// restart of the daemon it registered with.
func (a *app) readState() {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	st, err := a.bt.Snapshot(ctx)
	switch {
	case errors.Is(err, bluez.ErrNoState):
		a.lim.Printf("bluez", "reading BlueZ: %v", err)
		a.view.running = false
		return
	case err != nil:
		a.lim.Printf("bluez", "reading BlueZ: %v", err)
	}
	wasRunning := a.view.running
	a.view.state, a.view.running = st, true
	if !wasRunning || a.agent == nil {
		a.registerAgent()
	}
}

func (a *app) registerAgent() {
	if a.agent != nil {
		if err := a.agent.Close(); err != nil {
			a.lim.Printf("agent", "closing the previous pairing agent: %v", err)
		}
		a.agent, a.agentReq = nil, nil
	}
	agent, err := bluez.RegisterAgent(a.sys, agentPath)
	if err != nil {
		a.lim.Printf("agent", "registering the pairing agent: %v; devices that need a confirmation will not pair", err)
		return
	}
	a.agent, a.agentReq = agent, agent.Requests()
	a.debugf("pairing agent registered")
}

// request shows the prompt for an agent request.
func (a *app) request(p *bluez.Pending) {
	a.debugf("agent: request %d for %s", p.Kind, p.Device)
	switch p.Kind {
	case bluez.Released:
		log.Print("pairing agent: released by BlueZ")
		a.agent, a.agentReq = nil, nil
		a.dropPrompt()
		return
	case bluez.Cancelled:
		a.dropPrompt()
		return
	case bluez.PinCode, bluez.Passkey:
		log.Printf("pairing agent: %s wants a code typed in, which this menu cannot take; BlueZ was told no",
			deviceName(&a.view.state, p.Device))
	case bluez.Confirm, bluez.Authorize, bluez.AuthorizeService, bluez.DisplayPasskey, bluez.DisplayPinCode:
	}

	rows := promptRows(&p.Request, deviceName(&a.view.state, p.Device), a)
	a.host.Close()
	a.checkPrompt()
	if err := a.host.Open(a.lastX, rows); err != nil {
		log.Printf("showing a pairing request: %v; answering no", err)
		if p.Kind.NeedsAnswer() {
			p.Answer(false)
		}
		return
	}
	a.pending = p
	// A request already over when it arrives -- a refused typed-code
	// request -- is shown until dismissed, not taken straight down.
	a.pendingC = nil
	if p.Kind.NeedsAnswer() || p.Kind == bluez.DisplayPasskey || p.Kind == bluez.DisplayPinCode {
		a.pendingC = p.Done()
	}
}

// checkPrompt answers no for a prompt that was dismissed without an answer:
// a click elsewhere, Escape, or the icon.
func (a *app) checkPrompt() {
	if a.pending == nil || a.host.IsOpen() {
		return
	}
	if a.pending.Kind.NeedsAnswer() {
		a.pending.Answer(false)
	}
	a.pending, a.pendingC = nil, nil
}

func (a *app) dropPrompt() {
	if a.pending == nil {
		return
	}
	a.pending, a.pendingC = nil, nil
	a.host.Close()
}

// refreshShown brings the icon, the tooltip and an open menu up to date.
// An open prompt is left alone: it is not the menu.
func (a *app) refreshShown() {
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
	if a.pending != nil {
		return
	}
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
	a.lastX = x
	if err := a.host.Open(x, buildRows(&a.view, a)); err != nil {
		log.Printf("opening the menu: %v", err)
	}
}

// The actions behind the rows.

// setPower turns the radio on or off with its kill switch, which is what
// actually stops it transmitting and survives a restart of bluetoothd.
// Turning on also powers an adapter BlueZ has left off; one that appears
// after the unblock is powered by BlueZ itself (AutoEnable).
func (a *app) setPower(on bool) {
	if !on {
		if err := rfkill.SetBlocked(rfkill.DefaultPath, rfkill.TypeBluetooth, true); err != nil {
			log.Printf("turning Bluetooth off: %v", err)
		}
		return
	}
	if a.view.soft {
		if err := rfkill.SetBlocked(rfkill.DefaultPath, rfkill.TypeBluetooth, false); err != nil {
			log.Printf("turning Bluetooth on: %v", err)
			return
		}
	}
	if adapter := a.view.state.Adapter; adapter != nil && !adapter.Powered {
		path := adapter.Path
		a.async(callTimeout, func(ctx context.Context) error { return a.bt.SetPowered(ctx, path, true) })
	}
}

func (a *app) toggleDevice(d *bluez.Device) {
	path, name := d.Path, d.Name
	if d.Connected {
		a.async(connectTimeout, func(ctx context.Context) error {
			if err := a.bt.Disconnect(ctx, path); err != nil {
				return fmt.Errorf("disconnecting %s: %w", name, err)
			}
			return nil
		})
		return
	}
	a.async(connectTimeout, func(ctx context.Context) error {
		if err := a.bt.Connect(ctx, path); err != nil {
			return fmt.Errorf("connecting %s: %w", name, err)
		}
		return nil
	})
}

func (a *app) answer(accept bool) {
	p := a.pending
	a.pending, a.pendingC = nil, nil
	if p != nil && p.Kind.NeedsAnswer() {
		a.debugf("agent: answered %t", accept)
		p.Answer(accept)
	}
}

func (a *app) settings() {
	app := &desktop.App{Name: "Bluetooth Manager", Argv: []string{"blueman-manager"}}
	if err := launch.SpawnDetached(app); err != nil {
		log.Printf("opening Bluetooth settings: %v", err)
	}
}

const (
	// reloadScript brings back a controller that failed to start (on this
	// MacBook: "hci0: BCM: Reset failed (-110)" at boot). One program, so
	// pkexec asks for the password once rather than for each modprobe.
	reloadScript = "/usr/sbin/modprobe -r hci_uart && /usr/sbin/modprobe hci_uart"

	// reloadTimeout includes the time it takes to type a password.
	reloadTimeout = 2 * time.Minute

	// pkexec's own exit statuses, as its manual gives them. Anything else
	// is the script's.
	pkexecDismissed     = 126
	pkexecNotAuthorized = 127
)

// reloadDriver reloads the Bluetooth driver through pkexec, which has
// polkit ask for the password in its own dialog -- the user chose that
// over a standing sudo rule. It runs off the main loop, since the dialog
// waits on the user; the adapter coming back shows up as BlueZ signals.
func (a *app) reloadDriver() {
	if !a.reloading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.reloading.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), reloadTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "pkexec", "/bin/sh", "-c", reloadScript).CombinedOutput()
		msg, failed := reloadOutcome(err, out)
		if failed {
			a.lim.Printf("reload", "%s", msg)
			return
		}
		log.Print(msg)
	}()
}

// reloadOutcome says how a reload ended. A dismissed password dialog is the
// user changing their mind, not a failure.
func reloadOutcome(err error, out []byte) (msg string, failed bool) {
	detail := ""
	if s := strings.TrimSpace(string(out)); s != "" {
		detail = ": " + s
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "Bluetooth driver reloaded", false
	case errors.As(err, &exit) && exit.ExitCode() == pkexecDismissed:
		return "reloading the Bluetooth driver: the password dialog was dismissed", false
	case errors.As(err, &exit) && exit.ExitCode() == pkexecNotAuthorized:
		return "reloading the Bluetooth driver: not authorised" + detail, true
	}
	return fmt.Sprintf("reloading the Bluetooth driver: %v%s", err, detail), true
}

// async runs a BlueZ request off the main loop: connecting waits on the
// device, which can take seconds. Success shows up as signals.
func (a *app) async(timeout time.Duration, f func(context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := f(ctx); err != nil {
			a.lim.Printf("request", "%v", err)
		}
	}()
}

// readEvents pumps X events into a channel, so the main loop can wait on
// them and everything else at once.
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
// A prompt still open is answered no, so BlueZ is not left waiting.
func (a *app) close() error {
	var errs []error
	if a.pending != nil && a.pending.Kind.NeedsAnswer() {
		a.pending.Answer(false)
	}
	if a.host != nil {
		errs = append(errs, a.host.Release())
	}
	if a.agent != nil && a.sys.Connected() {
		errs = append(errs, a.agent.Close())
	}
	if a.radio != nil {
		errs = append(errs, a.radio.Close())
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
