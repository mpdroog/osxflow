// Package xmon answers "which screen is the user looking at", which X
// itself will not tell you.
//
// The X screen is the union of every monitor, so its width and height are
// the bounding box of the whole desktop and its centre is usually the gap
// between two of them. Anything that positions itself from
// screen.WidthInPixels is therefore correct only on a single-head machine.
// Two osxflow tools got this wrong in the same way: the launcher opened
// centred on the seam between the two screens, half on each, and the dock
// put its reveal trigger along the bottom of the union -- which on this
// Mac Pro is a line the pointer cannot reach at all on the shorter of the
// two monitors, so the dock could never be summoned there.
//
// RandR 1.5's GetMonitors is used rather than walking CRTCs because it
// reports exactly the logical screens the user sees, already merged. When
// RandR is missing or too old the whole screen is returned, which is both
// the old behaviour and correct for one monitor.
package xmon

import (
	"fmt"
	"log"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
)

// Rect is a monitor's place in the X screen's coordinate space.
type Rect struct {
	X, Y, W, H int

	// Name is the connector the monitor is plugged into, "DP-8". It is
	// what identifies the same monitor to something that is not looking at
	// X -- a Wayland compositor calls it by that name too -- and it is
	// empty when RandR could not be asked.
	Name string
}

// Bottom is the y coordinate just past the last visible row, which is
// where a dock hides itself.
func (r Rect) Bottom() int { return r.Y + r.H }

// Contains reports whether a point in screen coordinates is on this
// monitor.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// All lists the active monitors. The result always has at least one entry:
// the whole screen, when RandR cannot answer.
func All(conn *xgb.Conn, screen *xproto.ScreenInfo) []Rect {
	whole := []Rect{{X: 0, Y: 0, W: int(screen.WidthInPixels), H: int(screen.HeightInPixels)}}

	if err := randr.Init(conn); err != nil {
		log.Printf("RandR unavailable, treating the screen as one monitor: %v", err)
		return whole
	}
	reply, err := randr.GetMonitors(conn, screen.Root, true).Reply()
	if err != nil {
		log.Printf("listing monitors, treating the screen as one: %v", err)
		return whole
	}
	if reply == nil || len(reply.Monitors) == 0 {
		return whole
	}
	var out []Rect
	for _, m := range reply.Monitors {
		if m.Width == 0 || m.Height == 0 {
			continue
		}
		r := Rect{X: int(m.X), Y: int(m.Y), W: int(m.Width), H: int(m.Height)}
		// The name is what lets a caller say "that monitor" across display
		// servers, and it costs one round trip per monitor.
		r.Name = atomName(conn, m.Name)
		if m.Primary {
			out = append([]Rect{r}, out...)
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return whole
	}
	return out
}

// atomName resolves a monitor's name atom.
//
// A server that will not answer leaves the monitor nameless. That is
// logged and not returned: the rectangle is right either way, and the name
// only matters to a caller matching this monitor against another display
// server's idea of the same one.
func atomName(conn *xgb.Conn, atom xproto.Atom) string {
	reply, err := xproto.GetAtomName(conn, atom).Reply()
	if err != nil {
		log.Printf("naming monitor atom %d: %v", atom, err)
		return ""
	}
	if reply == nil {
		return ""
	}
	return reply.Name
}

// Active returns the monitor the user is most likely looking at: the one
// under the pointer, else the primary, else the first.
//
// The pointer is the best evidence a plain X connection has. It is a poor
// one for anything the keyboard summons, which is why For exists; prefer
// that where the caller can find out where the keyboard is.
func Active(conn *xgb.Conn, screen *xproto.ScreenInfo) Rect {
	return For(conn, screen, "")
}

// For returns the monitor to put a window on, given the connector name of
// the monitor the user is working on ("DP-8"). An empty or unknown name
// falls back to the pointer, then the primary, then the first.
//
// The name is asked for rather than worked out here because only the
// caller can know it. "Where is the user working" is answered by whichever
// window has the keyboard, and finding that out means talking to the
// window manager or the compositor -- neither of which this package does,
// and under XWayland the X pointer position is not even live: it stops
// being updated the moment the cursor leaves an X11 surface, so a launcher
// that trusts it opens on whichever screen the mouse last crossed one of
// its own windows on.
func For(conn *xgb.Conn, screen *xproto.ScreenInfo, name string) Rect {
	mons := All(conn, screen)
	if m, ok := Named(mons, name); ok {
		return m
	}
	x, y, err := Pointer(conn, screen.Root)
	if err != nil {
		log.Printf("finding the monitor under the pointer: %v", err)
		return mons[0]
	}
	if m, ok := Containing(mons, x, y); ok {
		return m
	}
	return mons[0]
}

// Named finds a monitor by connector name. An empty name matches nothing,
// so a caller that does not know where the user is working can pass what
// it has.
func Named(mons []Rect, name string) (Rect, bool) {
	if name == "" {
		return Rect{}, false
	}
	for _, m := range mons {
		if m.Name == name {
			return m, true
		}
	}
	return Rect{}, false
}

// Containing finds the monitor a point is on.
func Containing(mons []Rect, x, y int) (Rect, bool) {
	for _, m := range mons {
		if m.Contains(x, y) {
			return m, true
		}
	}
	return Rect{}, false
}

// Pointer reports where the pointer is, in screen coordinates.
func Pointer(conn *xgb.Conn, root xproto.Window) (x, y int, err error) {
	p, err := xproto.QueryPointer(conn, root).Reply()
	if err != nil {
		return 0, 0, fmt.Errorf("querying the pointer: %w", err)
	}
	if p == nil {
		return 0, 0, fmt.Errorf("querying the pointer: no reply")
	}
	return int(p.RootX), int(p.RootY), nil
}
