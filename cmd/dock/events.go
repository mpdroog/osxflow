package main

// Turning X events into dock state.

import (
	"fmt"
	"os"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/dock"
)

// handle applies one event.
//
// It reports whether the dock should stay up (show) or start counting down
// to hiding (leaving); the caller owns the timer, because the decision to
// hide is about time passing rather than about any single event.
func (d *dockApp) handle(ev xgb.Event) (show, leaving bool, err error) {
	switch e := ev.(type) {
	case xproto.EnterNotifyEvent:
		return d.enter(e)

	case xproto.MotionNotifyEvent:
		if d.popup != nil {
			return d.popup.motion(d, int(e.EventX), int(e.EventY))
		}
		return d.pointerAt(float64(e.EventX), float64(e.EventY), float64(e.RootY))

	case xproto.LeaveNotifyEvent:
		if d.popup != nil {
			return false, false, nil
		}
		// The pointer left the trigger without ever reaching the dock,
		// which is what happens when the dock is revealed from a point off
		// to one side of it: the pointer sits on the strip and on nothing
		// else, so this is the only word the dock gets that it has gone.
		if e.Event == d.trigger {
			return false, true, nil
		}
		if e.Event != d.win {
			return false, false, nil
		}
		// The pointer left the window entirely. Anything still hovered is
		// no longer under the cursor.
		if d.hover != -1 || d.inside {
			d.hover, d.inside = -1, false
			if err := d.paint(); err != nil {
				return false, false, err
			}
		}
		return false, true, nil

	case xproto.ButtonPressEvent:
		return d.click(e)

	case xproto.ExposeEvent:
		// Only the last expose of a burst matters; the earlier ones
		// describe rectangles of a frame about to be repainted whole.
		if e.Count != 0 {
			return false, false, nil
		}
		if d.popup != nil && e.Window == d.popup.win {
			return false, false, d.popup.surf.Copy()
		}
		if e.Window == d.win {
			return false, false, d.surf.Copy()
		}
		return false, false, nil

	case xproto.VisibilityNotifyEvent:
		// Nothing keeps an override-redirect window on top, so when
		// something covers the dock it puts itself back. Only while it is
		// actually up: raising a hidden dock would fight with whatever the
		// user is doing for no visible benefit.
		if e.Window == d.win && e.State != xproto.VisibilityUnobscured && !d.reveal.Hidden() {
			d.raise(d.win)
		}
		return false, false, nil

	case xproto.PropertyNotifyEvent:
		if e.Window != d.screen.Root || e.Atom != d.clientList {
			return false, false, nil
		}
		return false, false, d.windowsChanged()
	}
	return false, false, nil
}

// enter handles the pointer arriving at the bottom edge or on the dock.
func (d *dockApp) enter(e xproto.EnterNotifyEvent) (show, leaving bool, err error) {
	switch e.Event {
	case d.trigger:
		// The bottom edge of the screen: bring the dock up. Nothing else
		// belongs on this path -- it is the whole of the latency the user
		// feels, and the first frame of the slide is one tick away.
		d.reveal.Show()
		d.zoom.Target = 1
		d.raise(d.win)

		// The row is already current: the root window's _NET_CLIENT_LIST
		// says the moment it changes, which is a cheaper way to know than
		// asking X about every window in the millisecond the user is
		// waiting. All that is owed here is the repaint that was skipped
		// while the dock was off screen -- and only when the row actually
		// changed while it was down there.
		switch {
		case d.poll:
			if refreshErr := d.windowsChanged(); refreshErr != nil {
				return true, false, refreshErr
			}
		case d.stale:
			if redrawErr := d.redraw(); redrawErr != nil {
				return true, false, redrawErr
			}
		}
		return true, false, nil

	case d.win:
		// The dock slid up under a pointer that never moved, so this is
		// the first indication of where the cursor actually is.
		show, _, err = d.pointerAt(float64(e.EventX), float64(e.EventY), float64(e.RootY))
		return show || true, false, err
	}
	return false, false, nil
}

// pointerAt updates the magnification and the hover for a new pointer
// position, and repaints if anything changed.
//
// x and y are in window coordinates and rootY is the pointer's distance
// down the screen, which is not the same question at all while the dock is
// in motion.
func (d *dockApp) pointerAt(x, y, rootY float64) (show, leaving bool, err error) {
	// The row may have changed while the dock was down. The trigger catches
	// up on that before revealing, but the pointer can reach the dock's own
	// window without passing through the trigger, and hit-testing the new
	// items against the last frame's placements indexes past the end.
	if d.stale {
		if err := d.redraw(); err != nil {
			return false, false, err
		}
	}
	d.cursorX = x
	inside := d.overPanel(x, y) || d.atBottomEdge(rootY)
	hover := dock.Hit(d.items, d.places, d.th.baselineY(), x, y)
	if hover >= 0 {
		inside = true
	}

	changed := hover != d.hover || inside != d.inside
	d.hover, d.inside = hover, inside

	if inside {
		d.reveal.Show()
		d.zoom.Target = 1
	}
	// The layout follows the cursor on every motion event, so a repaint is
	// nearly always warranted; X rate-limits motion to what the pointer
	// actually does, which is what keeps this from being a busy loop.
	if err := d.paint(); err != nil {
		return false, false, err
	}
	_ = changed
	if inside {
		return true, false, nil
	}
	return false, true, nil
}

