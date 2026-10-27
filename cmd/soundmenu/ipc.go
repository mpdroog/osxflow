package main

// Taking the volume keys from outside the process.
//
// grabKeys asks X for the XF86Audio* keys on the root window. Under a
// Wayland compositor that cannot work: the compositor owns the keyboard,
// XWayland has no global grabs to give, and the grab either fails or
// succeeds and never fires. The keys still have to do something, so the
// compositor binds them -- and if it binds them to wpctl, the volume moves
// with no overlay, which is most of what this program is for.
//
// So the compositor binds them here instead:
//
//	<keybind key="XF86AudioRaiseVolume">
//	  <action name="Execute" command="soundmenu -key raise" />
//	</keybind>
//
// That runs this binary in client mode, which calls the running instance
// over the session bus and exits. The running instance then takes exactly
// the path a real key press took -- app.pressed -- so the volume moves and
// the overlay appears, the same on either display server.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	ipcBusName = "com.mpdroog.osxflow.SoundMenu"
	ipcPath    = "/com/mpdroog/osxflow/SoundMenu"
	ipcIface   = "com.mpdroog.osxflow.SoundMenu"
)

// errNameTaken is a name the bus would not hand over: another soundmenu
// holds it and has refused to be replaced.
var errNameTaken = errors.New(ipcBusName + " is held by another soundmenu that will not give it up")

// errNameLost is the running instance being replaced by a newer one.
var errNameLost = errors.New("another soundmenu took " + ipcBusName + "; it has the volume keys now")

// keyNames maps what the compositor is told to type to what it means.
var keyNames = map[string]keyAction{
	"raise":   keyRaise,
	"lower":   keyLower,
	"mute":    keyMute,
	"micmute": keyMicMute,
}

// ipcKeyNames is the list for the usage message, in a fixed order.
var ipcKeyNames = []string{"raise", "lower", "mute", "micmute"}

// ipc is the D-Bus object the running instance exports.
type ipc struct {
	keys chan<- keyAction
}

// Key implements com.mpdroog.osxflow.SoundMenu.Key.
//
// It only hands the action to the main loop: everything that touches the
// view, the overlay or PipeWire happens there, on one goroutine, and a
// D-Bus method runs on another. A full channel means keys are arriving
// faster than they can be acted on, and dropping one is better than
// blocking the bus.
func (i ipc) Key(action string) *dbus.Error {
	act, ok := keyNames[action]
	if !ok {
		return dbus.MakeFailedError(fmt.Errorf("unknown key %q, want one of %v", action, ipcKeyNames))
	}
	select {
	case i.keys <- act:
	default:
	}
	return nil
}

// exportIPC publishes the object and claims the name.
func (a *app) exportIPC() error {
	lost, err := exportIPCOn(a.session, a.keyC)
	a.nameLost = lost
	return err
}

