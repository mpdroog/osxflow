// Command dock is a macOS-style dock for XFCE: a magnifying, auto-hiding
// row of applications and stacks along the bottom of the screen.
//
// It replaces plank, which is a Vala/GTK3 program carrying GObject, Cairo
// and libgee behind it, and which draws this machine's dock at about 26
// physical pixels because it does not apply the desktop's 2x scale
// factor. This is one static binary with no cgo, no runtime icon-theme
// lookup and no configuration: the contents are the list in theme.go.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"slices"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/stack"
	"github.com/mpdroog/osxflow/internal/xmon"
	"github.com/mpdroog/osxflow/internal/xsurface"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func main() {
	// Before anything else, so that every line -- including the ones
	// newDock logs while it is still starting -- is marked as the dock's in
	// a session log shared with every other program started at login.
	log.SetPrefix("dock: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	if err := start(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// start is separate from main so that the deferred cleanup actually runs:
// os.Exit does not unwind, so a defer in main alongside it is a defer that
// never fires.
func start() error {
	var (
		scaleFlag = flag.Float64("scale", 0, "display scale factor (0 detects it)")
		verbose   = flag.Bool("v", false, "log what the dock is doing")
	)
	flag.Parse()

	d, err := newDock(*scaleFlag, *verbose)
	if err != nil {
		return err
	}
	defer d.close()
	return d.run()
}

type dockApp struct {
	X        *xgbutil.XUtil
	conn     *xgb.Conn
	screen   *xproto.ScreenInfo
	visual   argbVisual
	colormap xproto.Colormap

	win xproto.Window
	// triggers is one reveal strip per monitor. A single strip along the
	// bottom of the X screen -- the union of every monitor -- only works
	// when they are all the same height: here the shorter screen's bottom
	// row is 720px above the union's, so its strip was at a y the pointer
	// could never reach and the dock could not be summoned there at all.
	triggers []xproto.Window

	// mons is every monitor, mon the one the dock is currently on.
	mons []xmon.Rect
	mon  xmon.Rect
	surf *xsurface.Surface

	th    *theme
	set   *icons.Set
	faces *faces

	builder *dock.Builder
	server  xwin.Server
	opener  *launch.Launcher
	index   *launch.Index

	items  []dock.Item
	stacks map[int]dock.Stack
	places []dock.Placement

	reveal *dock.Reveal
	zoom   dock.Anim

	// cursorX is the pointer's position in window coordinates, and hover
	// the item under it, or -1.
	cursorX float64
	hover   int

	// inside records whether the pointer is over the panel itself, as
	// opposed to merely inside the window, which is much larger.
	inside bool

	// stale records that the row changed while the dock was off screen, so
	// the next reveal owes a repaint before it slides up.
	stale bool

	// poll is set when the root window would not tell us about window
	// changes, which is the one case where the dock has to ask on its own.
	poll bool

	winW int
	winX int

	popup *popup

	// fallbacks caches placeholder tiles for applications with no embedded
	// icon, keyed by name and size.
	fallbacks map[fallbackKey]*image.RGBA

	clientList xproto.Atom
	verbose    bool

	// lim bounds the errors that can arrive by the dozen: X protocol errors
	// for unchecked requests, which a failing upload produces several of per
	// frame, and the window-list failures below.
	lim *errlog.Limiter

	// listErr and buildErr are the last failure refreshItems reported, so a
	// failure that persists is said once rather than on every change to the
	// window list, and its clearing is said too.
	listErr  string
	buildErr string

	// buildWarnings collects what the Builder could not find out during one
	// Build; see refreshItems.
	buildWarnings []error
}

type fallbackKey struct {
	name string
	size int
}

func newDock(scaleOverride float64, verbose bool) (*dockApp, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connecting to X display: %w", err)
	}
	d := &dockApp{
		X:         X,
		conn:      X.Conn(),
		verbose:   verbose,
		set:       icons.New(),
		reveal:    dock.NewReveal(),
		zoom:      dock.Anim{Tau: 0.045},
		hover:     -1,
		fallbacks: make(map[fallbackKey]*image.RGBA),
		lim:       &errlog.Limiter{Burst: 5, Per: time.Minute},
	}
	d.screen = xproto.Setup(d.conn).DefaultScreen(d.conn)

	visual, ok := findARGB(d.screen)
	if !ok {
		d.conn.Close()
		return nil, errors.New("no 32-bit TrueColor visual; the dock needs a compositor " +
			"(xfwm4: Settings > Window Manager Tweaks > Compositor)")
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
		d.conn.Close()
		return nil, err
	}

	if missing := icons.Verify([]string{icons.Downloads, icons.Trash, icons.TrashFull}); missing != nil {
		log.Printf("warning: %v", missing)
	}

	apps, problems := desktop.Scan()
	for _, p := range problems {
		log.Printf("warning: %v", p)
	}
	d.index = launch.NewIndex(apps)
	// Build skips a pin whose application is gone without a word, because
	// it runs on every window change. The word belongs here, once: the
	// list in theme.go has drifted from what is installed.
	for _, id := range pinnedIDs {
		if d.index.ByDesktopID(id) == nil {
			log.Printf("warning: pinned %s is not installed", id)
		}
	}

	server, err := xwin.NewServerWith(X)
	if err != nil {
		d.faces.close()
		d.conn.Close()
		return nil, err
	}
	d.server = server
	d.opener = launch.New(server)

	// Neither folder is worth refusing to start over. A downloads folder
	// that is not the configured one still comes back as ~/Downloads; a
	// trash that cannot be found leaves the Builder to say so as the row is
	// built.
	trashDir, trashErr := stack.TrashDir()
	if trashErr != nil {
		log.Printf("warning: %v", trashErr)
	}
	downloadsDir, downloadsErr := stack.DownloadsDir()
	if downloadsErr != nil {
		log.Printf("warning: %v", downloadsErr)
	}
	d.builder = &dock.Builder{
		PinnedIDs:    pinnedIDs,
		Index:        d.index,
		TrashDir:     trashDir,
		DownloadsDir: downloadsDir,
		Warn:         func(err error) { d.buildWarnings = append(d.buildWarnings, err) },
	}

	d.refreshItems()
	if winErr := d.createWindows(); winErr != nil {
		d.faces.close()
		d.conn.Close()
		return nil, winErr
	}
	// The root window tells us when the set of open windows changes, which
	// is what keeps the running indicators honest without polling.
	//
	// Under Wayland it cannot -- and, the usual trap, it does not fail
	// while not doing it. labwc really does set _NET_CLIENT_LIST and
	// really does send the PropertyNotify, but only ever for XWayland
	// clients, because those are the only windows the X server knows
	// about. Firefox and foot are native Wayland toplevels: opening one
	// changes nothing on the root, so no notification arrives and the row
	// stays frozen at whatever happened to be running when the dock
	// started. Every call succeeded. They just answered a question about a
	// smaller world than the one being asked.
	//
	// So ask instead, in the moment the fallback below was already written
	// for: just before the dock slides up.
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		d.poll = true
	} else if watchErr := d.watchRoot(); watchErr != nil {
		log.Printf("warning: %v", watchErr)
		// Without the notification the row can only be kept honest by
		// asking, and the moment worth asking in is just before the dock
		// appears.
		d.poll = true
	}
	return d, nil
}

