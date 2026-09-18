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
// on the click, and xfce4-session-logout ends the session the way the
// session's own dialog would.
func mainRows(userName string, act actions) []menu.Row {
	logOut := "Log Out"
	if userName != "" {
		logOut = "Log Out " + userName
	}
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
