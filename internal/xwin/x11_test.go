package xwin

import (
	"os"
	"testing"
)

// TestX11Integration exercises the real display when there is one. It is
// skipped rather than failed without DISPLAY so that the suite still runs
// on a build machine, but it is the only test that can catch a change in
// the xgbutil calls actually working.
func TestX11Integration(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no DISPLAY; skipping the X11 integration test")
	}
	server, err := NewX11()
	if err != nil {
		t.Skipf("cannot connect to X: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})

	wins, err := server.Windows()
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	// A running desktop session always has something mapped -- a panel, a
	// desktop window -- so an empty list means the property read is broken
	// rather than that the user closed everything.
	if len(wins) == 0 {
		t.Fatal("no windows found; _NET_CLIENT_LIST is empty on a live session")
	}

	withPID, withClass := 0, 0
	for _, w := range wins {
		if w.ID == 0 {
			t.Error("window with a zero id")
		}
		if w.PID != 0 {
			withPID++
		}
		if w.Instance != "" || w.Class != "" {
			withClass++
		}
	}
	// Not every client sets these, but a session where none does would
	// mean the matcher has nothing to work with, and that is worth
	// knowing.
	if withPID == 0 {
		t.Error("no window reported _NET_WM_PID")
	}
	if withClass == 0 {
		t.Error("no window reported WM_CLASS")
	}
	t.Logf("%d windows, %d with a pid, %d with a class", len(wins), withPID, withClass)
}

func TestX11CloseTwice(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no DISPLAY")
	}
	server, err := NewX11()
	if err != nil {
		t.Skipf("cannot connect to X: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Closing twice must report the mistake rather than panic on a nil
	// connection.
	if err := server.Close(); err == nil {
		t.Error("second Close succeeded, want an error")
	}
}
