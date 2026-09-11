package notify

import (
	"math"
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func note(summary string, timeout time.Duration) *Notification {
	return &Notification{Summary: summary, Timeout: timeout, Urgency: Normal, Value: -1}
}

func shownIDs(q *Queue) []uint32 {
	shown := q.Shown()
	out := make([]uint32, len(shown))
	for i := range shown {
		out[i] = shown[i].ID
	}
	return out
}

func sec(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

func TestIDsStartAtOneAndNewestIsOnTop(t *testing.T) {
	q := NewQueue(5)
	for want := uint32(1); want <= 3; want++ {
		if id := q.Notify(note("n", sec(5)), 0, t0); id != want {
			t.Fatalf("id = %d, want %d", id, want)
		}
	}
	if got, want := shownIDs(q), []uint32{3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("shown = %v, want newest first %v", got, want)
	}
}

func TestReplaceKeepsIDAndPlaceAndRestartsTheTimeout(t *testing.T) {
	q := NewQueue(5)
	a := q.Notify(note("a", sec(5)), 0, t0)
	b := q.Notify(note("b", sec(5)), 0, t0)
	revBefore := q.Shown()[1].Rev

	if id := q.Notify(note("a again", sec(5)), a, t0.Add(sec(3))); id != a {
		t.Fatalf("replacement got id %d, want %d", id, a)
	}
	shown := q.Shown()
	if got, want := shownIDs(q), []uint32{b, a}; !slices.Equal(got, want) {
		t.Fatalf("shown = %v, want the replacement to keep its place %v", got, want)
	}
	if shown[1].Summary != "a again" || shown[1].Rev == revBefore {
		t.Errorf("replacement = %q rev %d, want new content and a new rev", shown[1].Summary, shown[1].Rev)
	}

	closed := q.Expire(t0.Add(sec(5)))
	if !slices.Equal(closed, []Closure{{ID: b, Reason: ReasonExpired}}) {
		t.Fatalf("at 5s closed %v, want only %d: the replacement's clock restarted", closed, b)
	}
	if closed = q.Expire(t0.Add(sec(8))); !slices.Equal(closed, []Closure{{ID: a, Reason: ReasonExpired}}) {
		t.Fatalf("at 8s closed %v, want %d", closed, a)
	}
}

func TestReplacingAnUnknownIDIsANewNotification(t *testing.T) {
	q := NewQueue(5)
	if id := q.Notify(note("n", 0), 42, t0); id != 1 {
		t.Fatalf("id = %d, want a fresh 1", id)
	}
}

func TestSyncReplaces(t *testing.T) {
	q := NewQueue(5)
	vol := note("Volume 40%", sec(2))
	vol.Sync = "hint:volume"
	first := q.Notify(vol, 0, t0)
	q.Notify(note("mail", sec(5)), 0, t0)

	vol2 := note("Volume 45%", sec(2))
	vol2.Sync = "hint:volume"
	if id := q.Notify(vol2, 0, t0.Add(sec(1))); id != first {
		t.Fatalf("synchronous notification got id %d, want it to replace %d", id, first)
	}
	if len(q.Shown()) != 2 {
		t.Fatalf("%d shown, want the volume popup replaced rather than stacked", len(q.Shown()))
	}
}

func TestExcessWaitsAndItsClockStartsWhenShown(t *testing.T) {
	q := NewQueue(2)
	q.Notify(note("1", sec(5)), 0, t0)
	q.Notify(note("2", sec(5)), 0, t0)
	third := q.Notify(note("3", sec(5)), 0, t0)
	if q.Waiting() != 1 || slices.Contains(shownIDs(q), third) {
		t.Fatalf("shown %v, waiting %d; want the third held back", shownIDs(q), q.Waiting())
	}

	q.Expire(t0.Add(sec(5)))
	if got := shownIDs(q); !slices.Equal(got, []uint32{third}) {
		t.Fatalf("after the first two expired, shown = %v, want %d promoted", got, third)
	}
	if closed := q.Expire(t0.Add(sec(9))); closed != nil {
		t.Fatalf("closed %v at 9s: the waiting time was counted against it", closed)
	}
	if closed := q.Expire(t0.Add(sec(10))); len(closed) != 1 || closed[0].ID != third {
		t.Fatalf("closed %v at 10s, want %d", closed, third)
	}
}

func TestClose(t *testing.T) {
	q := NewQueue(1)
	a := q.Notify(note("a", 0), 0, t0)
	b := q.Notify(note("b", 0), 0, t0) // waiting

	if got := q.Close(b, ReasonClosed, t0); !slices.Equal(got, []Closure{{ID: b, Reason: ReasonClosed}}) {
		t.Errorf("closing a waiting notification = %v", got)
	}
	if got := q.Close(a, ReasonDismissed, t0); !slices.Equal(got, []Closure{{ID: a, Reason: ReasonDismissed}}) {
		t.Errorf("closing a shown notification = %v", got)
	}
	if got := q.Close(a, ReasonClosed, t0); got != nil {
		t.Errorf("closing it twice = %v, want nothing", got)
	}
	if len(q.Shown()) != 0 || q.Waiting() != 0 {
		t.Errorf("queue not empty: shown %v, waiting %d", shownIDs(q), q.Waiting())
	}
}

func TestPauseHoldsEveryTimeout(t *testing.T) {
	q := NewQueue(5)
	id := q.Notify(note("n", sec(5)), 0, t0)
	q.Pause(t0.Add(sec(2)))
	if closed := q.Expire(t0.Add(sec(10))); closed != nil {
		t.Fatalf("expired %v while paused", closed)
	}
	if _, ok := q.NextDeadline(); ok {
		t.Error("NextDeadline reported a deadline while paused")
	}
	q.Resume(t0.Add(sec(12))) // ten seconds paused: due at 15s now
	if at, ok := q.NextDeadline(); !ok || !at.Equal(t0.Add(sec(15))) {
		t.Fatalf("NextDeadline = %v %t, want 15s", at, ok)
	}
	if closed := q.Expire(t0.Add(sec(14))); closed != nil {
		t.Fatalf("expired %v at 14s", closed)
	}
	if closed := q.Expire(t0.Add(sec(15))); len(closed) != 1 || closed[0].ID != id {
		t.Fatalf("closed %v at 15s, want %d", closed, id)
	}
}

func TestANotificationArrivingDuringAPauseGetsItsFullTime(t *testing.T) {
	q := NewQueue(5)
	q.Pause(t0)
	id := q.Notify(note("n", sec(5)), 0, t0.Add(sec(4)))
	q.Resume(t0.Add(sec(10)))
	if closed := q.Expire(t0.Add(sec(14.9))); closed != nil {
		t.Fatalf("closed %v before five seconds on screen unpaused", closed)
	}
	if closed := q.Expire(t0.Add(sec(15))); len(closed) != 1 || closed[0].ID != id {
		t.Fatalf("closed %v at 15s, want %d", closed, id)
	}
}

func TestPauseAndResumeAreIdempotent(t *testing.T) {
	q := NewQueue(5)
	q.Notify(note("n", sec(5)), 0, t0)
	q.Resume(t0.Add(sec(1))) // not paused: nothing happens
	q.Pause(t0.Add(sec(1)))
	q.Pause(t0.Add(sec(3))) // already paused: the first time stands
	q.Resume(t0.Add(sec(4)))
	if at, _ := q.NextDeadline(); !at.Equal(t0.Add(sec(8))) {
		t.Fatalf("deadline = %v, want 8s (three seconds paused)", at.Sub(t0))
	}
}

func TestInvoke(t *testing.T) {
	q := NewQueue(1)
	n := note("n", 0)
	n.Actions = []Action{{Key: DefaultAction}, {Key: "reply", Label: "Reply"}}
	id := q.Notify(n, 0, t0)

	if ok, closed := q.Invoke(id, "missing", t0); ok || closed != nil {
		t.Errorf("invoking an action that was not offered = %t %v", ok, closed)
	}
	ok, closed := q.Invoke(id, "reply", t0)
	if !ok || !slices.Equal(closed, []Closure{{ID: id, Reason: ReasonDismissed}}) {
		t.Errorf("Invoke = %t %v, want it to close as dismissed", ok, closed)
	}
}

func TestInvokeLeavesResidentNotificationsUp(t *testing.T) {
	q := NewQueue(1)
	n := note("n", 0)
	n.Actions = []Action{{Key: "pause", Label: "Pause"}}
	n.Resident = true
	id := q.Notify(n, 0, t0)
	if ok, closed := q.Invoke(id, "pause", t0); !ok || closed != nil {
		t.Errorf("Invoke = %t %v, want it invoked and still open", ok, closed)
	}
	if len(q.Shown()) != 1 {
		t.Error("resident notification closed")
	}
}

func TestInvokeNeedsTheNotificationOnScreen(t *testing.T) {
	q := NewQueue(1)
	q.Notify(note("shown", 0), 0, t0)
	n := note("waiting", 0)
	n.Actions = []Action{{Key: DefaultAction}}
	id := q.Notify(n, 0, t0)
	if ok, _ := q.Invoke(id, DefaultAction, t0); ok {
		t.Error("invoked an action on a notification nobody can see")
	}
}

func TestDoNotDisturbShowsOnlyCritical(t *testing.T) {
	q := NewQueue(5)
	q.DoNotDisturb = true
	quiet := q.Notify(note("quiet", sec(5)), 0, t0)
	loud := note("loud", 0)
	loud.Urgency = Critical
	critical := q.Notify(loud, 0, t0)
	if quiet == 0 || quiet == critical {
		t.Fatalf("ids %d and %d: a suppressed notification is still owed a distinct id", quiet, critical)
	}
	if got := shownIDs(q); !slices.Equal(got, []uint32{critical}) {
		t.Fatalf("shown = %v, want only the critical one", got)
	}
}

func TestIDsSkipZeroAndLiveOnesWhenTheyWrap(t *testing.T) {
	q := NewQueue(5)
	q.lastID = math.MaxUint32 - 1
	if id := q.Notify(note("a", 0), 0, t0); id != math.MaxUint32 {
		t.Fatalf("id = %d, want MaxUint32", id)
	}
	if id := q.Notify(note("b", 0), 0, t0); id != 1 {
		t.Fatalf("id after wrapping = %d, want 1: zero means no id", id)
	}
	q.lastID = 0
	if id := q.Notify(note("c", 0), 0, t0); id != 2 {
		t.Fatalf("id = %d, want 2: 1 is still on screen", id)
	}
}

func TestNextDeadline(t *testing.T) {
	q := NewQueue(5)
	q.Notify(note("sticky", 0), 0, t0)
	if _, ok := q.NextDeadline(); ok {
		t.Fatal("a notification with no timeout produced a deadline")
	}
	q.Notify(note("late", sec(9)), 0, t0)
	q.Notify(note("soon", sec(3)), 0, t0)
	if at, ok := q.NextDeadline(); !ok || !at.Equal(t0.Add(sec(3))) {
		t.Fatalf("NextDeadline = %v %t, want the earliest, 3s", at.Sub(t0), ok)
	}
}

func TestShownIsACopy(t *testing.T) {
	q := NewQueue(5)
	q.Notify(note("n", 0), 0, t0)
	q.Shown()[0].Summary = "changed"
	if q.Shown()[0].Summary != "n" {
		t.Fatal("changing what Shown returned changed the queue")
	}
}
