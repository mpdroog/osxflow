package main

import (
	"testing"
	"time"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/xwin"
	"github.com/mpdroog/osxflow/menu"
)

// labels is the rows' labels, with separators as "--", which is the shape
// of the menu these tests are about.
func labels(rows []menu.Row) []string {
	out := make([]string, len(rows))
	for i := range rows {
		if rows[i].Kind == menu.Separator {
			out[i] = "--"
			continue
		}
		out[i] = rows[i].Label
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The menu of an application with Desktop Actions: a new window, then its
// actions in the entry's order, then the window.
func TestAppRowsWithActions(t *testing.T) {
	a := &app{
		windows:   &xwin.Fake{},
		appWindow: 0x42,
		appOwner: &desktop.App{Name: "Thunar File Manager", Argv: []string{"thunar"}, Actions: []desktop.Action{
			{ID: "open-home", Name: "Home", Argv: []string{"thunar"}},
			{ID: "open-trash", Name: "Trash", Argv: []string{"thunar", "trash:///"}},
		}},
	}
	want := []string{"New Window", "Home", "Trash", "--", "Close Window"}
	if got := labels(a.appRows()); !equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// Foot's case, and most applications': an entry with no actions still gets a
// new window and its own window, so the name on the bar is never dead to a
// click.
func TestAppRowsWithoutActions(t *testing.T) {
	a := &app{
		windows:   &xwin.Fake{},
		appWindow: 7,
		appOwner:  &desktop.App{Name: "Foot", Argv: []string{"foot"}},
	}
	want := []string{"New Window", "--", "Close Window"}
	if got := labels(a.appRows()); !equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// A window that matched no .desktop entry -- its name on the bar came from
// WM_CLASS -- can still be closed, and nothing is offered that would need
// an entry.
func TestAppRowsWithoutEntry(t *testing.T) {
	a := &app{windows: &xwin.Fake{}, appWindow: 7}
	want := []string{"Close Window"}
	if got := labels(a.appRows()); !equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// Nothing focused is no menu, which is also the state the bar shows no name
// in, so it is not reachable by a click.
func TestAppRowsNothingFocused(t *testing.T) {
	a := &app{windows: &xwin.Fake{}}
	if rows := a.appRows(); len(rows) != 0 {
		t.Errorf("rows = %v, want none", labels(rows))
	}
}

// Before xwin.Await has delivered a window server there is nothing to close,
// and asking a nil interface would panic.
func TestAppRowsBeforeWindowServer(t *testing.T) {
	a := &app{appWindow: 7, appOwner: &desktop.App{Name: "Foot", Argv: []string{"foot"}}}
	want := []string{"New Window"}
	if got := labels(a.appRows()); !equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// Close Window closes the window that was focused when the menu was built,
// not whatever is focused by the time it is clicked.
func TestAppRowsCloseClosesTheWindowItWasBuiltFor(t *testing.T) {
	fake := &xwin.Fake{}
	a := &app{windows: fake, appWindow: 0x99, errs: make(chan error, 1)}
	rows := a.appRows()
	if len(rows) != 1 || rows[0].Click == nil {
		t.Fatalf("rows = %v, want one clickable row", labels(rows))
	}
	rows[0].Click()

	// Focus moves on while the click is still in flight; the row must not
	// follow it.
	a.appWindow = 0x11

	deadline := time.Now().Add(2 * time.Second)
	for len(fake.ClosedWindows) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(fake.ClosedWindows) != 1 || fake.ClosedWindows[0] != 0x99 {
		t.Errorf("closed = %#v, want just 0x99", fake.ClosedWindows)
	}
	select {
	case err := <-a.errs:
		t.Errorf("errs got %v, want nothing", err)
	default:
	}
}
