package main

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// The -windows table names the three expected reasons a window has no
// executable, and shows anything else in full.
func TestExeProblem(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{launch.ErrNoPID, "no pid"},
		{fmt.Errorf("reading /proc/1/exe: %w", fs.ErrNotExist), "process has exited"},
		{fmt.Errorf("reading /proc/1/exe: %w", fs.ErrPermission), "another user's process"},
		{errors.New("reading /proc/1/exe: input/output error"), "reading /proc/1/exe: input/output error"},
	}
	for _, tc := range tests {
		if got := exeProblem(tc.err); got != tc.want {
			t.Errorf("exeProblem(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// The real thing, for the case that needs no setup.
	if _, err := launch.ProcExe(0); exeProblem(err) != "no pid" {
		t.Errorf("exeProblem(ProcExe(0)) = %q", exeProblem(err))
	}
}

func TestFindApp(t *testing.T) {
	apps := []desktop.App{
		{Name: "Files", ID: "files.desktop"},
		{Name: "Firefox Web Browser", ID: "firefox.desktop"},
		{Name: "File Manager", ID: "thunar.desktop"},
		{Name: "Ghostty", ID: "ghostty.desktop"},
	}
	tests := []struct {
		query string
		want  string
	}{
		{"Ghostty", "Ghostty"},
		{"ghostty", "Ghostty"}, // exact match is case-insensitive
		{"Files", "Files"},     // exact beats the prefix match on "File Manager"
		{"File", "Files"},      // prefix: first in order wins
		{"fire", "Firefox Web Browser"},
		{"Browser", "Firefox Web Browser"}, // substring, no prefix matches
		{"host", "Ghostty"},                // substring only
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := findApp(apps, tc.query)
			if !ok {
				t.Fatalf("findApp(%q) found nothing, want %q", tc.query, tc.want)
			}
			if got.Name != tc.want {
				t.Errorf("findApp(%q) = %q, want %q", tc.query, got.Name, tc.want)
			}
		})
	}

	if _, ok := findApp(apps, "nothing here"); ok {
		t.Error("findApp matched a query that should not match")
	}
	if _, ok := findApp(nil, "anything"); ok {
		t.Error("findApp matched against an empty list")
	}
}

// The window the keyboard goes back to when the launcher is dismissed. The
// list is bottom-to-top, so the answer is the last entry -- but not the
// desktop furniture, which is listed there too.
func TestFocusedWindow(t *testing.T) {
	tests := []struct {
		name string
		list []xwin.Window
		want uint32
	}{
		{
			name: "topmost of several",
			list: []xwin.Window{{ID: 1}, {ID: 2}, {ID: 3}},
			want: 3,
		},
		{
			name: "the panel on top does not count",
			list: []xwin.Window{{ID: 1}, {ID: 2}, {ID: 3, Type: "DOCK"}},
			want: 2,
		},
		{
			name: "nor does the wallpaper, or anything hiding from the taskbar",
			list: []xwin.Window{{ID: 1}, {ID: 2, SkipTaskbar: true}, {ID: 3, Type: "DESKTOP"}},
			want: 1,
		},
		{
			name: "a dialog is where you were typing",
			list: []xwin.Window{{ID: 1}, {ID: 2, Type: "DIALOG"}},
			want: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := focusedWindow(&xwin.Fake{List: tc.list})
			if !ok {
				t.Fatalf("found nothing, want 0x%x", tc.want)
			}
			if got.ID != tc.want {
				t.Errorf("got 0x%x, want 0x%x", got.ID, tc.want)
			}
		})
	}

	// Nothing to hand the keyboard to, and no reason to fail over it.
	for _, tc := range []struct {
		name   string
		server *xwin.Fake
	}{
		{"an empty desktop", &xwin.Fake{}},
		{"nothing but furniture", &xwin.Fake{List: []xwin.Window{{ID: 1, Type: "DOCK"}}}},
		{"a display server that will not say", &xwin.Fake{Err: errors.New("no client list")}},
	} {
		if _, ok := focusedWindow(tc.server); ok {
			t.Errorf("%s: found a window to focus", tc.name)
		}
	}
}

// Dismissing the launcher hands the keyboard back; launching something does
// not, because the thing that was launched has it.
func TestRestoreFocusActivatesTheWindow(t *testing.T) {
	server := &xwin.Fake{}
	restoreFocus(server, &xwin.Window{ID: 42})
	if len(server.Activated) != 1 || server.Activated[0] != 42 {
		t.Errorf("activated %v, want [42]", server.Activated)
	}

	// A window that has closed in the meantime is logged, not fatal.
	failing := &xwin.Fake{ActivateErr: errors.New("no such window")}
	restoreFocus(failing, &xwin.Window{ID: 42})
}
