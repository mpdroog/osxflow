package main

// Drawing an item's own menu -- the one it publishes over D-Bus -- with
// the same rows as osxflow's menus, so that ezhours' menu looks like the
// network menu next to it rather than like GTK's.

import (
	"github.com/mpdroog/osxflow/internal/dbusmenu"
	"github.com/mpdroog/osxflow/internal/menu"
)

// menuRows turns a menu's entries into rows. click is called with the id of
// the entry chosen.
//
// A submenu is shown flattened, under a heading of its label: a tray menu
// is short, and a menu that opens another menu sideways is more machinery
// than one level of heading is worth. A checkmark or radio entry becomes a
// switch, and a disabled one dim text.
func menuRows(root *dbusmenu.Entry, click func(id int32)) []menu.Row {
	var rows []menu.Row
	appendRows(&rows, root.Children, click)
	return tidySeparators(rows)
}

func appendRows(rows *[]menu.Row, entries []dbusmenu.Entry, click func(id int32)) {
	for i := range entries {
		e := &entries[i]
		if !e.Visible {
			continue
		}
		switch {
		case e.Separator:
			*rows = append(*rows, menu.Row{Kind: menu.Separator})
		case len(e.Children) > 0:
			*rows = append(*rows, menu.Row{Kind: menu.Separator})
			if e.Label != "" {
				*rows = append(*rows, menu.Row{Kind: menu.Section, Label: e.Label})
			}
			appendRows(rows, e.Children, click)
			*rows = append(*rows, menu.Row{Kind: menu.Separator})
		case !e.Enabled:
			*rows = append(*rows, menu.Row{Kind: menu.Note, Label: e.Label})
		case e.Toggle != "":
			id := e.ID
			*rows = append(*rows, menu.Row{Kind: menu.Toggle, Label: e.Label, On: e.Checked,
				Click: func() { click(id) }})
		default:
			id := e.ID
			*rows = append(*rows, menu.Row{Kind: menu.Action, Label: e.Label,
				Click: func() { click(id) }})
		}
	}
}

// tidySeparators drops separators at either end and runs of them, which
// flattening submenus and hiding entries both leave behind.
func tidySeparators(rows []menu.Row) []menu.Row {
	out := rows[:0]
	for i := range rows {
		if rows[i].Kind == menu.Separator && (len(out) == 0 || out[len(out)-1].Kind == menu.Separator) {
			continue
		}
		out = append(out, rows[i])
	}
	for len(out) > 0 && out[len(out)-1].Kind == menu.Separator {
		out = out[:len(out)-1]
	}
	return out
}
