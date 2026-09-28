package xwin

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// logRecorder collects what Await logs; it is called from Await's own
// goroutine.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

func TestAwaitRetriesUntilOpen(t *testing.T) {
	want := &Fake{}
	var calls int
	open := func() (Server, error) {
		calls++ // only Await's goroutine touches it
		if calls < 4 {
			return nil, errors.New("no window manager")
		}
		return want, nil
	}
	var rec logRecorder
	done := make(chan struct{})
	defer close(done)

	select {
	case got := <-Await(open, time.Millisecond, rec.logf, done):
		if got != want {
			t.Errorf("got %v, want the Server open returned", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Server delivered")
	}
	// Three identical failures are one line, and the recovery another.
	if n := rec.count(); n != 2 {
		t.Errorf("logged %d lines, want 2: %q", n, rec.lines)
	}
}

func TestAwaitLogsAChangedFailure(t *testing.T) {
	errs := []error{errors.New("a"), errors.New("a"), errors.New("b")}
	var calls int
	open := func() (Server, error) {
		calls++
		if calls <= len(errs) {
			return nil, errs[calls-1]
		}
		return &Fake{}, nil
	}
	var rec logRecorder
	done := make(chan struct{})
	defer close(done)

	select {
	case <-Await(open, time.Millisecond, rec.logf, done):
	case <-time.After(5 * time.Second):
		t.Fatal("no Server delivered")
	}
	if n := rec.count(); n != 3 {
		t.Errorf("logged %d lines, want 3 (a, b, recovered): %q", n, rec.lines)
	}
}

// Closing done stops the retrying, and nothing is delivered after it.
func TestAwaitStopsOnDone(t *testing.T) {
	attempted := make(chan struct{}, 1)
	open := func() (Server, error) {
		select {
		case attempted <- struct{}{}:
		default:
		}
		return nil, errors.New("no window manager")
	}
	var rec logRecorder
	done := make(chan struct{})
	ch := Await(open, time.Hour, rec.logf, done)
	<-attempted
	close(done)
	select {
	case s := <-ch:
		t.Errorf("delivered %v after done closed", s)
	case <-time.After(50 * time.Millisecond):
	}
}
