package sni

// The host's view of an item another process exports: what it looks like
// and how to click it.

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// State is what a tray draws an item from.
type State struct {
	ID, Title, Status string

	// Icon is the item's own pixels. IconName names one from the icon
	// theme instead, in IconThemePath first when that is set; an item may
	// send either or both.
	Icon          []Pixmap
	IconName      string
	IconThemePath string

	// Menu is a com.canonical.dbusmenu object on the item's connection,
	// or "" when it has none. ItemIsMenu says a primary click should open
	// it rather than call Activate.
	Menu       dbus.ObjectPath
	ItemIsMenu bool

	ToolTip ToolTip
}

// Hidden reports whether the item asks not to be shown. "Passive" is the
// specification's word for an item with nothing to say right now.
func (s *State) Hidden() bool { return strings.EqualFold(s.Status, "Passive") }

// Remote is an item exported by another connection.
type Remote struct {
	conn *dbus.Conn
	Ref  Ref
}

// NewRemote talks to the item at ref over conn.
func NewRemote(conn *dbus.Conn, ref Ref) *Remote {
	return &Remote{conn: conn, Ref: ref}
}

func (r *Remote) obj() dbus.BusObject { return r.conn.Object(r.Ref.Service, r.Ref.Path) }

// Owner is the unique name of the connection serving the item, which is
// the sender its signals carry.
func (r *Remote) Owner(ctx context.Context) (string, error) {
	if strings.HasPrefix(r.Ref.Service, ":") {
		return r.Ref.Service, nil
	}
	var owner string
	err := r.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, r.Ref.Service).Store(&owner)
	if err != nil {
		return "", fmt.Errorf("finding the owner of %s: %w", r.Ref.Service, err)
	}
	return owner, nil
}

// Fetch reads every property. A property that is missing is left at its
// zero value; one of the wrong type is too, and is among the problems,
// which do not stop the rest from being read.
func (r *Remote) Fetch(ctx context.Context) (State, []error) {
	var props map[string]dbus.Variant
	err := r.obj().CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, Interface).Store(&props)
	if err != nil {
		return State{}, []error{fmt.Errorf("reading %s: %w", r.Ref, err)}
	}
	return DecodeState(props)
}

// DecodeState reads a State out of the properties GetAll returned.
func DecodeState(props map[string]dbus.Variant) (State, []error) {
	var s State
	var problems []error
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{"Id", &s.ID},
		{"Title", &s.Title},
		{"Status", &s.Status},
		{"IconName", &s.IconName},
		{"IconThemePath", &s.IconThemePath},
	} {
		problems = appendProblem(problems, f.name, props, func(v any) bool {
			str, ok := v.(string)
			*f.dst = str
			return ok
		})
	}
	problems = appendProblem(problems, "Menu", props, func(v any) bool {
		path, ok := v.(dbus.ObjectPath)
		s.Menu = path
		return ok
	})
	problems = appendProblem(problems, "ItemIsMenu", props, func(v any) bool {
		b, ok := v.(bool)
		s.ItemIsMenu = b
		return ok
	})
	// The structured two go through Store, which knows how to fill a Go
	// struct from D-Bus's untyped one -- and which, for them, does fail
	// on a mismatch. For a plain value it converts or leaves the zero
	// value without a word, which is why those are checked above instead.
	for _, f := range []struct {
		name string
		dst  any
	}{{"IconPixmap", &s.Icon}, {"ToolTip", &s.ToolTip}} {
		v, ok := props[f.name]
		if !ok {
			continue
		}
		if err := dbus.Store([]any{v.Value()}, f.dst); err != nil {
			problems = append(problems, fmt.Errorf("property %s (%s): %w", f.name, v.Signature(), err))
		}
	}
	if s.Menu == "/" {
		// What an item without a menu sends when its library insists on
		// sending an object path anyway.
		s.Menu = ""
	}
	return s, problems
}

// appendProblem reads one property with set, which reports whether the
// value had the right type, and notes it when it did not. An absent
// property is not a problem: every one is optional.
func appendProblem(problems []error, name string, props map[string]dbus.Variant, set func(any) bool) []error {
	v, ok := props[name]
	if !ok || set(v.Value()) {
		return problems
	}
	return append(problems, fmt.Errorf("property %s has type %s", name, v.Signature()))
}

// Click sends one activation or scroll. x and y are the screen position to
// put anything it opens at, in real pixels.
func (r *Remote) Click(ctx context.Context, c Click) error {
	var err error
	switch c.Kind {
	case Activate:
		err = r.obj().CallWithContext(ctx, Interface+".Activate", 0, toInt32(c.X), toInt32(c.Y)).Err
	case SecondaryActivate:
		err = r.obj().CallWithContext(ctx, Interface+".SecondaryActivate", 0, toInt32(c.X), toInt32(c.Y)).Err
	case ContextMenu:
		err = r.obj().CallWithContext(ctx, Interface+".ContextMenu", 0, toInt32(c.X), toInt32(c.Y)).Err
	case Scroll:
		orientation := "vertical"
		if c.Horizontal {
			orientation = "horizontal"
		}
		err = r.obj().CallWithContext(ctx, Interface+".Scroll", 0, toInt32(c.Delta), orientation).Err
	default:
		return fmt.Errorf("unknown click kind %d", c.Kind)
	}
	if err != nil {
		return fmt.Errorf("clicking %s: %w", r.Ref, err)
	}
	return nil
}

// Unsupported reports whether err says the item does not implement the
// method called. Items that only have a menu often leave out Activate.
func Unsupported(err error) bool {
	return errorName(err) == "org.freedesktop.DBus.Error.UnknownMethod"
}

// ItemSignals is the match rule for every item's signals, to be added once
// on the host's connection. Signals arrive from each item's unique name.
func ItemSignals() []dbus.MatchOption {
	return []dbus.MatchOption{dbus.WithMatchInterface(Interface)}
}

// Changed reports whether sig is an item saying something about it
// changed: its icon, title, status, tooltip or menu. None of the signals
// carries enough to act on by itself, so each means reading the
// properties again.
func Changed(sig *dbus.Signal) bool {
	if sig == nil {
		return false
	}
	member, ok := strings.CutPrefix(sig.Name, Interface+".")
	if !ok {
		return false
	}
	switch member {
	case "NewIcon", "NewAttentionIcon", "NewOverlayIcon", "NewIconThemePath",
		"NewTitle", "NewStatus", "NewToolTip", "NewMenu":
		return true
	}
	return false
}
