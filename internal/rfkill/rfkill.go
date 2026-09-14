// Package rfkill reads and flips the kernel's radio kill switches through
// /dev/rfkill: the switch that turns Bluetooth (or Wi-Fi) off below any
// daemon, which is what "Bluetooth off" means on this machine.
//
// logind gives the user on the active seat read and write access to
// /dev/rfkill through an ACL, so nothing here needs root.
package rfkill

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// DefaultPath is the kill switch device.
const DefaultPath = "/dev/rfkill"

// eventSize is RFKILL_EVENT_SIZE_V1: struct rfkill_event { __u32 idx;
// __u8 type; __u8 op; __u8 soft; __u8 hard; }. Newer kernels append more
// (hard_block_reasons), but they copy no more than the reader asks for, so
// reading exactly this many bytes always yields one whole v1 event.
const eventSize = 8

// Type is the kind of radio a switch controls (enum rfkill_type).
type Type uint8

// The radio types the kernel defines.
const (
	TypeAll Type = iota
	TypeWLAN
	TypeBluetooth
	TypeUWB
	TypeWiMAX
	TypeWWAN
	TypeGPS
	TypeFM
	TypeNFC
)

// Op is what an event reports or asks for (enum rfkill_operation).
type Op uint8

// The operations the kernel defines.
const (
	// OpAdd reports a switch; opening the device yields one per switch.
	OpAdd Op = iota
	// OpDel reports a switch gone.
	OpDel
	// OpChange reports one switch's new state, or, written, sets it.
	OpChange
	// OpChangeAll, written, sets every switch of a type at once.
	OpChangeAll
)

// Event is one rfkill event.
type Event struct {
	Index uint32
	Type  Type
	Op    Op

	// Soft is the software block anyone may lift; Hard is a hardware
	// switch or firmware block that software cannot.
	Soft, Hard bool
}

// ErrShortEvent means fewer bytes than an event holds.
var ErrShortEvent = errors.New("rfkill event too short")

// ErrUnknownOp means an operation this package does not know. Types are
// not checked the same way: a new kind of radio is still a switch.
var ErrUnknownOp = errors.New("unknown rfkill operation")

// ParseEvent decodes an event in the kernel's byte order. Anything past
// the first eight bytes -- a newer kernel's extended fields -- is ignored.
func ParseEvent(b []byte) (Event, error) {
	if len(b) < eventSize {
		return Event{}, fmt.Errorf("%w: %d bytes, want at least %d", ErrShortEvent, len(b), eventSize)
	}
	e := Event{
		Index: binary.NativeEndian.Uint32(b[0:4]),
		Type:  Type(b[4]),
		Op:    Op(b[5]),
		Soft:  b[6] != 0,
		Hard:  b[7] != 0,
	}
	if e.Op > OpChangeAll {
		return Event{}, fmt.Errorf("%w %d", ErrUnknownOp, e.Op)
	}
	return e, nil
}

// Marshal encodes an event as the eight bytes the kernel accepts.
func (e *Event) Marshal() []byte {
	b := make([]byte, eventSize)
	binary.NativeEndian.PutUint32(b[0:4], e.Index)
	b[4], b[5] = byte(e.Type), byte(e.Op)
	if e.Soft {
		b[6] = 1
	}
	if e.Hard {
		b[7] = 1
	}
	return b
}

// SetBlocked soft-blocks or unblocks every switch of type t. A hard block
// stays whatever this asks: that is what hard means.
func SetBlocked(path string, t Type, blocked bool) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	e := Event{Type: t, Op: OpChangeAll, Soft: blocked}
	_, writeErr := f.Write(e.Marshal())
	closeErr := f.Close()
	if writeErr != nil {
		writeErr = fmt.Errorf("writing to %s: %w", path, writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("closing %s: %w", path, closeErr)
	}
	return errors.Join(writeErr, closeErr)
}

// Switches is the state of every switch, by index, built up from events.
type Switches map[uint32]Event

// Apply folds one event into the state.
func (s Switches) Apply(e *Event) {
	switch e.Op {
	case OpAdd, OpChange:
		s[e.Index] = *e
	case OpDel:
		delete(s, e.Index)
	case OpChangeAll:
		// The kernel reports the effect as one OpChange per switch; this is
		// for a caller replaying what it wrote.
		for i, sw := range s {
			if e.Type == TypeAll || sw.Type == e.Type {
				sw.Soft = e.Soft
				s[i] = sw
			}
		}
	}
}

// State reports whether there is a switch of type t (any switch, for
// TypeAll), and whether any such switch is soft- or hard-blocked.
func (s Switches) State(t Type) (present, soft, hard bool) {
	for i := range s {
		sw := s[i]
		if t != TypeAll && sw.Type != t {
			continue
		}
		present = true
		soft = soft || sw.Soft
		hard = hard || sw.Hard
	}
	return present, soft, hard
}

// Watcher follows the kill switches.
type Watcher struct {
	r      io.ReadCloser
	events chan Event
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error

	mu  sync.Mutex
	err error
}

// Watch opens path and delivers its events: first one OpAdd per existing
// switch, then every change as it happens. Receive from Events until it is
// closed, then ask Err why.
func Watch(path string) (*Watcher, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return newWatcher(f), nil
}

func newWatcher(r io.ReadCloser) *Watcher {
	w := &Watcher{r: r, events: make(chan Event, 16), done: make(chan struct{})}
	go w.run()
	return w
}

// Events delivers events. It is closed when the watcher is closed, when
// the source ends, or when a read fails; Err tells those apart.
func (w *Watcher) Events() <-chan Event { return w.events }

// Err is why Events closed: nil after Close or a clean end of input,
// otherwise the read or decoding failure. Before Events closes it is nil.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Close stops the watcher. /dev/rfkill and pipes are pollable, so a read
// blocked on them returns as soon as the file closes. Calling Close again
// returns what the first call did.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		if err := w.r.Close(); err != nil {
			w.closeErr = fmt.Errorf("closing the rfkill device: %w", err)
		}
	})
	return w.closeErr
}

func (w *Watcher) run() {
	defer close(w.events)
	buf := make([]byte, eventSize)
	for {
		_, err := io.ReadFull(w.r, buf)
		switch {
		case errors.Is(err, io.EOF):
			// A clean end between events: only a file does that, never the
			// device, but it is not a failure.
			return
		case err != nil:
			if !w.closing() {
				w.fail(fmt.Errorf("reading rfkill events: %w", err))
			}
			return
		}
		e, err := ParseEvent(buf)
		if err != nil {
			w.fail(err)
			return
		}
		select {
		case w.events <- e:
		case <-w.done:
			return
		}
	}
}

func (w *Watcher) closing() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *Watcher) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}
