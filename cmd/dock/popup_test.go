package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mpdroog/osxflow/internal/dock"
)

// TestOpenStackFailureLeavesNoClosedPopup guards a crash. Opening a stack
// closes the one already open first; if the new one then failed, d.popup
// used to go on pointing at the closed one -- no surface, no window -- and
// the next pointer motion painted into it.
func TestOpenStackFailureLeavesNoClosedPopup(t *testing.T) {
	d := &dockApp{
		// NotAStack makes stackRows fail, which is the failure reachable
		// without an X server.
		stacks: map[int]dock.Stack{0: {Kind: dock.NotAStack}},
		// A popup holding nothing on the server, so closing it sends no
		// requests and needs no connection.
		popup: &popup{hover: -1},
	}
	if err := d.openStack(0); err == nil {
		t.Fatal("openStack succeeded on an item that is not a stack")
	}
	if d.popup != nil {
		t.Errorf("d.popup = %+v after a failed open; want nil", d.popup)
	}
}

func TestStackRows(t *testing.T) {
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "report.pdf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A trash whose files directory is a regular file: listing it fails
	// with something other than "does not exist".
	badTrash := t.TempDir()
	if err := os.WriteFile(filepath.Join(badTrash, "files"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Likewise a Downloads "folder" that is a regular file.
	badDownloads := filepath.Join(t.TempDir(), "Downloads")
	if err := os.WriteFile(badDownloads, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		st    dock.Stack
		first string
		dim   bool
	}{
		{"files", dock.Stack{Kind: dock.StackDownloads, Dir: full}, "report.pdf", false},
		{"empty", dock.Stack{Kind: dock.StackDownloads, Dir: t.TempDir()}, "Empty", true},
		// A folder that cannot be read is not an empty one, and must not
		// say it is.
		{"unreadable", dock.Stack{Kind: dock.StackDownloads, Dir: badDownloads}, "Can't read folder", true},
		// A Downloads folder that does not exist yet is empty, like a trash
		// that was never used -- not unreadable.
		{"no downloads yet", dock.Stack{Kind: dock.StackDownloads, Dir: filepath.Join(t.TempDir(), "gone")},
			"Empty", true},
		{"unreadable trash", dock.Stack{Kind: dock.StackTrash, Dir: badTrash}, "Can't read folder", true},
		// A trash that was never created is simply empty.
		{"no trash yet", dock.Stack{Kind: dock.StackTrash, Dir: filepath.Join(t.TempDir(), "never")},
			"Empty", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := stackRows(tc.st)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) < 2 {
				t.Fatalf("rows = %+v; want at least a first row and the action", rows)
			}
			if rows[0].label != tc.first || rows[0].dim != tc.dim {
				t.Errorf("first row = %+v; want label %q dim %v", rows[0], tc.first, tc.dim)
			}
			if last := rows[len(rows)-1]; !last.action || last.target == "" {
				t.Errorf("last row = %+v; want the action that opens the folder", last)
			}
		})
	}
}
