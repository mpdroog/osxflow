package xwin

import (
	"time"
)

// Await opens a Server off the caller's goroutine and delivers it on the
// returned channel once open succeeds, trying again every retry until it
// does or done closes.
//
// It is for a caller that can do without the window list for a while --
// menubar without the focused application's name, notifyd without knowing
// which monitor is active -- and must not wait for it at startup. Opening
// the X11 Server waits up to wmStartupGrace for a window manager, and
// both of those have better things to do in that time: menubar is the tray
// every menu registers with, and notifyd was started by the bus to answer
// a Notify that times out sooner than that. A window manager that is not
// up yet will be later, so a failure is not the end of trying either.
//
// logf hears each failure that differs from the one before, so a failure
// that persists is said once rather than every retry. The channel is
// buffered: a Server that arrives after the caller stopped listening is
// left there rather than blocking the goroutine for ever.
func Await(open func() (Server, error), retry time.Duration, logf func(string, ...any), done <-chan struct{}) <-chan Server {
	out := make(chan Server, 1)
	go func() {
		var last string
		for {
			s, err := open()
			if err == nil {
				if last != "" {
					logf("window list available again")
				}
				out <- s
				return
			}
			if msg := err.Error(); msg != last {
				logf("window list unavailable, trying again every %v: %v", retry, err)
				last = msg
			}
			t := time.NewTimer(retry)
			select {
			case <-t.C:
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return out
}