// createWindows sizes and creates the dock and its reveal trigger.
func (d *dockApp) createWindows() error {
	d.winW = int(dock.MaxWidth(d.items, &d.th.m) + 0.5)
	d.mons = xmon.All(d.conn, d.screen)
	d.mon = xmon.Active(d.conn, d.screen)
	d.winX = d.mon.X + (d.mon.W-d.winW)/2

	// Created at its hidden position, so nothing flashes on screen before
	// the first paint.
	if err := d.createDockWindow(d.winW, d.th.winH, d.winX, d.mon.Bottom()); err != nil {
		return err
	}
	var err error
	d.surf, err = xsurface.New(d.conn, d.win, d.visual.depth, d.winW, d.th.winH)
	if err != nil {
		return err
	}

	// One trigger per monitor, each spanning that monitor's own width at
	// that monitor's own bottom edge: reaching the bottom of whichever
	// screen you are working on brings the dock there, as macOS does.
	for _, m := range d.mons {
		if err = d.createTriggerWindow(m.W, triggerH, m.X, m.Bottom()-triggerH); err != nil {
			return err
		}
	}

	for _, win := range append([]xproto.Window{d.win}, d.triggers...) {
		if mapErr := xproto.MapWindowChecked(d.conn, win).Check(); mapErr != nil {
			return fmt.Errorf("mapping window 0x%x: %w", win, mapErr)
		}
	}

	// Mapped is not enough: a strip that is never drawn into never becomes
	// a Wayland surface the pointer can land on. One rectangle each, after
	// the map, and the hover works. The triggers were appended in the order
	// of d.mons, so the widths line up.
	for i, win := range d.triggers {
		if damageErr := d.damageTrigger(win, d.mons[i].W, triggerH); damageErr != nil {
			return damageErr
		}
	}
	return d.paint()
}

// watchRoot asks for PropertyNotify on the root window.
func (d *dockApp) watchRoot() error {
	atomID, err := atom(d.conn, "_NET_CLIENT_LIST")
	if err != nil {
		return err
	}
	d.clientList = atomID
	return xproto.ChangeWindowAttributesChecked(d.conn, d.screen.Root,
		xproto.CwEventMask, []uint32{uint32(xproto.EventMaskPropertyChange)}).Check()
}

