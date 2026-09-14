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
	confirm(k confirmKind)
}

type confirmKind int

const (
	confirmRestart confirmKind = iota
	confirmShutDown
	confirmLogOut
)

// confirmations are the actions that end the session or the machine, and so
// ask first, as macOS does. xfce4-session-logout does them the way the
// session's own dialog would -- saving the session, locking before sleep --
// just without showing that dialog.
var confirmations = [...]struct {
	title, button string
	argv          []string
}{
	confirmRestart:  {"Restart now?", "Restart", []string{"xfce4-session-logout", "--reboot"}},
	confirmShutDown: {"Shut down now?", "Shut Down", []string{"xfce4-session-logout", "--halt"}},
	confirmLogOut:   {"Log out now?", "Log Out", []string{"xfce4-session-logout", "--logout"}},
}

func separator() menu.Row { return menu.Row{Kind: menu.Separator} }

func action(label string, click func()) menu.Row {
	return menu.Row{Kind: menu.Action, Label: label, Click: click}
}

// mainRows is the menu itself. Apps are not in it: they are Cmd+Space, the
// launcher's job.
func mainRows(userName string, act actions) []menu.Row {
	logOut := "Log Out…"
	if userName != "" {
		logOut = "Log Out " + userName + "…"
	}
	return []menu.Row{
		action("About This Mac", act.about),
		separator(),
		action("System Settings…", func() { act.run("xfce4-settings-manager") }),
		action("Task Manager…", func() { act.run("xfce4-taskmanager") }),
		separator(),
		action("Sleep", func() { act.run("xfce4-session-logout", "--suspend") }),
		action("Restart…", func() { act.confirm(confirmRestart) }),
		action("Shut Down…", func() { act.confirm(confirmShutDown) }),
		separator(),
		action("Lock Screen", func() { act.run("xflock4") }),
		action(logOut, func() { act.confirm(confirmLogOut) }),
	}
}

// confirmRows asks before an action that closes everything. Cancel, or a
// click anywhere else, does nothing.
func confirmRows(k confirmKind, act actions) []menu.Row {
	c := confirmations[k]
	argv := c.argv
	return []menu.Row{
		{Kind: menu.Header, Label: c.title},
		{Kind: menu.Note, Label: "Open apps will be closed."},
		separator(),
		action(c.button, func() { act.run(argv...) }),
		action("Cancel", func() {}),
	}
}

// aboutRows shows what could be read about the machine; a field that could
// not be read is left out rather than shown blank.
func aboutRows(a *about) []menu.Row {
	rows := []menu.Row{{Kind: menu.Header, Label: "About This Mac", Detail: a.model}}
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
