package bluez

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	rootPath   = dbus.ObjectPath("/")
	objectRoot = "/org/bluez"

	getManagedObjects = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
	propsGetAll       = "org.freedesktop.DBus.Properties.GetAll"
	propsSet          = "org.freedesktop.DBus.Properties.Set"

	sigInterfacesAdded   = "org.freedesktop.DBus.ObjectManager.InterfacesAdded"
	sigInterfacesRemoved = "org.freedesktop.DBus.ObjectManager.InterfacesRemoved"
	sigPropertiesChanged = "org.freedesktop.DBus.Properties.PropertiesChanged"
)

// ErrNoState means BlueZ could not be read at all -- bluetoothd not
// running, say. The State that comes with it describes nothing; keep the
// previous one.
var ErrNoState = errors.New("BlueZ state unavailable")

// Client talks to bluetoothd.
type Client struct {
	conn *dbus.Conn
}

// New returns a client on conn, which should be the system bus.
func New(conn *dbus.Conn) *Client {
	return &Client{conn: conn}
}

// Snapshot reads the adapter and its paired devices in one call.
func (c *Client) Snapshot(ctx context.Context) (State, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := c.conn.Object(BusName, rootPath).CallWithContext(ctx, getManagedObjects, 0).Store(&objects); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrNoState, err)
	}
	return ParseManaged(objects)
}

// SetPowered turns an adapter on or off. With the radio soft-blocked by
// rfkill BlueZ refuses to power on, and says so in the error: lift the
// block first.
func (c *Client) SetPowered(ctx context.Context, adapter dbus.ObjectPath, on bool) error {
	err := c.conn.Object(BusName, adapter).
		CallWithContext(ctx, propsSet, 0, ifaceAdapter, "Powered", dbus.MakeVariant(on)).Err
	if err != nil {
		return fmt.Errorf("powering %s %s: %w", adapter, onOff(on), err)
	}
	return nil
}

// Connect connects a paired device's profiles. BlueZ answers only once
// they are connected or have failed, which for a headset can take several
// seconds: give ctx room, and do not call it on a thread that draws.
func (c *Client) Connect(ctx context.Context, device dbus.ObjectPath) error {
	if err := c.conn.Object(BusName, device).CallWithContext(ctx, ifaceDevice+".Connect", 0).Err; err != nil {
		return fmt.Errorf("connecting %s: %w", device, err)
	}
	return nil
}

// Disconnect disconnects a device.
func (c *Client) Disconnect(ctx context.Context, device dbus.ObjectPath) error {
	if err := c.conn.Object(BusName, device).CallWithContext(ctx, ifaceDevice+".Disconnect", 0).Err; err != nil {
		return fmt.Errorf("disconnecting %s: %w", device, err)
	}
	return nil
}

// Watch reports that something in BlueZ changed: an adapter or device
// appearing or going, or a property changing. A burst of signals -- a
// device connecting changes half a dozen properties -- is one or two
// receives. Read a Snapshot after it fires. The channel closes when the
// connection does.
func (c *Client) Watch() (<-chan struct{}, error) {
	if err := c.conn.AddMatchSignal(dbus.WithMatchSender(BusName)); err != nil {
		return nil, fmt.Errorf("watching BlueZ: %w", err)
	}
	signals := make(chan *dbus.Signal, 64)
	c.conn.Signal(signals)
	changed := make(chan struct{}, 1)
	go func() {
		defer close(changed)
		for sig := range signals {
			if !Relevant(sig) {
				continue
			}
			select {
			case changed <- struct{}{}:
			default:
				// A change is already waiting to be read, and covers this one.
			}
		}
	}()
	return changed, nil
}

// Relevant reports whether a signal is about BlueZ's objects.
//
// The match rule asks the bus for bluetoothd's signals only, but every
// channel registered on a connection receives every signal it gets, so a
// system bus shared with, say, NetworkManager's watch carries theirs too.
// Senders arrive as unique names, which do not say "org.bluez", so this
// looks at what the signal is about instead.
func Relevant(sig *dbus.Signal) bool {
	switch sig.Name {
	case sigInterfacesAdded, sigInterfacesRemoved:
		// Emitted on the object manager at "/"; the object is the first
		// argument.
		if len(sig.Body) == 0 {
			return false
		}
		p, ok := sig.Body[0].(dbus.ObjectPath)
		return ok && underBluez(p)
	case sigPropertiesChanged:
		return underBluez(sig.Path)
	}
	return false
}

func underBluez(p dbus.ObjectPath) bool {
	s := string(p)
	return s == objectRoot || strings.HasPrefix(s, objectRoot+"/")
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
