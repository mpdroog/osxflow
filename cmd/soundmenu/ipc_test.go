package main

// The volume keys on Wayland are this file's subject. The compositor runs
// `soundmenu -key raise`, that copy calls the running one over the session
// bus, and nothing else carries the keys there. Every step below was once
// untested, and the step that broke -- claiming the bus name -- broke
// silently: the tray icon, the menu and the sound all went on working.

import (
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// settleIPC bounds a wait for something that crosses the bus.
const settleIPC = 2 * time.Second

// wantKey reads one action, and says what was expected when none arrives.
func wantKey(t *testing.T, keys <-chan keyAction, want keyAction) {
	t.Helper()
	select {
	case got := <-keys:
		if got != want {
			t.Errorf("key = %v, want %v", got, want)
		}
	case <-time.After(settleIPC):
		t.Errorf("no key arrived within %v, want %v", settleIPC, want)
	}
}

// TestKeyReachesTheRunningInstance is the whole compositor path: a second
// copy of the binary, started by the keybind, reaching the running one
// through the well-known name.
func TestKeyReachesTheRunningInstance(t *testing.T) {
	addr := dbustest.Start(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	keys := make(chan keyAction, 8)
	if _, err := exportIPCOn(dbustest.Conn(t, addr), keys); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	// sendKey is client mode exactly as the keybind runs it, session bus
	// and all -- not a hand-built call that could agree with a name the
	// running instance never claimed.
	for _, name := range ipcKeyNames {
		if err := sendKey(name); err != nil {
			t.Fatalf("sendKey(%q): %v", name, err)
		}
		wantKey(t, keys, keyNames[name])
	}
}

func TestSendKeyRejectsAnUnknownKey(t *testing.T) {
	addr := dbustest.Start(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	keys := make(chan keyAction, 8)
	if _, err := exportIPCOn(dbustest.Conn(t, addr), keys); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := sendKey("louder"); err == nil {
		t.Fatal("sendKey(\"louder\") = nil, want an error")
	}
	select {
	case got := <-keys:
		t.Errorf("an unknown key produced %v, want nothing", got)
	default:
	}
}

// TestARestartTakesTheName is the regression. soundmenu is restarted by
// hand every time it is rebuilt, so a new instance routinely starts while
// the old one is still up. It must come away owning the name: the old
// behaviour was to be refused, log it, and run on unreachable, which left
// the volume keys dead until the next reboot.
func TestARestartTakesTheName(t *testing.T) {
	addr := dbustest.Start(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	oldKeys := make(chan keyAction, 8)
	oldLost, err := exportIPCOn(dbustest.Conn(t, addr), oldKeys)
	if err != nil {
		t.Fatalf("the first instance: %v", err)
	}

	newKeys := make(chan keyAction, 8)
	if _, err := exportIPCOn(dbustest.Conn(t, addr), newKeys); err != nil {
		t.Fatalf("the second instance: %v", err)
	}

	// The keys go to the instance the user just started.
	if err := sendKey("raise"); err != nil {
		t.Fatalf("sendKey: %v", err)
	}
	wantKey(t, newKeys, keyRaise)
	select {
	case got := <-oldKeys:
		t.Errorf("the replaced instance got %v, want nothing", got)
	default:
	}

	// And the replaced one is told, so it can stop rather than sit there
	// with a tray icon nothing can reach.
	select {
	case <-oldLost:
	case <-time.After(settleIPC):
		t.Errorf("the replaced instance was not told within %v", settleIPC)
	}
}

// TestAnImmovableOwnerIsAnError covers the one case left: some other
// program holding the name and refusing to be replaced. That must not be
// swallowed -- on Wayland it means no volume keys.
func TestAnImmovableOwnerIsAnError(t *testing.T) {
	addr := dbustest.Start(t)
	squatter := dbustest.Conn(t, addr)
	// No AllowReplacement, so the claim below cannot take it.
	reply, err := squatter.RequestName(ipcBusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatalf("the squatter: %v", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("the squatter got reply %d, want primary owner", reply)
	}

	if _, err := exportIPCOn(dbustest.Conn(t, addr), make(chan keyAction, 8)); !errors.Is(err, errNameTaken) {
		t.Errorf("err = %v, want errNameTaken", err)
	}
}