// exportIPCOn is exportIPC against any connection, so that it can be
// tested on a bus of its own.
//
// The name is taken from whoever holds it (NameFlagReplaceExisting) and
// left available to whoever asks next (NameFlagAllowReplacement). Asking
// politely is what killed the volume keys once already: soundmenu is
// restarted by hand whenever it is rebuilt, a polite claim against the
// copy still running is refused, and the refusal was only logged. The new
// instance then ran on with no name -- tray icon fine, menu fine, sound
// fine -- while every `soundmenu -key raise` the compositor ran got
// "The name com.mpdroog.osxflow.SoundMenu was not provided by any .service
// files" from the bus. Taking the name means the newest instance, which is
// the one the user just started, is always the one the keys reach.
//
// The returned channel is closed if some later soundmenu takes the name
// back; see run, which stops rather than linger as a copy nothing can
// reach.
func exportIPCOn(conn *dbus.Conn, keys chan<- keyAction) (<-chan struct{}, error) {
	if err := conn.Export(ipc{keys: keys}, dbus.ObjectPath(ipcPath), ipcIface); err != nil {
		return nil, fmt.Errorf("exporting %s: %w", ipcIface, err)
	}

	// Watched before the name is claimed, so a replacement arriving in
	// the gap between the two is still seen.
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameLost"),
		dbus.WithMatchArg(0, ipcBusName),
	); err != nil {
		return nil, fmt.Errorf("watching for the loss of %s: %w", ipcBusName, err)
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	// Buffered and sent on rather than closed, so that the connection
	// going away at shutdown -- which also ends the range below -- is not
	// mistaken for a replacement.
	lost := make(chan struct{}, 1)
	go func() {
		for sig := range signals {
			if nameLost(sig) {
				lost <- struct{}{}
				return
			}
		}
	}()

	reply, err := conn.RequestName(ipcBusName,
		dbus.NameFlagDoNotQueue|dbus.NameFlagReplaceExisting|dbus.NameFlagAllowReplacement)
	if err != nil {
		return nil, fmt.Errorf("claiming %s: %w", ipcBusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("%w (RequestName reply %d)", errNameTaken, reply)
	}
	return lost, nil
}

// nameLost reports whether sig says this connection just lost the IPC
// name. The channel carries everything the connection receives, so most
// of what arrives is something else.
func nameLost(sig *dbus.Signal) bool {
	if sig == nil || sig.Name != "org.freedesktop.DBus.NameLost" || len(sig.Body) < 1 {
		return false
	}
	name, ok := sig.Body[0].(string)
	return ok && name == ipcBusName
}

// sendKey is client mode: hand one key to the running instance and exit.
// keyTimeout bounds how long a key press waits for the running soundmenu.
const keyTimeout = 2 * time.Second

func sendKey(action string) error {
	if _, ok := keyNames[action]; !ok {
		return fmt.Errorf("unknown key %q, want one of %v", action, ipcKeyNames)
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Printf("closing the session bus: %v", closeErr)
		}
	}()

	err = callKey(conn, action)
	if err == nil || hasOwner(conn) {
		return err
	}
	// Nobody is listening: the running soundmenu died, or never started --
	// on 2026-10-27 it lost a race with XWayland at login, and every key
	// press for the rest of the session logged "The name ... was not
	// provided by any .service files" and did nothing. The key press is
	// the one moment anyone needs a soundmenu, so it brings one back.
	log.Printf("%v; starting soundmenu", err)
	if startErr := ensureRunning(conn); startErr != nil {
		return fmt.Errorf("%w; and could not start one: %w", err, startErr)
	}
	return callKey(conn, action)
}

// callKey makes the one call to the running instance.
func callKey(conn *dbus.Conn, action string) error {
	// A soundmenu that is stuck would otherwise hold every key press for
	// the bus's 25 seconds, and a held key starts one of these per repeat.
	ctx, cancel := context.WithTimeout(context.Background(), keyTimeout)
	defer cancel()
	obj := conn.Object(ipcBusName, dbus.ObjectPath(ipcPath))
	if err := obj.CallWithContext(ctx, ipcIface+".Key", 0, action).Store(); err != nil {
		return fmt.Errorf("sending %q to the running soundmenu: %w", action, err)
	}
	return nil
}

// startPatience bounds the wait for a started soundmenu to claim its
// name: longer than xconn's wait for a slow X server, which it may hit.
const startPatience = 30 * time.Second

// hasOwner reports whether some soundmenu holds the IPC name.
func hasOwner(conn *dbus.Conn) bool {
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, ipcBusName).Store(&has); err != nil {
		log.Printf("asking the bus who holds %s: %v", ipcBusName, err)
		return false
	}
	return has
}

// ensureRunning starts a soundmenu in the background and waits until it
// answers on the bus.
//
// A held key runs one client per repeat, all arriving at once at an empty
// bus. A lock file serialises them: the first starts the daemon and waits,
// the rest queue behind it and find it running -- rather than each starting
// its own, every one taking the name from the last (see exportIPCOn) and
// a row of tray icons blinking in and out.
func ensureRunning(conn *dbus.Conn) error {
	unlock, err := lockStart()
	if err != nil {
		return err
	}
	defer unlock()
	if hasOwner(conn) {
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	// Detached into a session of its own, so it outlives this client and
	// the compositor's Execute. Its log goes where ours does: labwc's.
	// Background, not a timeout: the daemon must outlive this call.
	cmd := exec.CommandContext(context.Background(), self) //nolint:gosec // this same binary, from its own path
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", self, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.After(startPatience)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("the started soundmenu exited: %w", err)
		case <-deadline:
			return fmt.Errorf("the started soundmenu did not claim %s within %v", ipcBusName, startPatience)
		case <-tick.C:
			if hasOwner(conn) {
				return nil
			}
		}
	}
}

// lockStart takes the start lock, waiting for whoever holds it.
func lockStart() (unlock func(), err error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, "osxflow-soundmenu.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // a fixed name in the runtime dir
	if err != nil {
		return nil, fmt.Errorf("opening the start lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("taking the start lock: %w", err), f.Close())
	}
	// Closing drops the lock.
	return func() {
		if err := f.Close(); err != nil {
			log.Printf("releasing the start lock: %v", err)
		}
	}, nil
}