// overPanel reports whether a point is on the dock itself, as opposed to
// somewhere else in the window, which is much larger than the dock and
// mostly transparent.
//
// It reaches to the bottom of the window rather than to the bottom of the
// plate. The air the plate floats above is part of the dock as far as the
// pointer is concerned, and calling it "outside" put a band of it directly
// under the dock that dismissed it: the pointer would slip below the
// plate, the dock would slide away, and it was too high up for the reveal
// trigger to catch it on the way back.
func (d *dockApp) overPanel(x, y float64) bool {
	width := dock.Width(d.items, d.places, &d.th.m)
	centre := float64(d.winW) / 2
	if x < centre-width/2 || x >= centre+width/2 {
		return false
	}
	return y >= d.th.panelTop()
}

// atBottomEdge reports whether the pointer is inside the strip along the
// bottom of the screen that the reveal trigger occupies.
//
// This is asked in screen coordinates because the window's are misleading
// exactly when it matters. The dock's window is taller than the plate it
// draws and covers the trigger for the whole time it is on screen, so a
// pointer arriving at the very bottom of the screen while the dock is
// sliding away lands somewhere in the transparent air above the plate.
// Judged by window coordinates that is "not on the dock", and the answer
// was to carry on hiding -- while the trigger, buried under the dock,
// could not see the pointer either. Coming back to the bottom edge just
// after clicking an icon therefore did nothing at all until the dock had
// finished getting out of the way, and then took the delay and the slide
// again: the best part of a second to answer a gesture that normally takes
// twenty milliseconds.
func (d *dockApp) atBottomEdge(rootY float64) bool {
	return rootY >= float64(int(d.screen.HeightInPixels)-triggerH)
}

// click activates whatever is under the pointer.
func (d *dockApp) click(e xproto.ButtonPressEvent) (show, leaving bool, err error) {
	const leftButton = 1

	if d.popup != nil {
		return d.popup.click(d, e)
	}
	if e.Detail != leftButton {
		return true, false, nil
	}
	i := dock.Hit(d.items, d.places, d.th.baselineY(), float64(e.EventX), float64(e.EventY))
	if i < 0 {
		return false, true, nil
	}

	switch d.items[i].Kind {
	case dock.KindApp:
		d.activate(i)
		// The dock gets out of the way once it has done its job, the same
		// way it would if the pointer had left.
		return false, true, nil
	case dock.KindStack:
		if err := d.openStack(i); err != nil {
			fmt.Fprintln(os.Stderr, "dock:", err)
		}
		return true, false, nil
	case dock.KindSeparator:
	}
	return true, false, nil
}

// activate focuses an application's window, or starts it if it has none.
//
// This is the launcher's run-or-raise, reused wholesale: clicking a dock
// icon for something already open should bring that window forward rather
// than start a second copy, which is the behaviour that made the launcher
// worth writing in the first place.
func (d *dockApp) activate(i int) {
	app := d.index.ByDesktopID(d.items[i].DesktopID)
	if app == nil {
		return
	}
	result, err := d.opener.Open(app)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dock:", err)
		return
	}
	if d.verbose {
		if result.Focused {
			fmt.Printf("dock: focused %s (%s)\n", app.Name, result.Confidence)
		} else {
			fmt.Printf("dock: started %s\n", app.Name)
		}
	}
}

// windowsChanged rebuilds the item list after the set of open windows
// changed.
//
// The repaint it would provoke is worth avoiding twice over. Most of these
// changes leave the row identical, and the ones that do not almost always
// arrive while the dock is hidden -- the window that just opened is the
// thing the user is looking at, which is precisely when they are not
// looking at the dock. Either way a frame costs a couple of milliseconds
// for pixels nobody sees, and a burst of them lands exactly when an
// application is starting and the machine is busiest. The work is deferred
// to the next reveal instead.
func (d *dockApp) windowsChanged() error {
	if !d.refreshItems() && !d.stale {
		return nil
	}
	if d.reveal.Hidden() && d.popup == nil {
		d.stale = true
		return nil
	}
	return d.redraw()
}

// redraw fits the window to the current row and paints it.
func (d *dockApp) redraw() error {
	d.stale = false
	if err := d.resizeIfNeeded(); err != nil {
		return err
	}
	if d.hover >= len(d.items) {
		d.hover = -1
	}
	return d.paint()
}
