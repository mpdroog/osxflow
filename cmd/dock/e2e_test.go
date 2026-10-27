package main

// End-to-end: the real dock, started the way autostart starts it, on a
// real labwc and XWayland -- headless, so nothing appears on screen -- with
// a virtual pointer sent to the bottom edges a mouse would reach.
//
// It is the only test that covers the dock's one job. Every unit below it
// can be right and the dock still impossible to summon: a strip that
// XWayland never gave a buffer, one under a window raised after it, one at
// a bottom edge that has since moved.
//
// Nothing here sleeps. Every wait is for something to have happened --
// a window mapped, moved or replaced, which X announces on the root window
// -- and its time limit is only how long to wait before calling it a
// failure.
//
// Skipped under -short, and where labwc is not installed.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/xmon"
	"github.com/mpdroog/osxflow/wl"
)

// revealWithin is how long the dock may take to come up or go away. The
// slide itself is a fraction of this; the rest is slack for a loaded
// machine.
const revealWithin = 2 * time.Second

// startWithin bounds each step of bringing the desk up.
const startWithin = 10 * time.Second

// desk is a headless labwc with the dock running on it.
type desk struct {
	t    *testing.T
	x    *xgb.Conn
	root xproto.Window
	ptr  *wl.Pointer
	mons []xmon.Rect
	// w and h are the layout's extent, which is what the virtual pointer's
	// absolute coordinates are fractions of.
	w, h int

	// changed is signalled after every X event on the root window's
	// children: anything that maps, moves, restacks or goes away. until
	// re-checks its condition on each.
	changed chan struct{}
}

// startDesk starts labwc with two monitors and the dock as its autostart,
// and waits until the dock's window is up. With withTile, cmd/tile is
// started beside it, as the real autostart does, on a session bus of its
// own so that it does not find the user's tile and step aside.
func startDesk(t *testing.T, withTile bool) *desk {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a compositor")
	}
	if _, err := exec.LookPath("labwc"); err != nil {
		t.Skip("labwc is not installed")
	}
	dir := t.TempDir()

	bin := filepath.Join(dir, "dock")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building the dock: %v\n%s", err, out)
	}
	tileBin := filepath.Join(dir, "tile")
	if withTile {
		if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", tileBin, "../tile").CombinedOutput(); err != nil {
			t.Fatalf("building tile: %v\n%s", err, out)
		}
	}
	// tile finds rc.xml through XDG_CONFIG_HOME, and must write the test's,
	// not the user's.
	cfg := filepath.Join(dir, "labwc")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "rc.xml"), []byte("<labwc_config></labwc_config>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The dock starts as labwc's startup command, which is as close to a
	// login as a test gets: XWayland is not up yet, and the outputs are
	// still being configured, when the dock first asks about either. The
	// command says which displays it got down a FIFO, which the test reads
	// -- blocking until it has been written, rather than looking for a file
	// until one appears.
	envFIFO := filepath.Join(dir, "env")
	if err := syscall.Mkfifo(envFIFO, 0o600); err != nil {
		t.Fatal(err)
	}
	dockLog := filepath.Join(dir, "dock.log")
	tileLog := filepath.Join(dir, "tile.log")
	tileStart := ""
	if withTile {
		tileStart = fmt.Sprintf("%s > %s 2>&1 & ", tileBin, tileLog)
	}
	startup := fmt.Sprintf(`sh -c 'echo "$WAYLAND_DISPLAY $DISPLAY" > %s; %sexec %s -v > %s 2>&1'`,
		envFIFO, tileStart, bin, dockLog)
	argv := []string{"labwc", "-C", cfg, "-s", startup}
	if withTile {
		argv = append([]string{"dbus-run-session", "--"}, argv...)
	}
	// Not t.Context: that is cancelled before the cleanup below runs, and
	// would SIGKILL labwc alone, leaving XWayland and the dock behind.
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...)
	var labwcLog bytes.Buffer
	cmd.Stdout, cmd.Stderr = &labwcLog, &labwcLog
	cmd.Env = append(withoutDisplays(os.Environ()), "XDG_CONFIG_HOME="+dir,
		"WLR_BACKENDS=headless", "WLR_RENDERER=pixman", "WLR_HEADLESS_OUTPUTS=2", "WLR_LIBINPUT_NO_DEVICES=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting labwc: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		// The whole group: labwc, XWayland, tile and the dock.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Logf("stopping labwc: %v", err)
		}
		if err := <-exited; err != nil {
			t.Logf("labwc: %v", err)
		}
		if t.Failed() {
			out, err := os.ReadFile(dockLog)
			if err != nil {
				t.Logf("reading the dock log: %v", err)
			}
			t.Logf("dock log:\n%s", out)
			if withTile {
				out, _ := os.ReadFile(tileLog) //nolint:errcheck // a missing log is shown as empty
				t.Logf("tile log:\n%s", out)
			}
			t.Logf("labwc log (tail):\n%s", tail(labwcLog.String(), 30))
		}
	})

	wayland, display := readDisplays(t, envFIFO, exited)
	t.Setenv("WAYLAND_DISPLAY", wayland)
	t.Setenv("DISPLAY", display)

	wc, err := wl.Dial()
	if err != nil {
		t.Fatalf("connecting to labwc: %v", err)
	}
	t.Cleanup(func() {
		if discErr := wc.Disconnect(); discErr != nil {
			t.Logf("disconnecting from labwc: %v", discErr)
		}
	})
	ptr, err := wc.VirtualPointer()
	if err != nil {
		t.Fatal(err)
	}

	// labwc starts XWayland on the first connection to it, and holds that
	// connection until it is ready, so there is nothing to retry.
	x, err := xgb.NewConn()
	if err != nil {
		t.Fatalf("connecting to XWayland: %v", err)
	}
	t.Cleanup(x.Close)
	screen := xproto.Setup(x).DefaultScreen(x)
	d := &desk{t: t, x: x, root: screen.Root, ptr: ptr, changed: make(chan struct{}, 1)}
	d.watch()
	d.mons = xmon.All(d.x, screen)
	if len(d.mons) != 2 {
		t.Fatalf("monitors = %v; want two", d.mons)
	}
	for _, m := range d.mons {
		d.w, d.h = max(d.w, m.X+m.W), max(d.h, m.Bottom())
	}

	d.until(startWithin, "the dock's window and its strips", func() bool {
		_, ok := d.dock()
		return ok && len(d.triggers()) == len(d.mons)
	})
	return d
}

