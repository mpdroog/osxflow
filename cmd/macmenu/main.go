// Command macmenu is the system menu at the left end of the panel, the way
// macOS has one under the Apple logo: About This Mac, System Settings, Task
// Manager, Sleep, Restart, Shut Down, Lock Screen and Log Out.
//
// It replaces Whisker Menu for everything but finding apps, which is the
// launcher's (Cmd+Space). A panel launcher runs it on click: it opens under
// the pointer, does what is chosen, and exits -- nothing stays in memory
// between clicks. Clicking the launcher again while it is open closes it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os/user"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/menu"
)

func main() {
	log.SetPrefix("macmenu: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

const (
	menuWidth = 240

	// busName makes macmenu single-instance: a second click on the
	// launcher finds the first menu open and closes it instead of stacking
	// another on top.
	busName = "org.osxflow.MacMenu"
	busPath = dbus.ObjectPath("/org/osxflow/MacMenu")
)

func start() error {
	scaleFlag := flag.Float64("scale", 0, "display scale factor (0 detects it)")
	flag.Parse()

	session, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			log.Printf("closing the session bus: %v", closeErr)
		}
	}()
	closeReq, first, err := claim(session)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}

	xu, err := xgbutil.NewConn()
	if err != nil {
		return fmt.Errorf("connecting to X display: %w", err)
	}
	defer xu.Conn().Close()
	host, err := menu.NewHost(xu, *scaleFlag, menuWidth)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := host.Release(); releaseErr != nil {
			log.Printf("releasing the menu: %v", releaseErr)
		}
	}()

	a := &app{conn: xu.Conn(), host: host}
	if a.x, err = host.PointerX(); err != nil {
		// The menu opens at the left edge, which is where the launcher is
		// anyway.
		log.Printf("%v; opening the menu at the left edge", err)
	}
	if err := host.Open(a.x, mainRows(fullName(), a)); err != nil {
		return fmt.Errorf("opening the menu: %w", err)
	}
	return a.loop(closeReq)
}

// claim takes the bus name, or, when another macmenu has it, asks that one
// to close. first reports whether this is the only one.
func claim(conn *dbus.Conn) (closeReq <-chan struct{}, first bool, err error) {
	c := &closer{req: make(chan struct{}, 1)}
	if exportErr := conn.Export(c, busPath, busName); exportErr != nil {
		return nil, false, fmt.Errorf("exporting %s: %w", busName, exportErr)
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, false, fmt.Errorf("requesting %s: %w", busName, err)
	}
	if reply == dbus.RequestNameReplyPrimaryOwner {
		return c.req, true, nil
	}
	if callErr := conn.Object(busName, busPath).Call(busName+".Close", 0).Err; callErr != nil {
		return nil, false, fmt.Errorf("closing the open menu: %w", callErr)
	}
	return nil, false, nil
}

// closer carries the one D-Bus method, separately from app because godbus
// exports every exported method of what it is given.
type closer struct{ req chan struct{} }

// Close implements org.osxflow.MacMenu.Close. It only asks: the menu is
// the main loop's to close.
//
//nolint:unparam // godbus exports only methods whose last result is *dbus.Error, even one that never fails
func (c *closer) Close() *dbus.Error {
	select {
	case c.req <- struct{}{}:
	default:
		// A close is already waiting to be acted on.
	}
	return nil
}

type app struct {
	conn *xgb.Conn
	host *menu.Host
	x    int
}

var _ actions = (*app)(nil)

// loop waits until the menu closes -- chosen, dismissed or closed from a
// second click -- and returns.
func (a *app) loop(closeReq <-chan struct{}) error {
	events := make(chan xgb.Event, 64)
	xGone := make(chan error, 1)
	go readEvents(a.conn, events, xGone)
	for a.host.IsOpen() {
		select {
		case ev := <-events:
			if err := a.host.Handle(ev); err != nil {
				log.Printf("drawing the menu: %v", err)
			}
		case err := <-xGone:
			return err
		case <-closeReq:
			a.host.Close()
		}
	}
	return nil
}

func (a *app) about() {
	info, err := readAbout()
	if err != nil {
		log.Printf("about this computer: %v", err)
	}
	if err := a.host.Open(a.x, aboutRows(&info)); err != nil {
		log.Printf("opening About This Mac: %v", err)
	}
}

func (a *app) confirm(k confirmKind) {
	if err := a.host.Open(a.x, confirmRows(k, a)); err != nil {
		log.Printf("opening the confirmation: %v", err)
	}
}

// run starts argv detached, so it outlives macmenu, which exits as soon as
// the menu has closed.
func (a *app) run(argv ...string) {
	app := &desktop.App{Name: argv[0], Argv: argv}
	if err := launch.SpawnDetached(app); err != nil {
		log.Printf("starting %s: %v", strings.Join(argv, " "), err)
	}
}

// fullName is the user's name for "Log Out <name>…": the first part of the
// GECOS field, or the login name when that is empty.
func fullName() string {
	u, err := user.Current()
	if err != nil {
		log.Printf("looking up the user: %v", err)
		return ""
	}
	if name, _, _ := strings.Cut(u.Name, ","); strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return u.Username
}

func readEvents(conn *xgb.Conn, out chan<- xgb.Event, gone chan<- error) {
	for {
		ev, err := conn.WaitForEvent()
		if err != nil {
			log.Printf("X error: %v", err)
			continue
		}
		if ev == nil {
			gone <- errors.New("the X connection closed")
			return
		}
		out <- ev
	}
}
