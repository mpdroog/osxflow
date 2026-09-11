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
//
// A Change with Err set carries no property: it reports a signal from
// xfconfd that could not be read, so the watcher can say so rather than
// act on half of it.
type Change struct {
	Channel  string
	Property string
	Value    any
	Removed  bool
	Err      error
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

// ErrNoXfconf is returned by Get when nothing on the bus answers for
// xfconfd, which means the session is not XFCE (or xfconf is not
// installed). Every property then holds its program's built-in default, so
// a caller treats this as information, not as a fault.
var ErrNoXfconf = errors.New("xfconfd is not on the session bus")

// ErrMalformed marks a signal from xfconfd's object that does not have the
// shape its definition requires.
var ErrMalformed = errors.New("malformed xfconf signal")

// Get reads one property.
func (c *Client) Get(channel, property string) (any, error) {
	var v dbus.Variant
	err := c.conn.Object(BusName, ObjectPath).Call(Interface+".GetProperty", 0, channel, property).Store(&v)
	if err != nil {
		var dbusErr dbus.Error
		if errors.As(err, &dbusErr) {
			switch dbusErr.Name {
			case "org.xfce.Xfconf.Error.PropertyNotFound":
				return nil, ErrNotSet
			case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
				// ServiceUnknown is what the bus answers when it cannot
				// start xfconfd either; NameHasNoOwner when auto-start is
				// off or refused.
				return nil, fmt.Errorf("reading %s %s: %w: %w", channel, property, ErrNoXfconf, err)
			}
		}
		return nil, fmt.Errorf("reading %s %s: %w", channel, property, err)
	}
	return v.Value(), nil
}

// Watch delivers every change to a channel until the connection closes,
// when the channel it returns is closed too. A signal about the channel
// that cannot be read arrives as a Change with Err set.
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
			ch, ok, parseErr := parse(sig, channel)
			switch {
			case parseErr != nil:
				out <- Change{Channel: channel, Err: parseErr}
			case ok:
				out <- ch
			}
		}
	}()
	return out, nil
}

// parse reads a PropertyChanged or PropertyRemoved signal about channel.
//
// A signal that is simply not one of those -- another object's, another
// member, another channel -- is foreign and reports false with no error:
// the connection hears everything the daemon's other subscriptions bring
// in. One that is a property signal about channel but cannot be read
// reports an error, because acting on it would mean guessing: a change
// with no value is exactly what a caller would read as "off".
func parse(sig *dbus.Signal, channel string) (Change, bool, error) {
	if sig.Path != ObjectPath {
		return Change{}, false, nil
	}
	var removed bool
	switch sig.Name {
	case Interface + ".PropertyChanged":
	case Interface + ".PropertyRemoved":
		removed = true
	default:
		return Change{}, false, nil
	}
	if len(sig.Body) < 2 {
		return Change{}, false, fmt.Errorf("%w: %s has %d arguments, want at least 2", ErrMalformed, sig.Name, len(sig.Body))
	}
	ch, ok := sig.Body[0].(string)
	if !ok {
		return Change{}, false, fmt.Errorf("%w: %s channel is %T, want string", ErrMalformed, sig.Name, sig.Body[0])
	}
	if ch != channel {
		return Change{}, false, nil
	}
	prop, ok := sig.Body[1].(string)
	if !ok {
		return Change{}, false, fmt.Errorf("%w: %s property is %T, want string", ErrMalformed, sig.Name, sig.Body[1])
	}
	out := Change{Channel: ch, Property: prop, Removed: removed}
	if removed {
		return out, true, nil
	}
	if len(sig.Body) < 3 {
		return Change{}, false, fmt.Errorf("%w: PropertyChanged for %s %s has no value", ErrMalformed, ch, prop)
	}
	v, ok := sig.Body[2].(dbus.Variant)
	if !ok {
		return Change{}, false, fmt.Errorf("%w: PropertyChanged value for %s %s is %T, want a variant",
			ErrMalformed, ch, prop, sig.Body[2])
	}
	if out.Value = v.Value(); out.Value == nil {
		return Change{}, false, fmt.Errorf("%w: PropertyChanged for %s %s carries an empty variant", ErrMalformed, ch, prop)
	}
	return out, true, nil
}