// refreshItems rebuilds the row from the current window list, and reports
// whether anything the dock draws actually changed.
//
// Most window changes do not change it. A second window opening for an
// application that already had one moves no icon and lights no dot that was
// dark, and the dock is told about every one of them, so answering "no" here
// is what stops a full repaint from being the response to nothing.
func (d *dockApp) refreshItems() bool {
	wins, err := d.server.Windows()
	if err != nil {
		// Losing the window list costs the running indicators, not the
		// dock. Carrying on with an empty list would blank every dot; using
		// the previous one is closer to the truth.
		//
		// This runs on every change to the window list, so a failure that
		// persists is said once, and again only when it changes or clears.
		if msg := err.Error(); msg != d.listErr {
			d.lim.Printf("windows", "listing windows: %v", err)
			d.listErr = msg
		}
		if d.items != nil {
			return false
		}
		wins = nil
	} else if d.listErr != "" {
		log.Print("window list recovered")
		d.listErr = ""
	}
	d.buildWarnings = d.buildWarnings[:0]
	items, stacks := d.builder.Build(wins)
	d.reportBuild()
	for i := range items {
		if short, ok := displayNames[items[i].DesktopID]; ok {
			items[i].Name = short
		}
	}
	changed := !slices.Equal(d.items, items)
	d.items, d.stacks = items, stacks
	return changed
}

// reportBuild logs what the last Build warned about, on the same terms as
// a failing window list: once while it persists, and once when it clears.
func (d *dockApp) reportBuild() {
	var msg string
	if len(d.buildWarnings) > 0 {
		msg = errors.Join(d.buildWarnings...).Error()
	}
	switch msg {
	case d.buildErr:
		return
	case "":
		log.Printf("resolved: %s", d.buildErr)
	default:
		d.lim.Printf("build", "%s", msg)
	}
	d.buildErr = msg
}

// resizeIfNeeded grows or shrinks the window when the number of items
// changes, which happens when an unpinned application starts or stops.
func (d *dockApp) resizeIfNeeded() error {
	want := int(dock.MaxWidth(d.items, &d.th.m) + 0.5)
	if want == d.winW {
		return nil
	}
	d.winW = want
	d.winX = d.mon.X + (d.mon.W-d.winW)/2

	// Checked, unlike the slide: this happens when an application starts
	// or stops, not sixty times a second, and a window that did not take
	// its new width would have the new surface copied into it wrongly.
	// Logged rather than returned, though. Both of these act on the dock's
	// own ids and next to never fail; if one does, a dock drawn at the old
	// width until the next change is far better than no dock at all, which
	// is what returning up the event loop would make of it.
	if err := xproto.ConfigureWindowChecked(d.conn, d.win,
		xproto.ConfigWindowX|xproto.ConfigWindowWidth,
		[]uint32{geom.I32AsU32(d.winX), geom.U32(d.winW)}).Check(); err != nil {
		log.Printf("resizing the dock: %v", err)
	}
	if err := d.surf.Close(); err != nil {
		log.Printf("releasing the old surface: %v", err)
	}
	surf, err := xsurface.New(d.conn, d.win, d.visual.depth, d.winW, d.th.winH)
	if err != nil {
		return err
	}
	d.surf = surf
	return nil
}

// triggerH is how tall the reveal strip along the bottom of the screen is.
//
// Deliberately a few pixels rather than one: a single-pixel target is
// missed by a fast flick of the mouse, because X delivers motion in jumps
// and the pointer can cross a one-pixel row without ever being reported
// inside it.
const triggerH = 3

// frameInterval paces animation. It only runs while something is actually
// moving -- the slide, or the zoom settling -- so the dock costs nothing
// while it sits hidden, which is nearly all of the time.
const frameInterval = 8 * time.Millisecond

// hideDelay is how long the dock waits after the pointer leaves before it
// slides away. Long enough that crossing a corner does not dismiss it,
// short enough that it does not feel stuck.
const hideDelay = 350 * time.Millisecond

