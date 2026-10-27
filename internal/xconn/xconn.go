// Package xconn connects to the X display, waiting for it if it is still
// starting.
//
// Under labwc the X server is XWayland, started lazily: the socket exists
// from the moment the compositor is up, but the server behind it only
// starts on the first client's knock. Everything in autostart knocks at
// once, a second after login, and a connect in that window gets
// "connect: resource temporarily unavailable" -- the socket's backlog is
// full while XWayland comes up. A tool that treats that as fatal is gone
// for the session: on 2026-10-27 soundmenu lost the race this way, and
// every volume key afterwards went to a soundmenu that did not exist,
// while netmenu and bluemenu, started a moment later, got through.
//
// So no osxflow tool connects with a single attempt. Dial keeps trying
// for long enough to cover a slow XWayland start and then gives up, so a
// session with no X at all still fails rather than hangs.
package xconn

import (
	"fmt"
	"log"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgbutil"
)

const (
	// patience is how long a display is waited for in all.
	patience = 20 * time.Second
	// firstWait doubles after each attempt, up to maxWait.
	firstWait = 50 * time.Millisecond
	maxWait   = time.Second
)

// Dial connects to the display named by DISPLAY, retrying while it is
// unavailable.
func Dial() (*xgbutil.XUtil, error) {
	conn, err := DialXgb()
	if err != nil {
		return nil, err
	}
	xu, err := xgbutil.NewConnXgb(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("setting up the X connection: %w", err)
	}
	return xu, nil
}

// DialXgb is Dial for callers that want the bare connection.
func DialXgb() (*xgb.Conn, error) {
	return retry(xgb.NewConn, patience)
}

func retry(dial func() (*xgb.Conn, error), patience time.Duration) (*xgb.Conn, error) {
	deadline := time.Now().Add(patience)
	wait := firstWait
	for attempt := 1; ; attempt++ {
		conn, err := dial()
		if err == nil {
			if attempt > 1 {
				log.Printf("connected to the X display on attempt %d", attempt)
			}
			return conn, nil
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		if attempt == 1 {
			log.Printf("X display not ready (%v); retrying for up to %v", err, patience)
		}
		time.Sleep(wait)
		wait = min(wait*2, maxWait)
	}
}
