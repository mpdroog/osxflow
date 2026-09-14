// Command soundmenu is a sound menu in the panel's status tray: a speaker
// icon that opens a macOS-style menu with the volume, the output and input
// devices, the microphone, and the media that is playing.
//
// It replaces the XFCE panel's PulseAudio plugin, including the part of it
// that is easy to miss: the plugin held the volume keys. soundmenu takes
// them over, and shows macOS's volume overlay when they are pressed.
//
// Sound goes through PipeWire's PulseAudio server (internal/audio, pure Go)
// and media players through MPRIS on the session bus (internal/mpris). The
// menu and the overlay are X11 (internal/menu).
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

	"github.com/mpdroog/osxflow/internal/audio"
	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/mpris"
	"github.com/mpdroog/osxflow/internal/sni"
)

func main() {
	log.SetPrefix("soundmenu: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

// start is separate from main so that the deferred cleanup actually runs.
func start() error {
	var (
		scaleFlag = flag.Float64("scale", 0, "display scale factor (0 detects it)")
		verbose   = flag.Bool("v", false, "log registrations, clicks, scrolls and icon changes")
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

	// settle is how long a burst of sound server events is left to finish
	// before the state is read again. Short: a volume key's effect should
	// reach the icon before the next press.
	settle = 40 * time.Millisecond

	// reconnectEvery is how often a lost sound server is tried again.
	// PipeWire restarting -- a package upgrade, a user service restart --
	// is ordinary, and should not end the tray icon.
	reconnectEvery = 2 * time.Second

	// playerTimeout bounds a call to a media player, which can be slow or
	// hung in a way the sound server never is.
	playerTimeout = 2 * time.Second

	logBurst = 5
	logEvery = time.Minute
)

type app struct {
	X    *xgbutil.XUtil
	conn *xgb.Conn
	host *menu.Host
	keys map[xproto.Keycode]keyAction
	osd  osd

	session   *dbus.Conn
	sound     *audio.Client
	soundC    <-chan struct{}
	players   *mpris.Client
	playersC  <-chan struct{}
	item      *sni.Item
	reconnect *time.Timer
	retryC    <-chan time.Time

	view view
	icon iconState
	tip  sni.ToolTip

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
	a.grabKeys()
	a.connectSound()

	if a.session, err = dbus.ConnectSessionBus(); err != nil {
		return abandon(fmt.Errorf("connecting to the session bus: %w", err))
	}
	a.players = mpris.New(a.session)
	if a.playersC, err = a.players.Watch(); err != nil {
		// The menu still shows a player when it opens; it just does not
		// follow one that changes track while it is open.
		log.Printf("media players: %v", err)
	}
	a.readPlayers()

	a.icon, a.tip = iconFor(&a.view), tooltipFor(&a.view)
	a.item, err = sni.Export(a.session, &sni.Options{
		ID:       "osxflow-soundmenu",
		Title:    "Sound",
		Category: "Hardware",
		Icon:     iconPixmaps(a.icon),
		ToolTip:  a.tip,
	})
	if err != nil {
		return abandon(err)
	}
	return a, nil
}

// connectSound connects to the sound server, or arranges to try again.
func (a *app) connectSound() {
	c, err := audio.Connect("osxflow soundmenu")
	if err != nil {
		a.lim.Printf("sound", "connecting to the sound server: %v; trying again every %v", err, reconnectEvery)
		a.reconnect = time.NewTimer(reconnectEvery)
		a.retryC = a.reconnect.C
		return
	}
	if a.view.connected {
		return
	}
	a.sound, a.soundC = c, c.Changes()
	a.view.connected = true
	a.readSound()
}

// lostSound drops a connection the server has closed, shows that, and
// starts trying again.
func (a *app) lostSound() {
	log.Printf("lost the sound server; trying again every %v", reconnectEvery)
	if err := a.sound.Close(); err != nil {
		a.lim.Printf("sound", "closing the old sound server connection: %v", err)
	}
	a.sound, a.soundC = nil, nil
	a.view.connected, a.view.audio = false, audio.State{}
	a.refreshShown()
	a.reconnect = time.NewTimer(reconnectEvery)
	a.retryC = a.reconnect.C
}

// run is the main loop. Everything that touches the menu, the overlay or
// the view happens on it. It ends only in failure: X or the session bus
// going away is how the session ends.
func (a *app) run() error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go a.readEvents(events, xGone)
	defer a.stopTimers()

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

		case _, ok := <-a.soundC:
			if !ok {
				a.lostSound()
				continue
			}
			if a.refreshC == nil {
				a.refreshTimer = time.NewTimer(settle)
				a.refreshC = a.refreshTimer.C
			}

		case <-a.retryC:
			a.reconnect, a.retryC = nil, nil
			a.connectSound()
			a.refreshShown()

		case _, ok := <-a.playersC:
			if !ok {
				log.Print("media players: watch ended; the menu no longer follows them")
				a.playersC = nil
				continue
			}
			a.readPlayers()
			a.updateMenu()

		case <-a.refreshC:
			a.refreshTimer, a.refreshC = nil, nil
			a.readSound()
			a.refreshShown()

		case <-a.osd.hideC:
			a.osd.hide()

		case <-a.session.Context().Done():
			return fmt.Errorf("lost the session bus: %w", context.Cause(a.session.Context()))
		}
	}
}

