// Command tile keeps every window on screen side by side: each monitor's
// windows in a grid that fills it, without overlap, laid out again
// whenever a window opens, closes or moves to another monitor.
//
// It is switched on and off from the Tux menu, and remembers which across
// logins:
//
//	tile -enable    remember on, and run
//	tile -disable   remember off, and stop the one running
//	tile -session   run if remembered on (for labwc's autostart)
//
// Only labwc is supported: see layout.go for how it places windows it has
// no way to move.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/wl"
)

const (
	busName = "org.osxflow.Tile"
	busPath = dbus.ObjectPath("/org/osxflow/Tile")
)

// settle is how long tile waits after a window changes before laying out:
// a new window is announced before it is on a monitor, and a closing app
// often closes several at once.
const settle = 200 * time.Millisecond

func main() {
	log.SetPrefix("tile: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

func logf(format string, args ...any) { log.Printf(format, args...) }

func start() error {
	enable := flag.Bool("enable", false, "remember tiling as on, and tile")
	disable := flag.Bool("disable", false, "remember tiling as off, and stop tiling")
	session := flag.Bool("session", false, "tile only if it was left on")
	flag.Parse()

	state, err := statePath()
	if err != nil {
		return err
	}
	switch {
	case *disable:
		if err := os.Remove(state); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("forgetting that tiling is on: %w", err)
		}
		return stopRunning()
	case *enable:
		if err := os.MkdirAll(filepath.Dir(state), 0o750); err != nil {
			return fmt.Errorf("remembering that tiling is on: %w", err)
		}
		if err := os.WriteFile(state, nil, 0o600); err != nil {
			return fmt.Errorf("remembering that tiling is on: %w", err)
		}
	case *session:
		if _, err := os.Stat(state); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return run()
}

// statePath is the file whose presence means tiling is on.
func statePath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the state directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "osxflow", "tile"), nil
}

// stopRunning asks the running tile to stop, if there is one.
func stopRunning() error {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	defer closeBus(bus)
	call := bus.Object(busName, busPath).Call(busName+".Stop", 0)
	var dbusErr dbus.Error
	if errors.As(call.Err, &dbusErr) && dbusErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
		return nil
	}
	return call.Err
}

func closeBus(bus *dbus.Conn) {
	if err := bus.Close(); err != nil {
		log.Printf("closing the session bus: %v", err)
	}
}

// stopper carries the one D-Bus method, separately from the tiler because
// godbus exports every exported method of what it is given.
type stopper struct{ req chan struct{} }

// Stop implements org.osxflow.Tile.Stop.
//
//nolint:unparam // godbus exports only methods whose last result is *dbus.Error, even one that never fails
func (s *stopper) Stop() *dbus.Error {
	select {
	case s.req <- struct{}{}:
	default:
	}
	return nil
}

func run() error {
	if os.Getenv("LABWC_PID") == "" {
		return errors.New("tiling needs labwc, and LABWC_PID is not set")
	}
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	defer closeBus(bus)
	stop := &stopper{req: make(chan struct{}, 1)}
	if err = bus.Export(stop, busPath, busName); err != nil {
		return fmt.Errorf("exporting %s: %w", busName, err)
	}
	reply, err := bus.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Print("already running")
		return nil
	}

	if blockErr := installBlock(); blockErr != nil {
		return blockErr
	}
	conn, err := wl.Dial()
	if err != nil {
		return err
	}
	defer func() {
		if discErr := conn.Disconnect(); discErr != nil {
			log.Printf("disconnecting: %v", discErr)
		}
	}()
	kb, err := conn.VirtualKeyboard(keymap())
	if err != nil {
		return err
	}
	t := &tiler{conn: conn, kb: kb, bindings: bindings()}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	// labwc rereads rc.xml on a signal and in its own time; the first
	// layout waits for that like any other.
	timer := time.NewTimer(settle)
	alive := time.NewTicker(5 * time.Second)
	defer alive.Stop()
	log.Print("tiling")
	for {
		select {
		case ev := <-conn.Events():
			switch ev.(type) {
			case wl.Windows, wl.Monitor:
				timer.Reset(settle)
			}
		case <-timer.C:
			if err := t.tile(); err != nil {
				return err
			}
		case <-alive.C:
			if err := conn.Err(); err != nil {
				return err
			}
		case <-stop.req:
			log.Print("stopped")
			return nil
		case s := <-sigs:
			log.Printf("stopped by %v", s)
			return nil
		}
	}
}

type tiler struct {
	conn     *wl.Conn
	kb       *wl.Keyboard
	bindings [][]binding
	last     string
}

// tile lays the windows out again, if the set of them has changed.
func (t *tiler) tile() error {
	ts, err := t.conn.Toplevels()
	if err != nil {
		return err
	}
	p := plan(ts)
	sig := signature(p)
	if sig == t.last {
		return nil
	}
	t.last = sig

	active, hadActive := t.conn.Active()
	for _, ids := range p {
		n := min(len(ids), maxTiled)
		for i, id := range ids[:n] {
			b := t.bindings[n][i]
			// A window that closed in between is not an error: the next
			// layout, which its closing has already asked for, leaves it out.
			if err := t.conn.Activate(id); err != nil {
				log.Printf("focusing 0x%x: %v", id, err)
				continue
			}
			if err := t.kb.Chord(b.mods, uint32(keycode(b.key)-8)); err != nil { //nolint:gosec // keycode is 200 to 222
				return err
			}
		}
	}
	if hadActive {
		if err := t.conn.Activate(active.ID); err != nil {
			log.Printf("giving focus back to 0x%x: %v", active.ID, err)
		}
	}
	return nil
}
