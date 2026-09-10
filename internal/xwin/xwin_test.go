package xwin

import (
	"errors"
	"strings"
	"testing"
)

func TestFakeRecordsActivations(t *testing.T) {
	f := &Fake{List: []Window{{ID: 1}, {ID: 2}}}

	wins, err := f.Windows()
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if len(wins) != 2 {
		t.Fatalf("got %d windows, want 2", len(wins))
	}
	for _, id := range []uint32{2, 1, 2} {
		if err := f.Activate(id); err != nil {
			t.Fatalf("Activate(%d): %v", id, err)
		}
	}
	want := []uint32{2, 1, 2}
	if len(f.Activated) != len(want) {
		t.Fatalf("Activated = %v, want %v", f.Activated, want)
	}
	for i := range want {
		if f.Activated[i] != want[i] {
			t.Fatalf("Activated = %v, want %v (order matters)", f.Activated, want)
		}
	}
}

func TestFakeErrors(t *testing.T) {
	sentinel := errors.New("no display")
	f := &Fake{Err: sentinel}
	if _, err := f.Windows(); !errors.Is(err, sentinel) {
		t.Errorf("Windows error = %v, want the configured one", err)
	}

	f = &Fake{ActivateErr: sentinel}
	if err := f.Activate(1); !errors.Is(err, sentinel) {
		t.Errorf("Activate error = %v, want the configured one", err)
	}
	// A failed Activate must not be recorded as having happened.
	if len(f.Activated) != 0 {
		t.Errorf("Activated = %v after a failure, want empty", f.Activated)
	}
}

func TestFakeClose(t *testing.T) {
	f := &Fake{}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !f.Closed {
		t.Error("Closed = false after Close")
	}
}

// Fake must satisfy Server, which is the only reason it exists.
var _ Server = (*Fake)(nil)

func TestWindowString(t *testing.T) {
	w := Window{ID: 0x4a00004, PID: 5045, Instance: "ghostty", Class: "com.mitchellh.ghostty", Title: "a title"}
	got := w.String()
	for _, want := range []string{"0x4a00004", "5045", "ghostty", "com.mitchellh.ghostty", "a title"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}
