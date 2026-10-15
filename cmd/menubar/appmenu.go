package main

// The focused application's menu: what opens when its name on the bar is
// clicked.
//
// macOS opens the application's own File menu there, and almost nothing
// here has one to hand over. A menu on the bar rather than in the window is
// what the appmenu protocols are for, GTK and Qt do not agree on which
// protocol that is, and a Wayland-native client publishes neither -- foot,
// this machine's terminal, has no menus at all to publish. Reading a real
// appmenu is still worth doing for the Qt applications that export one; it
// is a separate piece of work, and it would leave the name dead to a click
// for everything else in the meantime.
//
// So this menu is osxflow's own, and says so by what is in it: the Desktop
// Actions the application's .desktop entry declares, which is the only
// menu every application already publishes, and the window the compositor
// says has focus.

import (
	"log"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/menu"
)

// appRows is the focused application's menu: a new window and the entry's
// Desktop Actions when its window matched an entry, then what can be done
// to the window itself.
//
// It is never empty when there is a name on the bar to click. A name is
// shown only for a focused, listable window, and closing that window is
// offered whether or not anything else could be.
func (a *app) appRows() []menu.Row {
	var rows []menu.Row
	if owner := a.appOwner; owner != nil {
		// The entry's own Exec first, under the name the dock uses for it.
		// For an application that is already running this is what its
		// entry means in practice: another window.
		rows = append(rows, menu.Row{Kind: menu.Action, Label: "New Window",
			Click: a.spawn(owner.Name, owner.Argv, owner.Terminal)})
		for _, act := range owner.Actions {
			// Terminal is the entry's, not the action's: the spec puts the
			// key on the application, and an action of a terminal program
			// needs the same wrapping its Exec does.
			rows = append(rows, menu.Row{Kind: menu.Action, Label: act.Name,
				Click: a.spawn(owner.Name+": "+act.Name, act.Argv, owner.Terminal)})
		}
	}
	if win := a.appWindow; win != 0 && a.windows != nil {
		if len(rows) > 0 {
			rows = append(rows, menu.Row{Kind: menu.Separator})
		}
		rows = append(rows, menu.Row{Kind: menu.Action, Label: "Close Window", Click: func() {
			go func() {
				if err := a.windows.CloseWindow(win); err != nil {
					a.errs <- err
				}
			}()
		}})
	}
	return rows
}

// spawn is a row's Click that starts a command.
//
// On a goroutine, like every other click that leaves this process: starting
// something means a fork, a PATH lookup and a systemd-run round trip, and
// the main loop is what redraws the bar and answers the menu's own events.
// The name is only for the log and the scope the process is started in.
func (a *app) spawn(name string, argv []string, terminal bool) func() {
	app := &desktop.App{Name: name, Argv: argv, Terminal: terminal}
	return func() {
		go func() {
			if err := launch.SpawnDetached(app); err != nil {
				a.errs <- err
			}
		}()
	}
}

// openAppMenu opens the focused application's menu under its name on the
// bar, centred on centreX in screen coordinates.
func (a *app) openAppMenu(centreX int) {
	rows := a.appRows()
	if len(rows) == 0 {
		// A name was on the bar to be clicked, so something was focused:
		// the window went away between the click and here, which is a race
		// with the user and not worth more than a line.
		log.Print("the focused application has nothing left to show in its menu")
		return
	}
	if err := a.host.Open(centreX, rows); err != nil {
		log.Printf("opening the focused application's menu: %v", err)
	}
}
