package errlog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newLimiter(burst int, per time.Duration) (*Limiter, *clock, *[]string) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	var got []string
	l := &Limiter{Burst: burst, Per: per, now: c.now, Output: func(s string) { got = append(got, s) }}
	return l, c, &got
}

func TestLimiterAdmitsTheBurstThenSuppresses(t *testing.T) {
	l, _, got := newLimiter(2, time.Minute)
	for i := range 5 {
		l.Printf("k", "failure %d", i)
	}
	if want := []string{"failure 0", "failure 1"}; !equal(*got, want) {
		t.Errorf("logged %q, want %q", *got, want)
	}
}

func TestLimiterReportsWhatItSuppressed(t *testing.T) {
	l, c, got := newLimiter(1, time.Minute)
	for range 4 {
		l.Printf("k", "boom")
	}
	c.advance(time.Minute)
	l.Printf("k", "boom again")
	if n := len(*got); n != 2 {
		t.Fatalf("logged %d lines, want 2: %q", n, *got)
	}
	if !strings.Contains((*got)[1], "3 more like this suppressed") {
		t.Errorf("second line %q does not report the 3 suppressed", (*got)[1])
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _, got := newLimiter(1, time.Minute)
	l.Printf("a", "a1")
	l.Printf("a", "a2")
	l.Printf("b", "b1")
	if want := []string{"a1", "b1"}; !equal(*got, want) {
		t.Errorf("logged %q, want %q", *got, want)
	}
}

func TestLimiterZeroBurstStillLogsOnce(t *testing.T) {
	// A zero-value Burst must not turn the limiter into a silencer.
	l, _, got := newLimiter(0, time.Minute)
	l.Printf("k", "only")
	l.Printf("k", "dropped")
	if want := []string{"only"}; !equal(*got, want) {
		t.Errorf("logged %q, want %q", *got, want)
	}
}

func TestLimiterStaysBoundedUnderUnboundedKeys(t *testing.T) {
	l, c, _ := newLimiter(1, time.Second)
	for i := range 10 * maxKeys {
		l.Printf(fmt.Sprint(i), "x")
		c.advance(10 * time.Millisecond)
	}
	if n := len(l.keys); n > maxKeys+1 {
		t.Errorf("table holds %d keys, want at most %d", n, maxKeys+1)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
