package rfkill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMarshalRoundTrip(t *testing.T) {
	for _, e := range []Event{
		{},
		{Index: 3, Type: TypeBluetooth, Op: OpChange, Soft: true},
		{Index: 0xdeadbeef, Type: TypeWLAN, Op: OpDel, Hard: true, Soft: true},
		{Type: TypeNFC + 7, Op: OpChangeAll}, // an unknown type is still a switch
	} {
		got, err := ParseEvent(e.Marshal())
		if err != nil {
			t.Fatalf("ParseEvent(Marshal(%+v)): %v", e, err)
		}
		if got != e {
			t.Errorf("round trip: got %+v, want %+v", got, e)
		}
	}
}

func TestParseEvent(t *testing.T) {
	bt := Event{Index: 1, Type: TypeBluetooth, Op: OpAdd, Soft: true}
	extended := append(bt.Marshal(), 0x01) // a newer kernel's hard_block_reasons
	if got, err := ParseEvent(extended); err != nil || got != bt {
		t.Errorf("extended event: %+v, %v; want %+v", got, err, bt)
	}
	if _, err := ParseEvent(bt.Marshal()[:7]); !errors.Is(err, ErrShortEvent) {
		t.Errorf("7 bytes: %v, want ErrShortEvent", err)
	}
	if _, err := ParseEvent(nil); !errors.Is(err, ErrShortEvent) {
		t.Errorf("no bytes: %v, want ErrShortEvent", err)
	}
	bad := bt.Marshal()
	bad[5] = 9
	if _, err := ParseEvent(bad); !errors.Is(err, ErrUnknownOp) {
		t.Errorf("op 9: %v, want ErrUnknownOp", err)
	}
	// Any non-zero byte is a block, as the kernel treats it.
	odd := bt.Marshal()
	odd[6], odd[7] = 2, 0xff
	if got, err := ParseEvent(odd); err != nil || !got.Soft || !got.Hard {
		t.Errorf("non-one flags: %+v, %v", got, err)
	}
}

func FuzzParseEvent(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0, 2, 0, 1, 0})
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 1, 3, 0, 0, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := ParseEvent(b)
		if len(b) < eventSize {
			if !errors.Is(err, ErrShortEvent) {
				t.Fatalf("ParseEvent(%x) = %+v, %v; want ErrShortEvent", b, e, err)
			}
			return
		}
		if err != nil {
			if !errors.Is(err, ErrUnknownOp) {
				t.Fatalf("ParseEvent(%x): unexpected error %v", b, err)
			}
			return
		}
		again, err := ParseEvent(e.Marshal())
		if err != nil || again != e {
			t.Fatalf("round trip of %+v: %+v, %v", e, again, err)
		}
		if !bytes.Equal(e.Marshal()[:6], b[:6]) {
			t.Fatalf("index, type and op changed: %x -> %x", b[:6], e.Marshal()[:6])
		}
	})
}

func TestSwitches(t *testing.T) {
	s := Switches{}
	if present, _, _ := s.State(TypeBluetooth); present {
		t.Error("an empty state has Bluetooth")
	}
	for _, e := range []Event{
		{Index: 0, Type: TypeBluetooth, Op: OpAdd, Soft: true},
		{Index: 1, Type: TypeWLAN, Op: OpAdd},
		{Index: 2, Type: TypeBluetooth, Op: OpAdd},
	} {
		s.Apply(&e)
	}
	if present, soft, hard := s.State(TypeBluetooth); !present || !soft || hard {
		t.Errorf("Bluetooth: present %t soft %t hard %t; want true true false", present, soft, hard)
	}
	s.Apply(&Event{Index: 0, Type: TypeBluetooth, Op: OpChange, Hard: true})
	if _, soft, hard := s.State(TypeBluetooth); soft || !hard {
		t.Errorf("after a change: soft %t hard %t; want false true", soft, hard)
	}
	s.Apply(&Event{Type: TypeWLAN, Op: OpChangeAll, Soft: true})
	if _, soft, _ := s.State(TypeWLAN); !soft {
		t.Error("OpChangeAll did not block Wi-Fi")
	}
	if _, soft, _ := s.State(TypeBluetooth); soft {
		t.Error("OpChangeAll for Wi-Fi blocked Bluetooth")
	}
	s.Apply(&Event{Type: TypeAll, Op: OpChangeAll, Soft: true})
	if _, soft, _ := s.State(TypeBluetooth); !soft {
		t.Error("OpChangeAll for all types did not block Bluetooth")
	}
	s.Apply(&Event{Index: 0, Op: OpDel})
	s.Apply(&Event{Index: 2, Op: OpDel})
	if present, _, _ := s.State(TypeBluetooth); present {
		t.Error("Bluetooth still present after its switches were deleted")
	}
	if present, _, _ := s.State(TypeAll); !present {
		t.Error("TypeAll does not see the remaining Wi-Fi switch")
	}
}

func TestSetBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rfkill")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetBlocked(path, TypeBluetooth, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Event{Type: TypeBluetooth, Op: OpChangeAll, Soft: true}
	if got, err := ParseEvent(b); len(b) != eventSize || err != nil || got != want {
		t.Errorf("wrote %x (%+v, %v), want %+v", b, got, err, want)
	}
	if err := SetBlocked(filepath.Join(t.TempDir(), "missing"), TypeBluetooth, false); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing device: %v, want ErrNotExist", err)
	}
}

func collect(t *testing.T, w *Watcher) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				return got
			}
			got = append(got, e)
		case <-deadline:
			t.Fatal("watcher never finished")
		}
	}
}

func writeEvents(t *testing.T, events ...Event) string {
	t.Helper()
	var b []byte
	for i := range events {
		b = append(b, events[i].Marshal()...)
	}
	path := filepath.Join(t.TempDir(), "events")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWatchFile(t *testing.T) {
	want := []Event{
		{Index: 0, Type: TypeBluetooth, Op: OpAdd, Soft: true},
		{Index: 1, Type: TypeWLAN, Op: OpAdd},
		{Index: 0, Type: TypeBluetooth, Op: OpChange},
	}
	w, err := Watch(writeEvents(t, want...))
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, w)
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if err := w.Err(); err != nil {
		t.Errorf("Err after a clean end: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// A file cut off mid-event is a failure, reported, after the whole events
// before it.
func TestWatchTruncated(t *testing.T) {
	path := writeEvents(t, Event{Index: 4, Type: TypeBluetooth, Op: OpAdd})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := f.Write([]byte{1, 2, 3}); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	w, err := Watch(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, w); len(got) != 1 {
		t.Errorf("got %d events, want 1", len(got))
	}
	if err := w.Err(); err == nil {
		t.Error("a truncated event reported no error")
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestWatchUnknownOp(t *testing.T) {
	bad := (&Event{Type: TypeBluetooth}).Marshal()
	bad[5] = 42
	path := filepath.Join(t.TempDir(), "events")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Watch(path)
	if err != nil {
		t.Fatal(err)
	}
	collect(t, w)
	if err := w.Err(); !errors.Is(err, ErrUnknownOp) {
		t.Errorf("Err = %v, want ErrUnknownOp", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// Closing a watcher blocked on a live source ends it without an error, the
// way it will be on /dev/rfkill, which never reaches end of file.
func TestWatchCloseWhileBlocked(t *testing.T) {
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := wr.Close(); err != nil {
			t.Logf("closing the pipe's write end: %v", err)
		}
	})
	w := newWatcher(r)
	e := Event{Index: 7, Type: TypeBluetooth, Op: OpChange, Soft: true}
	if _, err := wr.Write(e.Marshal()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.Events():
		if got != e {
			t.Errorf("got %+v, want %+v", got, e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event from the pipe")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	collect(t, w)
	if err := w.Err(); err != nil {
		t.Errorf("Err after Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestWatchMissing(t *testing.T) {
	if _, err := Watch(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Watch of a missing device: %v, want ErrNotExist", err)
	}
}