func (a *app) handle(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if a.osd.expose(e) {
			return
		}
	case xproto.KeyPressEvent:
		// Also while a menu is open: its keyboard grab brings the keys to
		// the menu window, but they are still the volume keys.
		if act, ok := a.keys[e.Detail]; ok {
			a.pressed(act)
			return
		}
	}
	if err := a.host.Handle(ev); err != nil {
		a.lim.Printf("menu", "drawing the menu: %v", err)
	}
}

// readSound takes a fresh snapshot. A failure keeps the previous state: a
// menu claiming there is no output when the server merely stumbled is
// worse than one a moment out of date.
func (a *app) readSound() {
	if a.sound == nil {
		return
	}
	st, err := a.sound.Snapshot()
	if err != nil {
		a.lim.Printf("sound", "reading the sound server: %v", err)
		return
	}
	a.view.audio = st
}

func (a *app) readPlayers() {
	ctx, cancel := context.WithTimeout(context.Background(), playerTimeout)
	defer cancel()
	players, err := a.players.Players(ctx)
	if err != nil {
		// What could be read is still shown.
		a.lim.Printf("players", "media players: %v", err)
	}
	a.view.players = players
}

// refreshShown brings the icon, the tooltip and any open menu up to date
// with the view.
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
	a.updateMenu()
}

func (a *app) updateMenu() {
	if err := a.host.Update(buildRows(&a.view, a)); err != nil {
		log.Printf("redrawing the menu: %v; it has been closed", err)
	}
}

