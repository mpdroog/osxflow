// Package errlog reports the errors that cannot be returned: the ones that
// arrive on their own (X protocol errors for requests sent long ago), the
// ones from cleanup after the real work is done, and the ones raised in a
// loop that would otherwise print the same failure a thousand times.
//
// The rule in this repository is that every error is either returned or
// logged. Logging in a hot path is where that rule gets quietly broken --
// someone sees the log flood and deletes the line -- so the answer here is
// to limit, never to drop: a suppressed message is counted, and the count
// is reported with the next one that gets through.
package errlog

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Limiter logs at most Burst messages per key in any Per interval.
//
// The key names a kind of failure ("x:WindowError", "exe:4312"); the
// message is free text. Keys are the caller's choice, so a caller that
// keys on something unbounded such as a pid still stays bounded: windows
// older than Per are pruned once the table grows past maxKeys.
type Limiter struct {
	Burst int
	Per   time.Duration

	// Output receives each admitted line. Nil means the standard logger,
	// which each command configures with its own prefix.
	Output func(string)

	// now is the clock, replaceable in tests.
	now func() time.Time

	mu   sync.Mutex
	keys map[string]*window
}

type window struct {
	start      time.Time
	n          int
	suppressed int
}

// maxKeys bounds the table before stale windows are pruned.
const maxKeys = 1024

// Printf logs a message under key, unless key has used up its burst.
func (l *Limiter) Printf(key, format string, args ...any) {
	msg, ok := l.admit(key, fmt.Sprintf(format, args...))
	if !ok {
		return
	}
	if l.Output != nil {
		l.Output(msg)
		return
	}
	log.Print(msg)
}

func (l *Limiter) admit(key, msg string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.keys == nil {
		l.keys = make(map[string]*window)
	}

	w := l.keys[key]
	if w == nil || now.Sub(w.start) >= l.Per {
		if len(l.keys) >= maxKeys {
			l.prune(now)
		}
		var suppressed int
		if w != nil {
			suppressed = w.suppressed
		}
		w = &window{start: now}
		l.keys[key] = w
		if suppressed > 0 {
			msg += fmt.Sprintf(" (%d more like this suppressed)", suppressed)
		}
	}
	if w.n >= max(l.Burst, 1) {
		w.suppressed++
		return "", false
	}
	w.n++
	return msg, true
}

// prune drops every window that has expired. An expired window that was
// still holding a suppressed count loses it, which is the one place a
// count can go unreported -- and only when more than maxKeys kinds of
// failure are live at once.
func (l *Limiter) prune(now time.Time) {
	for k, w := range l.keys {
		if now.Sub(w.start) >= l.Per {
			delete(l.keys, k)
		}
	}
}