// readDisplays reads the startup command's line from the FIFO: the
// displays labwc gave its children.
func readDisplays(t *testing.T, fifo string, exited <-chan error) (wayland, display string) {
	t.Helper()
	type result struct {
		b   []byte
		err error
	}
	got := make(chan result, 1)
	go func() {
		// Opening a FIFO to read blocks until something opens it to write.
		f, err := os.Open(fifo)
		if err != nil {
			got <- result{nil, err}
			return
		}
		defer func() { _ = f.Close() }() //nolint:errcheck // read-only
		b, err := io.ReadAll(f)
		got <- result{b, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("reading labwc's displays: %v", r.err)
		}
		f := strings.Fields(string(r.b))
		if len(f) != 2 {
			t.Fatalf("labwc's startup command said %q; want two displays", r.b)
		}
		return f[0], f[1]
	case err := <-exited:
		t.Fatalf("labwc exited before starting the dock: %v", err)
	case <-time.After(startWithin):
		t.Fatalf("labwc did not run its startup command within %v", startWithin)
	}
	return "", ""
}

// watch asks for every change to the root window's children and signals
// d.changed after each.
func (d *desk) watch() {
	if err := xproto.ChangeWindowAttributesChecked(d.x, d.root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskSubstructureNotify}).Check(); err != nil {
		d.t.Fatalf("watching the root window: %v", err)
	}
	go func() {
		for {
			ev, err := d.x.WaitForEvent()
			if ev == nil && err == nil {
				return // the connection closed
			}
			select {
			case d.changed <- struct{}{}:
			default: // one pending signal is as good as many
			}
		}
	}()
}

// until waits for ok to become true, checking it now and after every
// change to the windows, and fails the test if it is not within the limit.
func (d *desk) until(within time.Duration, what string, ok func() bool) {
	d.t.Helper()
	limit := time.NewTimer(within)
	defer limit.Stop()
	for !ok() {
		select {
		case <-d.changed:
		case <-limit.C:
			d.t.Fatalf("not within %v: %s; windows:%s", within, what, d.windows())
		}
	}
}

