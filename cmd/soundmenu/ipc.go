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
	"fmt"
	"log"

	"github.com/godbus/dbus/v5"
)

const (
	ipcBusName = "com.mpdroog.osxflow.SoundMenu"
	ipcPath    = "/com/mpdroog/osxflow/SoundMenu"
	ipcIface   = "com.mpdroog.osxflow.SoundMenu"
)

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
//
// Losing the name is not fatal: a second soundmenu is a mistake rather
// than a disaster, and the one already running keeps the keys.
func (a *app) exportIPC() error {
	if err := a.session.Export(ipc{keys: a.keyC}, dbus.ObjectPath(ipcPath), ipcIface); err != nil {
		return fmt.Errorf("exporting %s: %w", ipcIface, err)
	}
	reply, err := a.session.RequestName(ipcBusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("claiming %s: %w", ipcBusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned; another soundmenu is running", ipcBusName)
	}
	return nil
}

// sendKey is client mode: hand one key to the running instance and exit.
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

	obj := conn.Object(ipcBusName, dbus.ObjectPath(ipcPath))
	if err := obj.Call(ipcIface+".Key", 0, action).Store(); err != nil {
		return fmt.Errorf("sending %q to the running soundmenu: %w", action, err)
	}
	return nil
}
