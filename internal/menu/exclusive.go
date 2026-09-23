package menu

// One menu at a time, across processes.
//
// Each tray menu is its own program, and on X11 nothing had to coordinate
// them: the open menu holds a pointer grab, so a click on another icon is
// delivered to the menu that holds it, which closes itself before the
// other one opens. The grab is the mutual exclusion.
//
// Under Wayland that is gone. The tray is waybar, a native Wayland
// surface, so a click on it never reaches the X server and the open menu
// never hears about it. Two consequences, both of which look like bugs in
// the menus rather than in the arrangement:
//
//   - Every menu can be open at once, overlapping each other.
//   - Opening a second menu takes about a second. The first still holds
//     the pointer and keyboard, so the second's retryGrab spins
//     grabAttempts times at grabRetry each -- twice, once per grab -- and
//     a user who clicks again in that second toggles the menu they were
//     waiting for straight back off. It reads as clicks being ignored.
//
// So the menus say so explicitly. Opening one emits a signal; every other
// menu in the session closes. The signal goes out before the new menu
// grabs anything, so the old one is releasing its grabs while the new one
// is still retrying, and the retry absorbs the race.

import (
	"github.com/godbus/dbus/v5"
)

const (
	exclusiveIface  = "com.mpdroog.osxflow.Menu"
	exclusivePath   = "/com/mpdroog/osxflow/Menu"
	exclusiveMember = "Opened"
)

// Exclusive makes this host close its menu whenever another osxflow menu
// opens, and announce its own.
//
// The returned channel carries one token per request to close. It is
// deliberately not acted on here: closing touches X and the popup, which
// belong to the caller's event loop, and a D-Bus signal arrives on another
// goroutine. Read it in the main select and call Close.
//
// Failure is not fatal to a menu -- it only means the old behaviour, where
// several can be open at once -- so callers log it and carry on.
func (h *Host) Exclusive(conn *dbus.Conn) (<-chan struct{}, error) {
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(exclusiveIface),
		dbus.WithMatchMember(exclusiveMember),
	); err != nil {
		return nil, err
	}
	h.bus = conn

	sig := make(chan *dbus.Signal, 16)
	conn.Signal(sig)

	out := make(chan struct{}, 1)
	go func() {
		want := exclusiveIface + "." + exclusiveMember
		for s := range sig {
			if s.Name != want || h.isSelf(s.Sender) {
				continue
			}
			// One pending close is as good as five: the loop closes
			// whatever is open when it gets there.
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, nil
}

// isSelf reports whether a signal came from this process, which must not
// close its own menu the moment it opens it.
func (h *Host) isSelf(sender string) bool {
	if h.bus == nil {
		return false
	}
	for _, n := range h.bus.Names() {
		if n == sender {
			return true
		}
	}
	return false
}

// announce tells the other menus that this one is opening. Emitted before
// the new menu takes its grabs, so the old one has the whole of the new
// one's grab retry in which to let go.
func (h *Host) announce() {
	if h.bus == nil {
		return
	}
	// A failed emit costs mutual exclusion, not the menu, and the session
	// bus going away is reported by everything else here already.
	_ = AnnounceOpened(h.bus)
}

// AnnounceOpened asks every osxflow menu in the session to close.
//
// Host.Open sends this for itself, and for menus that is the whole story.
// It is exported for the tools that are not menus but still need the X
// keyboard or pointer, which an open menu holds and which nothing else
// can prise loose: on X11 the click that dismissed a menu always reached
// it, and under Wayland it does not, because the tray is waybar and a
// click on a native Wayland surface never reaches the X server at all.
//
// The launcher is the case that matters. Its grab fails with
// AlreadyGrabbed while a menu is open, and it has no other way to ask.
func AnnounceOpened(conn *dbus.Conn) error {
	return conn.Emit(dbus.ObjectPath(exclusivePath), exclusiveIface+"."+exclusiveMember)
}
