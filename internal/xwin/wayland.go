package xwin

import (
	"fmt"
	"os"

	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/wl"
)

// wayland is a Server backed by wlr-foreign-toplevel-management.
//
// It exists because under a Wayland compositor the X11 implementation
// cannot work, and not for the reason the package comment used to give.
// labwc does not publish _NET_CLIENT_LIST on the XWayland root at all --
// XWayland has no idea which Wayland-native windows exist, so the property
// is absent rather than empty, and x11.Windows fails at the first read
// with "is the window manager EWMH-compliant?". Every osxflow tool that
// draws still draws through XWayland perfectly well; it is only the
// question "what is open" that has to be asked somewhere else.
//
// The package comment also said a Wayland implementation would satisfy
// Server "badly -- there is no portable way to activate another client's
// window there". That was true of the core protocol and is not true of
// this one: zwlr_foreign_toplevel_handle_v1 has an activate request, and
// labwc advertises version 3 of it.
type wayland struct {
	c *wl.Conn
}

// NewWayland connects to the compositor.
func NewWayland() (Server, error) {
	c, err := wl.Dial()
	if err != nil {
		return nil, err
	}
	return &wayland{c: c}, nil
}

// NewServer returns the right Server for the session: Wayland when there
// is a compositor, X11 otherwise.
//
// The laptop still runs Mint and XFCE with these same binaries, so the
// X11 path is not legacy and is not going away -- which is the whole
// reason the choice is made at runtime rather than with a build tag.
func NewServer() (Server, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return NewX11()
	}
	return NewWayland()
}

// NewServerWith is NewServer for a caller that already holds an X
// connection and wants the X11 server to share it. The connection is
// ignored on Wayland, where it is not what answers the question.
func NewServerWith(x *xgbutil.XUtil) (Server, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return NewX11With(x)
	}
	return NewWayland()
}

// Windows lists the compositor's toplevels.
//
// Two fields cannot be filled in, and saying so here is more useful than
// inventing values:
//
//   - PID is always 0. wlr-foreign-toplevel carries no process id. Callers
//     that matched a window to a .desktop entry by _NET_WM_PID must fall
//     back to the app id, which on Wayland is by convention the .desktop
//     basename and so is usually the better key anyway.
//   - Type is always "", which Listable treats as NORMAL. The protocol has
//     no window-type concept: a compositor only ever reports things that
//     are already toplevels, so the desktop furniture Type existed to
//     filter out -- panels, the wallpaper handler -- never appears here.
//     That is why its absence costs nothing.
//
// Instance and Class are both the app id. Matching tries both halves of
// WM_CLASS, and Wayland has only the one string, so filling both is what
// keeps existing matching code working unchanged.
func (w *wayland) Windows() ([]Window, error) {
	tops, err := w.c.Toplevels()
	if err != nil {
		return nil, err
	}
	out := make([]Window, 0, len(tops))
	for _, t := range tops {
		out = append(out, Window{
			Instance: t.AppID,
			Class:    t.AppID,
			Title:    t.Title,
			ID:       t.ID,
		})
	}
	return out, nil
}

// Activate focuses a window. The compositor does the raising and the
// workspace switch, exactly as a window manager does under X11.
func (w *wayland) Activate(id uint32) error {
	if err := w.c.Activate(id); err != nil {
		return fmt.Errorf("activating window 0x%x: %w", id, err)
	}
	return nil
}

// ActiveMonitor names the monitor holding the focused window.
//
// The compositor is asked, not X: XWayland has no idea where a
// Wayland-native window is, or that it exists. wlr-foreign-toplevel says
// which outputs each window is on, and the output carries the same
// connector name that XWayland's RandR reports -- which is what makes the
// answer usable by a window this process places through X.
//
// An unnamed output answers "", which happens only on a compositor whose
// wl_output is older than version 4. There is a position to fall back on
// there, but no honest way to return it as a name, and the caller has its
// own fallback.
func (w *wayland) ActiveMonitor() (string, error) {
	out, ok := w.c.ActiveOutput()
	if !ok {
		return "", nil
	}
	return out.Name, nil
}

func (w *wayland) Close() error {
	return w.c.Disconnect()
}
