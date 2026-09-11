// Package xfconf reads XFCE settings over D-Bus, and follows them as they
// change.
//
// internal/scale reads one setting straight out of xfconf's XML file,
// because it runs once at startup and a file read is cheaper than a bus
// connection. This package is for a program that already has the bus open
// and needs to hear about a change the moment it happens -- the panel's
// do-not-disturb toggle, say -- which a file cannot tell anybody.
package xfconf

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// The xfconfd daemon's well-known name, object and interface.
const (
	BusName    = "org.xfce.Xfconf"
	ObjectPath = dbus.ObjectPath("/org/xfce/Xfconf")
	Interface  = "org.xfce.Xfconf"
)

// Change is one property changing, or being removed (reset to default).
type Change struct {
	Channel  string
	Property string
	Value    any
	Removed  bool
}

// Client talks to xfconfd.
type Client struct {
	conn *dbus.Conn
}

// New returns a client using conn, which it does not own.
func New(conn *dbus.Conn) *Client { return &Client{conn: conn} }

// ErrNotSet is returned by Get for a property that has never been set,
// which in xfconf means it holds its program's built-in default.
var ErrNotSet = errors.New("xfconf property not set")

// Get reads one property.
func (c *Client) Get(channel, property string) (any, error) {
	var v dbus.Variant
	err := c.conn.Object(BusName, ObjectPath).Call(Interface+".GetProperty", 0, channel, property).Store(&v)
	if err != nil {
		var dbusErr dbus.Error
		if errors.As(err, &dbusErr) && dbusErr.Name == "org.xfce.Xfconf.Error.PropertyNotFound" {
			return nil, ErrNotSet
		}
		return nil, fmt.Errorf("reading %s %s: %w", channel, property, err)
	}
	return v.Value(), nil
}

// Watch delivers every change to a channel until the connection closes,
// when the channel it returns is closed too.
//
// It registers for every signal the connection receives and filters them
// here, so it should be called once per connection.
func (c *Client) Watch(channel string) (<-chan Change, error) {
	err := c.conn.AddMatchSignal(
		dbus.WithMatchInterface(Interface),
		dbus.WithMatchObjectPath(ObjectPath),
		dbus.WithMatchArg(0, channel),
	)
	if err != nil {
		return nil, fmt.Errorf("subscribing to xfconf channel %s: %w", channel, err)
	}
	signals := make(chan *dbus.Signal, 16)
	c.conn.Signal(signals)

	out := make(chan Change, 16)
	go func() {
		defer close(out)
		// godbus closes signals when the connection closes.
		for sig := range signals {
			if ch, ok := parse(sig, channel); ok {
				out <- ch
			}
		}
	}()
	return out, nil
}

// parse reads a PropertyChanged or PropertyRemoved signal about channel.
func parse(sig *dbus.Signal, channel string) (Change, bool) {
	if sig.Path != ObjectPath {
		return Change{}, false
	}
	var removed bool
	switch sig.Name {
	case Interface + ".PropertyChanged":
	case Interface + ".PropertyRemoved":
		removed = true
	default:
		return Change{}, false
	}
	if len(sig.Body) < 2 {
		return Change{}, false
	}
	ch, okC := sig.Body[0].(string)
	prop, okP := sig.Body[1].(string)
	if !okC || !okP || ch != channel {
		return Change{}, false
	}
	out := Change{Channel: ch, Property: prop, Removed: removed}
	if !removed && len(sig.Body) >= 3 {
		if v, ok := sig.Body[2].(dbus.Variant); ok {
			out.Value = v.Value()
		}
	}
	return out, true
}