// withoutDisplays drops the session's own WAYLAND_DISPLAY and DISPLAY, so
// the nested labwc does not mistake itself for a window on them.
func withoutDisplays(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "WAYLAND_DISPLAY=") || strings.HasPrefix(kv, "DISPLAY=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// mappedWin is one mapped child of the root window.
type mappedWin struct {
	id       xproto.Window
	name     string
	override bool
	geo      xmon.Rect
}

// mapped lists the root window's mapped children, bottom of the stack
// first.
func (d *desk) mapped() []mappedWin {
	tree, err := xproto.QueryTree(d.x, d.root).Reply()
	if err != nil {
		return nil
	}
	var out []mappedWin
	for _, w := range tree.Children {
		attrs, err := xproto.GetWindowAttributes(d.x, w).Reply()
		if err != nil || attrs.MapState != xproto.MapStateViewable {
			continue
		}
		g, err := xproto.GetGeometry(d.x, xproto.Drawable(w)).Reply()
		if err != nil {
			continue
		}
		name, err := xproto.GetProperty(d.x, false, w, xproto.AtomWmName, xproto.AtomString, 0, 16).Reply()
		if err != nil {
			continue
		}
		out = append(out, mappedWin{
			id: w, name: string(name.Value), override: attrs.OverrideRedirect,
			geo: xmon.Rect{X: int(g.X), Y: int(g.Y), W: int(g.Width), H: int(g.Height)},
		})
	}
	return out
}

// windows describes every mapped window, for a failure message.
func (d *desk) windows() string {
	var b strings.Builder
	for _, w := range d.mapped() {
		fmt.Fprintf(&b, "\n  0x%x %q override=%v %dx%d+%d+%d", w.id, w.name, w.override, w.geo.W, w.geo.H, w.geo.X, w.geo.Y)
	}
	return b.String()
}

// dock finds the dock's window and says where it is.
func (d *desk) dock() (xmon.Rect, bool) {
	for _, w := range d.mapped() {
		if w.name == "dock" {
			return w.geo, true
		}
	}
	return xmon.Rect{}, false
}

// triggers lists the dock's reveal strips.
func (d *desk) triggers() []xproto.Window {
	var out []xproto.Window
	for _, w := range d.mapped() {
		if w.name == "dock-trigger" {
			out = append(out, w.id)
		}
	}
	return out
}

// shownOn reports whether the dock is all the way up on m.
func (d *desk) shownOn(m xmon.Rect) bool {
	g, ok := d.dock()
	return ok && g.Bottom() == m.Bottom() && g.X >= m.X && g.X+g.W <= m.X+m.W
}

// hidden reports whether the dock is entirely below its monitor.
func (d *desk) hidden() bool {
	g, ok := d.dock()
	if !ok {
		return true
	}
	m, found := xmon.Containing(d.mons, g.X+g.W/2, 0)
	return !found || g.Y >= m.Bottom()
}

func (d *desk) moveTo(x, y int) {
	d.t.Helper()
	if err := d.ptr.MoveTo(uint32(x), uint32(y), uint32(d.w), uint32(d.h)); err != nil {
		d.t.Fatalf("moving the pointer: %v", err)
	}
}

// summon puts the pointer at the bottom of m, a quarter of the way in --
// off to one side of the dock, which is the case with no dock under the
// pointer to keep it up -- and expects the dock there within the limit;
// then moves away and expects it gone.
func (d *desk) summon(m xmon.Rect, within time.Duration) {
	d.t.Helper()
	d.moveTo(m.X+m.W/2, m.Y+m.H/2)
	d.moveTo(m.X+m.W/4, m.Bottom()-1)
	d.until(within, fmt.Sprintf("the dock to come up on %v", m), func() bool { return d.shownOn(m) })
	d.moveTo(m.X+m.W/2, m.Y+m.H/2)
	d.until(revealWithin, "the dock to hide", d.hidden)
}

// healed waits until the dock has replaced every strip it has now.
func (d *desk) healed() {
	d.t.Helper()
	old := d.triggers()
	d.until(healInterval+startWithin, "the strips to be rebuilt", func() bool {
		now := d.triggers()
		if len(now) != len(d.mons) {
			return false
		}
		for _, w := range now {
			if slices.Contains(old, w) {
				return false
			}
		}
		return true
	})
}

// window opens an ordinary, managed X window on m, as an application
// would, and returns a function that closes it.
func (d *desk) window(m xmon.Rect) func() {
	d.t.Helper()
	// labwc puts a new window on the monitor with the pointer, whatever
	// position it asks for.
	d.moveTo(m.X+m.W/2, m.Y+m.H/2)
	win, err := xproto.NewWindowId(d.x)
	if err != nil {
		d.t.Fatal(err)
	}
	screen := xproto.Setup(d.x).DefaultScreen(d.x)
	if err := xproto.CreateWindowChecked(d.x, screen.RootDepth, win, d.root,
		int16(m.X+m.W/4), int16(m.Y+m.H/4), uint16(m.W/2), uint16(m.H/2), 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixel, []uint32{screen.WhitePixel}).Check(); err != nil {
		d.t.Fatal(err)
	}
	if err := xproto.MapWindowChecked(d.x, win).Check(); err != nil {
		d.t.Fatal(err)
	}
	// Placed: mapped, and on m. Until then moving the pointer on would
	// put it wherever the pointer went next.
	d.until(startWithin, fmt.Sprintf("window 0x%x to be placed on %v", win, m), func() bool {
		for _, w := range d.mapped() {
			if w.id == win {
				return m.Contains(w.geo.X, w.geo.Y)
			}
		}
		return false
	})
	return func() {
		if err := xproto.DestroyWindowChecked(d.x, win).Check(); err != nil {
			d.t.Fatal(err)
		}
	}
}

// tiledOn reports whether a managed window reaches m's bottom edge -- tile
// having laid one out there.
func (d *desk) tiledOn(m xmon.Rect) bool {
	for _, w := range d.mapped() {
		// labwc draws the border itself, so a root child at its client
		// geometry ends a pixel or so short of the edge.
		if !w.override && w.geo.X >= m.X && w.geo.X+w.geo.W <= m.X+m.W && w.geo.Bottom() >= m.Bottom()-2 {
			return true
		}
	}
	return false
}

// TestE2EColdStart is the login: the dock started with the compositor,
// nothing touched, then the pointer sent to each bottom edge.
func TestE2EColdStart(t *testing.T) {
	d := startDesk(t, false)
	if !d.hidden() {
		t.Fatal("the dock is up before anything asked for it")
	}
	for _, m := range d.mons {
		d.summon(m, revealWithin)
	}
}

// TestE2EAfterHeal waits out two rebuilds of the strips and checks the
// new ones work as the first did.
func TestE2EAfterHeal(t *testing.T) {
	d := startDesk(t, false)
	d.healed()
	d.healed()
	for _, m := range d.mons {
		d.summon(m, revealWithin)
	}
}

// TestE2ECoveredEdge maps an ordinary X window over the whole of the
// first monitor, bottom edge included, and raises it above everything the
// dock owns. The dock must still come up -- at the latest once heal has
// put a new strip on top, under the waiting pointer.
func TestE2ECoveredEdge(t *testing.T) {
	d := startDesk(t, false)
	m := d.mons[0]
	win, err := xproto.NewWindowId(d.x)
	if err != nil {
		t.Fatal(err)
	}
	screen := xproto.Setup(d.x).DefaultScreen(d.x)
	if err := xproto.CreateWindowChecked(d.x, screen.RootDepth, win, d.root,
		int16(m.X), int16(m.Y), uint16(m.W), uint16(m.H), 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixel|xproto.CwEventMask, []uint32{screen.BlackPixel, xproto.EventMaskPointerMotion}).Check(); err != nil {
		t.Fatal(err)
	}
	if err := xproto.MapWindowChecked(d.x, win).Check(); err != nil {
		t.Fatal(err)
	}
	if err := xproto.ConfigureWindowChecked(d.x, win, xproto.ConfigWindowStackMode,
		[]uint32{xproto.StackModeAbove}).Check(); err != nil {
		t.Fatal(err)
	}
	d.summon(m, healInterval+revealWithin)
}

// TestE2EWithTile runs tile beside the dock, as the real autostart does,
// and opens and closes windows on both monitors -- each change sends tile
// focusing every window in turn and snapping it to the bottom edge -- and
// checks between each that the dock still comes up everywhere.
func TestE2EWithTile(t *testing.T) {
	d := startDesk(t, true)
	const rounds = 4
	closers := make([]func(), 0, rounds*len(d.mons))
	for round := range rounds {
		for _, m := range d.mons {
			closers = append(closers, d.window(m))
		}
		for _, m := range d.mons {
			d.until(startWithin, fmt.Sprintf("round %d: tile to lay out %v", round, m), func() bool { return d.tiledOn(m) })
		}
		for _, m := range d.mons {
			d.summon(m, revealWithin)
		}
	}
	// Summoning straight after a close is summoning while tile re-lays out
	// what is left: the moment it is busy focusing windows.
	for _, closeWin := range closers {
		closeWin()
		for _, m := range d.mons {
			d.summon(m, revealWithin)
		}
	}
}