func (d *dockApp) run() error {
	events := make(chan xgb.Event, 64)
	// errc says why the reader stopped; done stops a reader that is still
	// running when this loop returns with an error of its own, which would
	// otherwise block for ever on a send nobody receives.
	errc := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go d.readEvents(events, errc, done)

	var (
		ticker *time.Ticker
		tick   <-chan time.Time
		hideAt *time.Timer
		hide   <-chan time.Time
		last   = time.Now()
	)
	animate := func(on bool) {
		switch {
		case on && ticker == nil:
			ticker = time.NewTicker(frameInterval)
			tick = ticker.C
			last = time.Now()
		case !on && ticker != nil:
			ticker.Stop()
			ticker, tick = nil, nil
		}
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
		if hideAt != nil {
			hideAt.Stop()
		}
	}()

	scheduleHide := func() {
		if hideAt != nil {
			hideAt.Stop()
		}
		hideAt = time.NewTimer(hideDelay)
		hide = hideAt.C
	}
	cancelHide := func() {
		if hideAt != nil {
			hideAt.Stop()
			hideAt = nil
		}
		hide = nil
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// The connection closed under the dock. At logout that is the
				// session ending, but it is also what a crashed server looks
				// like, and a dock that vanished with status 0 and no word
				// would be a mystery; it says so and exits with an error.
				return <-errc
			}
			for _, e := range coalesce(ev, events) {
				show, hidden, err := d.handle(e)
				if err != nil {
					return err
				}
				switch {
				case show:
					cancelHide()
				case hidden:
					scheduleHide()
				}
			}
			animate(!d.settled())

		case <-hide:
			hide = nil
			if d.popup == nil {
				d.reveal.Hide()
				d.zoom.Target = 0
				animate(true)
			}

		case <-tick:
			now := time.Now()
			dt := now.Sub(last).Seconds()
			last = now
			moved := d.reveal.Step(dt)
			zoomed := d.zoom.Step(dt)
			if moved {
				d.slide()
			}
			if zoomed {
				if err := d.paint(); err != nil {
					return err
				}
			}
			animate(!d.settled())
		}
	}
}

func (d *dockApp) settled() bool { return d.reveal.Settled() && d.zoom.Done() }

// slide moves the window to match the reveal animation. No repainting is
// involved: the panel's pixels do not change as it comes and goes, only
// where they are, so this is one ConfigureWindow per frame rather than a
// megabyte of pixels.
func (d *dockApp) slide() {
	y := float64(d.mon.Bottom()-d.th.winH) + d.reveal.Offset(float64(d.th.winH))
	d.moveWindow(d.win, d.winX, int(y))
}

// coalesce takes an event and everything already queued behind it, and
// throws away all but the last pointer motion.
//
// X delivers motion as fast as the pointer produces it, and the dock
// repaints on every one. Without this, a fast sweep across the dock builds
// a backlog of motion events, each provoking a full repaint of a position
// the cursor has already left -- the dock ends up rendering the recent
// past, which is precisely what "less responsive" feels like. Only the
// newest motion says anything true about where the pointer is now.
//
// Everything that is not motion is kept, in order: a click or a property
// change is a fact, not a sample.
func coalesce(first xgb.Event, queued <-chan xgb.Event) []xgb.Event {
	batch := []xgb.Event{first}
	// Drain only what has already arrived; never wait for more.
	for drained := false; !drained; {
		select {
		case ev, ok := <-queued:
			if !ok {
				drained = true
				break
			}
			batch = append(batch, ev)
		default:
			drained = true
		}
	}
	if len(batch) == 1 {
		return batch
	}

	// Find the last motion, then drop every earlier one.
	lastMotion := -1
	for i, ev := range batch {
		if _, ok := ev.(xproto.MotionNotifyEvent); ok {
			lastMotion = i
		}
	}
	if lastMotion < 0 {
		return batch
	}
	out := batch[:0]
	for i, ev := range batch {
		if _, ok := ev.(xproto.MotionNotifyEvent); ok && i != lastMotion {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// readEvents pumps X events into a channel so the main loop can wait on
// them and on a timer at the same time. It stops when the connection ends,
// saying why on errc before closing out, or when done closes.
//
// xgb's connection is safe for concurrent use, so requests still go
// straight from the main goroutine; only the blocking read lives here.
func (d *dockApp) readEvents(out chan<- xgb.Event, errc chan<- error, done <-chan struct{}) {
	defer close(out)
	for {
		ev, xerr := d.conn.WaitForEvent()
		if xerr != nil {
			// A protocol error answers an unchecked request sent some time
			// ago -- a frame's PutImage, a raise, a step of the slide. The
			// connection is still good and the dock carries on, but it says
			// so, always: through the limiter, keyed by the kind of error,
			// because a failing upload is half a dozen of these per frame.
			d.lim.Printf(fmt.Sprintf("%T", xerr), "X error: %v", xerr)
			continue
		}
		if ev == nil {
			// Both nil is xgb's only word that the connection has ended. A
			// read failure behind it has already gone to xgb's own logger;
			// nothing more of it reaches us than this.
			errc <- errors.New("lost the connection to the X server")
			return
		}
		select {
		case out <- ev:
		case <-done:
			return
		}
	}
}
