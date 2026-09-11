package notify

import (
	"slices"
	"time"
)

// Reason is why a notification closed, as NotificationClosed reports it.
type Reason uint32

// The reasons the specification defines.
const (
	ReasonExpired   Reason = 1
	ReasonDismissed Reason = 2
	ReasonClosed    Reason = 3
	ReasonUndefined Reason = 4
)

// Closure is one NotificationClosed signal owed to the bus.
type Closure struct {
	ID     uint32
	Reason Reason
}

// Shown is a notification on screen.
type Shown struct {
	Notification

	// Rev changes whenever the notification's content does, which is how
	// a renderer knows a banner it already has needs drawing again.
	Rev uint64
}

// Queue is every notification the daemon is holding, and the rules for
// when each one goes.
//
// At most a fixed number are on screen; the rest wait, oldest first, and
// appear as room is made. A notification's timeout runs only while it is
// on screen, so one that spent a minute waiting still gets its full five
// seconds of being seen.
//
// Time is always passed in rather than read, so tests can drive it.
type Queue struct {
	// DoNotDisturb drops everything below critical urgency on arrival.
	// Such a notification still gets an id, because the sender is owed one,
	// but is never shown.
	DoNotDisturb bool

	maxShown int
	shown    []*entry // newest first, the order they are stacked in
	waiting  []*entry // oldest first
	lastID   uint32
	lastRev  uint64

	// pausedAt is when expiry was paused, or zero while it runs.
	pausedAt time.Time
}

type entry struct {
	n        Notification
	rev      uint64
	deadline time.Time // zero: stays until closed
}

// NewQueue returns an empty queue that shows at most maxShown
// notifications at once.
func NewQueue(maxShown int) *Queue {
	return &Queue{maxShown: max(maxShown, 1)}
}

// Notify adds a notification, or replaces one, and returns its id.
//
// A notification replaces an existing one when replaces names it or when
// both carry the same Sync key. A replaced notification keeps its id and
// its place and has its timeout restarted, which is what keeps a volume
// popup up for as long as the key is held. A replaces id that is unknown
// -- most often because that notification already closed -- is not an
// error: the specification says to treat it as new.
func (q *Queue) Notify(n *Notification, replaces uint32, now time.Time) uint32 {
	e := q.find(replaces)
	if e == nil && n.Sync != "" {
		e = q.findSync(n.Sync)
	}
	if e != nil {
		id := e.n.ID
		e.n = *n
		e.n.ID = id
		e.rev = q.nextRev()
		if slices.Contains(q.shown, e) {
			e.deadline = q.deadlineFor(&e.n, now)
		}
		return id
	}

	id := q.nextID()
	if q.DoNotDisturb && n.Urgency != Critical {
		return id
	}
	e = &entry{n: *n, rev: q.nextRev()}
	e.n.ID = id
	if len(q.shown) < q.maxShown {
		q.show(e, now)
	} else {
		q.waiting = append(q.waiting, e)
	}
	return id
}

// Close removes a notification for the given reason, returning the signal
// owed for it, or nothing when there was no such notification.
func (q *Queue) Close(id uint32, reason Reason, now time.Time) []Closure {
	if i := index(q.shown, id); i >= 0 {
		q.shown = slices.Delete(q.shown, i, i+1)
		q.promote(now)
		return []Closure{{ID: id, Reason: reason}}
	}
	if i := index(q.waiting, id); i >= 0 {
		q.waiting = slices.Delete(q.waiting, i, i+1)
		return []Closure{{ID: id, Reason: reason}}
	}
	return nil
}

// Invoke records that the user chose an action. It reports whether the
// notification is on screen and offers that action, and returns the
// closure owed when invoking it closes the notification, which it does
// unless the notification is resident.
func (q *Queue) Invoke(id uint32, key string, now time.Time) (bool, []Closure) {
	i := index(q.shown, id)
	if i < 0 || !q.shown[i].n.HasAction(key) {
		return false, nil
	}
	if q.shown[i].n.Resident {
		return true, nil
	}
	return true, q.Close(id, ReasonDismissed, now)
}

