package main

// What the menu lists and what each row does, kept free of X11 so it is
// tested without a display.

import (
	"github.com/mpdroog/osxflow/internal/menu"
)

// actions is what the rows do.
type actions interface {
	about()
	// run starts a program and lets the menu go.
	run(argv ...string)
}

func separator() menu.Row { return menu.Row{Kind: menu.Separator} }

func action(label string, click func()) menu.Row {
	return menu.Row{Kind: menu.Action, Label: label, Click: click}
}

// mainRows is the menu itself. Apps are not in it: they are Cmd+Space, the
// launcher's job. Nothing asks twice: restart, shut down and log out happen
// on the click.
//
// The same binary runs on two desktops -- Alpine with labwc, and Mint with
// XFCE on the laptop -- and the commands behind the rows share nothing. A
// missing program is not an error the menu can show: the row would close
// and nothing would happen. So wayland picks the set, the same way
// WAYLAND_DISPLAY picks the display code everywhere else.
func mainRows(userName string, wayland bool, act actions) []menu.Row {
	logOut := "Log Out"
	if userName != "" {
		logOut = "Log Out " + userName
	}
	if wayland {
		return labwcRows(logOut, act)
	}
	return xfceRows(logOut, act)
}

// labwcRows is the menu on Alpine, where none of the XFCE programs exist:
//
//   - Sleep, Restart and Shut Down go through doas, because this machine
//     has no elogind and so no loginctl to ask. See files/doas/30-osxflow-power.conf
//     for the rule and the suspend helper, which must be installed as root
//     before those three rows do anything.
//   - Sleep locks first, the way the XFCE dialog did. swaylock -f returns
//     once the screen is actually covered, so the lock is up before the
//     machine goes down.
//   - Log Out is labwc's own --exit, which ends the session cleanly.
//   - There is no System Settings row: nothing on Alpine answers to that.
func labwcRows(logOut string, act actions) []menu.Row {
	return []menu.Row{
		action("About This Linux", act.about),
		separator(),
		action("Task Manager…", func() { act.run("foot", "-T", "Task Manager", "top") }),
		separator(),
		action("Sleep", func() { act.run("sh", "-c", lockCmd+" && doas /usr/local/sbin/osxflow-suspend") }),
		action("Restart", func() { act.run("doas", "/sbin/reboot") }),
		action("Shut Down", func() { act.run("doas", "/sbin/poweroff") }),
		separator(),
		action("Lock Screen", func() { act.run("sh", "-c", lockCmd) }),
		action(logOut, func() { act.run("labwc", "--exit") }),
	}
}

// xfceRows is the menu under XFCE, where xfce4-session-logout ends the
// session the way the session's own dialog would, and locks before a
// suspend by itself.
func xfceRows(logOut string, act actions) []menu.Row {
	return []menu.Row{
		action("About This Linux", act.about),
		separator(),
		action("System Settings…", func() { act.run("xfce4-settings-manager") }),
		action("Task Manager…", func() { act.run("xfce4-taskmanager") }),
		separator(),
		action("Sleep", func() { act.run("xfce4-session-logout", "--suspend") }),
		action("Restart", func() { act.run("xfce4-session-logout", "--reboot") }),
		action("Shut Down", func() { act.run("xfce4-session-logout", "--halt") }),
		separator(),
		action("Lock Screen", func() { act.run("xflock4") }),
		action(logOut, func() { act.run("xfce4-session-logout", "--logout") }),
	}
}

// lockCmd is the locker, spelled exactly as rc.xml's Ctrl+Alt+L spells it so
// that the menu and the keybind put up the same screen.
const lockCmd = "swaylock -f -c 000000"

// aboutRows shows what could be read about the machine; a field that could
// not be read is left out rather than shown blank.
func aboutRows(a *about) []menu.Row {
	rows := []menu.Row{{Kind: menu.Header, Label: "About This Linux", Detail: a.model}}
	for _, f := range []struct{ label, value string }{
		{"", a.system},
		{"Kernel ", a.kernel},
		{"Memory ", a.memory},
		{"Up ", a.uptime},
	} {
		if f.value != "" {
			rows = append(rows, menu.Row{Kind: menu.Note, Label: f.label + f.value})
		}
	}
	return append(rows, separator(), action("OK", func() {}))
}
