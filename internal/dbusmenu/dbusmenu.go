// Package dbusmenu reads the menus tray items publish over D-Bus, so the
// tray can draw them itself.
//
// com.canonical.dbusmenu is what libappindicator, Qt and fyne's systray
// use: the item exports a tree of entries, each a bag of properties, and
// the tray draws it and reports clicks back by id. Only what a tray menu
// uses is read here -- labels, separators, checkmarks, enabled, visible and
// submenus. Icons and shortcuts in the entries are ignored.
package dbusmenu

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Interface is the D-Bus interface a menu is exported under.
const Interface = "com.canonical.dbusmenu"

// maxDepth bounds how deeply submenus may nest. Real menus go two or three
// levels; the tree comes from another process, and decoding recurses.
const maxDepth = 16

// Entry is one entry of a menu, and the root is an Entry too: the menu
// itself, whose Children are what is shown.
type Entry struct {
	ID int32

	// Label is the text shown, with the mnemonic underscores taken out.
	Label string

	Enabled, Visible bool
	Separator        bool

	// Toggle is "checkmark", "radio" or "" for an ordinary entry, and
	// Checked whether a checkmark or radio entry is on.
	Toggle  string
	Checked bool

	Children []Entry
}

// Decode reads an entry, and everything below it, from the (ia{sv}av)
// structure GetLayout returns, as godbus hands it over: a []any of the id,
// the properties and the children, each child a variant holding the same.
func Decode(v any) (Entry, error) { return decode(v, 0) }

func decode(v any, depth int) (Entry, error) {
	if depth > maxDepth {
		return Entry{}, fmt.Errorf("menu nested more than %d deep", maxDepth)
	}
	fields, ok := v.([]any)
	if !ok || len(fields) != 3 {
		return Entry{}, fmt.Errorf("menu entry is %T, want a struct of three", v)
	}
	id, ok := fields[0].(int32)
	if !ok {
		return Entry{}, fmt.Errorf("menu entry id is %T, want int32", fields[0])
	}
	props, ok := fields[1].(map[string]dbus.Variant)
	if !ok {
		return Entry{}, fmt.Errorf("menu entry %d properties are %T, want a{sv}", id, fields[1])
	}
	children, ok := fields[2].([]dbus.Variant)
	if !ok {
		return Entry{}, fmt.Errorf("menu entry %d children are %T, want av", id, fields[2])
	}
	e := Entry{
		ID:        id,
		Label:     Label(str(props, "label")),
		Enabled:   boolean(props, "enabled", true),
		Visible:   boolean(props, "visible", true),
		Separator: str(props, "type") == "separator",
		Toggle:    str(props, "toggle-type"),
	}
	if state, ok := props["toggle-state"].Value().(int32); ok {
		e.Checked = state == 1
	}
	for _, c := range children {
		child, err := decode(c.Value(), depth+1)
		if err != nil {
			return Entry{}, err
		}
		e.Children = append(e.Children, child)
	}
	return e, nil
}

// str reads a string property, or "" when it is absent or not a string --
// which the specification says to treat as its default.
func str(props map[string]dbus.Variant, name string) string {
	s, ok := props[name].Value().(string)
	if !ok {
		return ""
	}
	return s
}

func boolean(props map[string]dbus.Variant, name string, def bool) bool {
	b, ok := props[name].Value().(bool)
	if !ok {
		return def
	}
	return b
}

// Label takes the mnemonic markers out of a label: a single underscore
// marks the next letter as the access key and is not shown, and a double
// one is a literal underscore.
func Label(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	// Byte by byte: an underscore never occurs inside a multi-byte UTF-8
	// sequence, and a label that is not valid UTF-8 passes through as it
	// came rather than growing replacement characters.
	b := make([]byte, 0, len(s))
	escaped := false
	for i := range len(s) {
		if s[i] == '_' && !escaped {
			escaped = true
			continue
		}
		escaped = false
		b = append(b, s[i])
	}
	return string(b)
}

// Client is one item's menu.
type Client struct {
	conn *dbus.Conn
	dest string
	path dbus.ObjectPath
}

// New talks to the menu at path on dest.
func New(conn *dbus.Conn, dest string, path dbus.ObjectPath) *Client {
	return &Client{conn: conn, dest: dest, path: path}
}

func (c *Client) obj() dbus.BusObject { return c.conn.Object(c.dest, c.path) }

// AboutToShow tells the item its menu is about to open, which some items
// wait for to fill it in. The answer, whether it changed anything, is not
// needed: the layout is read afterwards either way.
func (c *Client) AboutToShow(ctx context.Context) error {
	err := c.obj().CallWithContext(ctx, Interface+".AboutToShow", 0, int32(0)).Err
	if err != nil && !unknownMethod(err) {
		return fmt.Errorf("AboutToShow on %s%s: %w", c.dest, c.path, err)
	}
	return nil
}

// Layout reads the whole menu.
func (c *Client) Layout(ctx context.Context) (Entry, error) {
	call := c.obj().CallWithContext(ctx, Interface+".GetLayout", 0, int32(0), int32(-1), []string{})
	if call.Err != nil {
		return Entry{}, fmt.Errorf("reading the menu at %s%s: %w", c.dest, c.path, call.Err)
	}
	if len(call.Body) != 2 {
		return Entry{}, fmt.Errorf("reading the menu at %s%s: %d values returned, want 2", c.dest, c.path, len(call.Body))
	}
	root, err := Decode(call.Body[1])
	if err != nil {
		return Entry{}, fmt.Errorf("reading the menu at %s%s: %w", c.dest, c.path, err)
	}
	return root, nil
}

// Clicked tells the item entry id was chosen.
func (c *Client) Clicked(ctx context.Context, id int32) error {
	err := c.obj().CallWithContext(ctx, Interface+".Event", 0,
		id, "clicked", dbus.MakeVariant(int32(0)), uint32(0)).Err
	if err != nil {
		return fmt.Errorf("clicking menu entry %d at %s%s: %w", id, c.dest, c.path, err)
	}
	return nil
}

func unknownMethod(err error) bool {
	var dbusErr dbus.Error
	var dbusErrPtr *dbus.Error
	switch {
	case errors.As(err, &dbusErr):
		return dbusErr.Name == "org.freedesktop.DBus.Error.UnknownMethod"
	case errors.As(err, &dbusErrPtr):
		return dbusErrPtr.Name == "org.freedesktop.DBus.Error.UnknownMethod"
	}
	return false
}