// Expire closes every notification whose time is up.
func (q *Queue) Expire(now time.Time) []Closure {
	if q.Paused() {
		return nil
	}
	var out []Closure
	q.shown = slices.DeleteFunc(q.shown, func(e *entry) bool {
		if e.deadline.IsZero() || now.Before(e.deadline) {
			return false
		}
		out = append(out, Closure{ID: e.n.ID, Reason: ReasonExpired})
		return true
	})
	if len(out) > 0 {
		q.promote(now)
	}
	return out
}

// Pause stops every timeout, and Resume restarts them with the time spent
// paused added back. The daemon pauses while the pointer is over a
// notification, so that nothing is taken away while it is being read --
// and nothing above it expires and moves the stack out from under the
// pointer, either.
func (q *Queue) Pause(now time.Time) {
	if !q.Paused() {
		q.pausedAt = now
	}
}

// Resume undoes Pause.
func (q *Queue) Resume(now time.Time) {
	if !q.Paused() {
		return
	}
	held := now.Sub(q.pausedAt)
	q.pausedAt = time.Time{}
	if held <= 0 {
		return
	}
	for _, e := range q.shown {
		if !e.deadline.IsZero() {
			e.deadline = e.deadline.Add(held)
		}
	}
}

// Paused reports whether expiry is stopped.
func (q *Queue) Paused() bool { return !q.pausedAt.IsZero() }

// NextDeadline is when Expire next has something to do. It reports false
// when nothing on screen expires, or while paused.
func (q *Queue) NextDeadline() (time.Time, bool) {
	if q.Paused() {
		return time.Time{}, false
	}
	var next time.Time
	for _, e := range q.shown {
		if !e.deadline.IsZero() && (next.IsZero() || e.deadline.Before(next)) {
			next = e.deadline
		}
	}
	return next, !next.IsZero()
}

// Shown returns what is on screen, newest first.
func (q *Queue) Shown() []Shown {
	out := make([]Shown, len(q.shown))
	for i, e := range q.shown {
		out[i] = Shown{Notification: e.n, Rev: e.rev}
	}
	return out
}

// Waiting reports how many notifications are held back for want of room.
func (q *Queue) Waiting() int { return len(q.waiting) }

func (q *Queue) show(e *entry, now time.Time) {
	e.deadline = q.deadlineFor(&e.n, now)
	q.shown = slices.Insert(q.shown, 0, e)
}

// promote moves waiting notifications on screen while there is room.
func (q *Queue) promote(now time.Time) {
	for len(q.shown) < q.maxShown && len(q.waiting) > 0 {
		e := q.waiting[0]
		q.waiting = slices.Delete(q.waiting, 0, 1)
		q.show(e, now)
	}
}

// deadlineFor is when a notification appearing now should expire.
//
// While paused, the clock is taken to have stopped when the pause began.
// Resume adds the whole pause back onto every deadline, so a notification
// that arrived halfway through one would otherwise be given that first
// half as a bonus.
func (q *Queue) deadlineFor(n *Notification, now time.Time) time.Time {
	if n.Timeout <= 0 {
		return time.Time{}
	}
	if q.Paused() {
		now = q.pausedAt
	}
	return now.Add(n.Timeout)
}

func (q *Queue) find(id uint32) *entry {
	if id == 0 {
		return nil
	}
	if i := index(q.shown, id); i >= 0 {
		return q.shown[i]
	}
	if i := index(q.waiting, id); i >= 0 {
		return q.waiting[i]
	}
	return nil
}

func (q *Queue) findSync(key string) *entry {
	for _, list := range [...][]*entry{q.shown, q.waiting} {
		for _, e := range list {
			if e.n.Sync == key {
				return e
			}
		}
	}
	return nil
}

// nextID hands out ids from 1, skipping 0 -- which the specification
// reserves to mean "no id" -- and any still in use after the counter
// wraps, which at one notification a second takes 136 years.
func (q *Queue) nextID() uint32 {
	for {
		q.lastID++
		if q.lastID != 0 && q.find(q.lastID) == nil {
			return q.lastID
		}
	}
}

func (q *Queue) nextRev() uint64 {
	q.lastRev++
	return q.lastRev
}

func index(list []*entry, id uint32) int {
	return slices.IndexFunc(list, func(e *entry) bool { return e.n.ID == id })
}