// clicked opens or closes the menu, or turns the volume with the wheel.
func (a *app) clicked(c sni.Click) {
	switch c.Kind {
	case sni.SecondaryActivate:
		return
	case sni.Scroll:
		a.debugf("scroll: delta %d horizontal %t", c.Delta, c.Horizontal)
		if !c.Horizontal && c.Delta != 0 {
			if c.Delta > 0 {
				a.pressed(keyRaise)
			} else {
				a.pressed(keyLower)
			}
		}
		return
	case sni.Activate, sni.ContextMenu:
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
	a.readPlayers()
	if err := a.host.Open(x, buildRows(&a.view, a)); err != nil {
		log.Printf("opening the menu: %v", err)
	}
}

// pressed acts on a volume key, or the wheel over the icon, and shows the
// overlay.
func (a *app) pressed(k keyAction) {
	switch k {
	case keyRaise, keyLower:
		out := a.view.audio.DefaultOutput()
		if out == nil {
			return
		}
		name, v := out.Name, nextVolume(out.Volume, k == keyRaise)
		muted := out.Muted
		// Turning it up unmutes, as on macOS: nobody presses volume-up
		// wanting silence.
		if muted && k == keyRaise {
			a.setMute(true, name, false)
			muted = false
		}
		a.setVolume(true, name, v, true)
		a.osd.show(a.host, osdView{kind: osdVolume, level: v, muted: muted})
	case keyMute:
		out := a.view.audio.DefaultOutput()
		if out == nil {
			return
		}
		a.setMute(true, out.Name, !out.Muted)
		a.osd.show(a.host, osdView{kind: osdVolume, level: out.Volume, muted: out.Muted})
	case keyMicMute:
		in := a.view.audio.DefaultInput()
		if in == nil {
			return
		}
		a.setMute(false, in.Name, !in.Muted)
		a.osd.show(a.host, osdView{kind: osdMic, level: in.Volume, muted: in.Muted})
	}
}

// device finds a device in the view, to record a change as soon as the
// server has accepted it. The server's own event follows a moment later;
// without this, a second key press before it arrives would step from the
// old volume again.
func (a *app) device(output bool, name string) *audio.Device {
	list := a.view.audio.Inputs
	if output {
		list = a.view.audio.Outputs
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// The actions behind the menu's rows. The sound server is a local socket
// and answers in well under a millisecond, so these run on the main loop,
// which keeps a drag's changes in order.

func (a *app) setVolume(output bool, name string, value float64, final bool) {
	if a.sound == nil {
		return
	}
	if err := a.sound.SetVolume(output, name, value); err != nil {
		a.lim.Printf("volume", "setting the volume of %s: %v", name, err)
		return
	}
	if d := a.device(output, name); d != nil {
		d.Volume = value
	}
	if final {
		a.refreshShown()
	}
}

func (a *app) setMute(output bool, name string, mute bool) {
	if a.sound == nil {
		return
	}
	if err := a.sound.SetMute(output, name, mute); err != nil {
		a.lim.Printf("mute", "muting %s: %v", name, err)
		return
	}
	if d := a.device(output, name); d != nil {
		d.Muted = mute
	}
	a.refreshShown()
}

func (a *app) setDefault(output bool, name string) {
	if a.sound == nil {
		return
	}
	if err := a.sound.SetDefault(output, name); err != nil {
		a.lim.Printf("default", "switching to %s: %v", name, err)
	}
	// The server's event redraws the menu with the new default.
}

func (a *app) playPause(bus string) { a.player(bus, a.players.PlayPause) }
func (a *app) next(bus string)      { a.player(bus, a.players.Next) }
func (a *app) previous(bus string)  { a.player(bus, a.players.Previous) }

// player calls a media player off the main loop: a hung player must not
// freeze the menu. What it did arrives as its own property change.
func (a *app) player(bus string, call func(context.Context, string) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), playerTimeout)
		defer cancel()
		if err := call(ctx, bus); err != nil {
			a.lim.Printf("player:"+bus, "media player %s: %v", bus, err)
		}
	}()
}

func (a *app) settings() {
	app := &desktop.App{Name: "Volume Control", Argv: []string{"pavucontrol"}}
	if err := launch.SpawnDetached(app); err != nil {
		log.Printf("opening sound settings: %v", err)
	}
}

func (a *app) stopTimers() {
	for _, t := range []*time.Timer{a.refreshTimer, a.reconnect} {
		if t != nil {
			t.Stop()
		}
	}
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
func (a *app) close() error {
	var errs []error
	a.osd.close()
	if a.host != nil {
		errs = append(errs, a.host.Release())
	}
	if a.sound != nil {
		errs = append(errs, a.sound.Close())
	}
	if a.item != nil && a.session.Connected() {
		errs = append(errs, a.item.Close())
	}
	if a.session != nil && a.session.Connected() {
		if err := a.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing the session bus: %w", err))
		}
	}
	a.conn.Close()
	return errors.Join(errs...)
}
