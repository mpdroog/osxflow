// Package xwin is the X11 half of the launcher: enumerating the windows
// that exist and asking the window manager to focus one.
//
// Everything display-server-specific lives behind Server, for the same
// reason gokeyd puts focus detection behind an interface: it is the part
// that cannot be unit tested, so it is the part worth keeping small and
// swappable. A Wayland implementation would satisfy the same interface
// (badly -- there is no portable way to activate another client's window
// there -- but the seam is in the right place).
package xwin

import "fmt"

// Window is a top-level window as the launcher cares about it.
type Window struct {
	// Instance and Class are the two halves of WM_CLASS. Which half is the
	// recognisable one varies: for Ghostty the instance is "ghostty" and
	// the class is "com.mitchellh.ghostty"; for Firefox the instance is
	// "Navigator" and the class is "firefox". Both are kept, and matching
	// tries both.
	Instance string
	Class    string

	// Title is _NET_WM_NAME, used only for display.
	Title string

	// ID is the X window id, opaque outside this package.
	ID uint32

	// PID is _NET_WM_PID, or 0 when the window does not advertise one.
	// Every window on this machine does; remote X clients are the case
	// that does not, and they are correctly unmatchable.
	PID uint32

	// Type is the last atom of _NET_WM_WINDOW_TYPE with the
	// "_NET_WM_WINDOW_TYPE_" prefix removed ("NORMAL", "DOCK", "DESKTOP"),
	// or "" when the window declares none.
	//
	// It is what separates the windows a user thinks of as open programs
	// from the furniture of the desktop itself. Without it a dock lists
	// the panel and the wallpaper handler alongside the browser, because
	// those really are processes with windows and matching .desktop
	// entries -- xfce4-panel's window resolves to panel-preferences.desktop
	// and appears as an application called "Panel".
	Type string

	// SkipTaskbar is _NET_WM_STATE_SKIP_TASKBAR: the window asking not to
	// be listed. A dock is a taskbar for this purpose and should honour it.
	SkipTaskbar bool
}

// Listable reports whether a window is one a dock or taskbar should show.
//
// Anything that declares a type other than NORMAL or DIALOG is desktop
// furniture: DOCK is a panel, DESKTOP is the wallpaper, and SPLASH,
// TOOLBAR, MENU and the rest are transient parts of some other window. A
// window with no declared type at all is treated as normal, which is what
// the specification says and what older applications rely on.
func (w *Window) Listable() bool {
	if w.SkipTaskbar {
		return false
	}
	switch w.Type {
	case "", "NORMAL", "DIALOG":
		return true
	}
	return false
}

func (w *Window) String() string {
	return fmt.Sprintf("0x%x pid=%d %s.%s %q", w.ID, w.PID, w.Instance, w.Class, w.Title)
}

// Server is the set of window-manager operations the launcher needs.
type Server interface {
	// Windows lists the managed top-level windows, ordered bottom-to-top
	// in the stacking order, so the last match is the most recently
	// raised one.
	Windows() ([]Window, error)

	// Activate focuses a window, raising it and switching workspace if
	// required. It is the window manager that does all of that; we only
	// ask.
	Activate(id uint32) error

	// Close releases the display connection.
	Close() error
}

// Fake is a Server for tests. It records what it was asked to activate,
// which is the assertion most tests actually want to make.
type Fake struct {
	// Err, when set, is returned by Windows.
	Err error

	// ActivateErr, when set, is returned by Activate.
	ActivateErr error

	List []Window

	// Activated is every id passed to Activate, in order.
	Activated []uint32

	Closed bool
}

func (f *Fake) Windows() ([]Window, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.List, nil
}

func (f *Fake) Activate(id uint32) error {
	if f.ActivateErr != nil {
		return f.ActivateErr
	}
	f.Activated = append(f.Activated, id)
	return nil
}

func (f *Fake) Close() error {
	f.Closed = true
	return nil
}
