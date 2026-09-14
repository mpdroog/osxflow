package upower

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/godbus/dbus/v5"
)

// BusName is UPower's name on the system bus.
const BusName = "org.freedesktop.UPower"

const (
	rootPath    = dbus.ObjectPath("/org/freedesktop/UPower")
	displayPath = dbus.ObjectPath("/org/freedesktop/UPower/devices/DisplayDevice")

	ifaceRoot   = BusName
	ifaceDevice = BusName + ".Device"

	propsGetAll = "org.freedesktop.DBus.Properties.GetAll"
)

// deviceTypeBattery is UP_DEVICE_KIND_BATTERY.
const deviceTypeBattery = 2

// ErrNoState means UPower could not be read at all. The Battery that comes
// with it describes nothing; keep the previous one.
var ErrNoState = errors.New("UPower state unavailable")

// ErrNoBattery means UPower answers but reports no battery: a desktop
// machine, or a laptop with its battery removed.
var ErrNoBattery = errors.New("no battery")

// Client talks to UPower.
type Client struct {
	conn *dbus.Conn
}

// New returns a client on conn, which should be the system bus.
func New(conn *dbus.Conn) *Client {
	return &Client{conn: conn}
}

// Snapshot reads the battery.
//
// Charge, state and the time estimates come from UPower's display device,
// which is its summary of every battery that powers the machine -- the one
// thing a panel icon should show. Health comes from the first real battery,
// since the display device has no design capacity or cycle count of its
// own.
//
// A failure to read the display device returns ErrNoState, and a display
// device that is no battery returns ErrNoBattery. A failure with anything
// else -- the health, OnBattery -- leaves that part at its zero value and
// is returned alongside a Battery that is otherwise complete.
func (c *Client) Snapshot(ctx context.Context) (Battery, error) {
	b := Battery{Cycles: -1}
	display, err := c.getAll(ctx, displayPath, ifaceDevice)
	if err != nil {
		return b, fmt.Errorf("%w: %w", ErrNoState, err)
	}
	present, presentErr := value[bool](display, displayPath, "IsPresent")
	kind, kindErr := value[uint32](display, displayPath, "Type")
	if joined := errors.Join(presentErr, kindErr); joined != nil {
		return b, fmt.Errorf("%w: %w", ErrNoState, joined)
	}
	if !present || kind != deviceTypeBattery {
		return b, ErrNoBattery
	}

	percentage, percentageErr := value[float64](display, displayPath, "Percentage")
	state, stateErr := value[uint32](display, displayPath, "State")
	toEmpty, toEmptyErr := value[int64](display, displayPath, "TimeToEmpty")
	toFull, toFullErr := value[int64](display, displayPath, "TimeToFull")
	b.Percentage = math.Max(0, math.Min(100, percentage))
	b.State = State(state)
	b.TimeToEmpty, b.TimeToFull = seconds(toEmpty), seconds(toFull)

	onBattery, onBatteryErr := c.onBattery(ctx)
	b.OnBattery = onBattery
	healthErr := c.health(ctx, &b)
	return b, errors.Join(percentageErr, stateErr, toEmptyErr, toFullErr, onBatteryErr, healthErr)
}

func (c *Client) onBattery(ctx context.Context) (bool, error) {
	root, err := c.getAll(ctx, rootPath, ifaceRoot)
	if err != nil {
		return false, err
	}
	return value[bool](root, rootPath, "OnBattery")
}

// health fills in capacity and cycles from the first battery that powers
// the machine. A mouse or a phone is a battery UPower knows about too, but
// PowerSupply is false for those.
func (c *Client) health(ctx context.Context, b *Battery) error {
	var paths []dbus.ObjectPath
	if err := c.conn.Object(BusName, rootPath).
		CallWithContext(ctx, ifaceRoot+".EnumerateDevices", 0).Store(&paths); err != nil {
		return fmt.Errorf("listing power devices: %w", err)
	}
	var errs []error
	for _, p := range paths {
		props, err := c.getAll(ctx, p, ifaceDevice)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		kind, kindErr := value[uint32](props, p, "Type")
		supply, supplyErr := value[bool](props, p, "PowerSupply")
		if joined := errors.Join(kindErr, supplyErr); joined != nil {
			errs = append(errs, joined)
			continue
		}
		if kind != deviceTypeBattery || !supply {
			continue
		}
		capacity, capacityErr := value[float64](props, p, "Capacity")
		cycles, cyclesErr := value[int32](props, p, "ChargeCycles")
		if capacity > 0 {
			b.Capacity = capacity
		}
		// UPower says -1 when the kernel does not report a count, and some
		// firmware reports 0 for the same; neither is a real count.
		if cycles > 0 {
			b.Cycles = int(cycles)
		}
		errs = append(errs, capacityErr, cyclesErr)
		return errors.Join(errs...)
	}
	return errors.Join(errs...)
}

// Watch reports that something in UPower changed.
//
// Like netmgr.Watch it says nothing about what: every signal UPower sends
// is folded into one receive. Read a Snapshot after it fires. The channel
// closes when the connection does.
func (c *Client) Watch() (<-chan struct{}, error) {
	if err := c.conn.AddMatchSignal(dbus.WithMatchSender(BusName)); err != nil {
		return nil, fmt.Errorf("watching UPower: %w", err)
	}
	signals := make(chan *dbus.Signal, 16)
	c.conn.Signal(signals)
	changed := make(chan struct{}, 1)
	go func() {
		defer close(changed)
		for range signals {
			select {
			case changed <- struct{}{}:
			default:
				// A change is already waiting to be read, and it covers
				// this one too.
			}
		}
	}()
	return changed, nil
}

func (c *Client) getAll(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var props map[string]dbus.Variant
	if err := c.conn.Object(BusName, path).CallWithContext(ctx, propsGetAll, 0, iface).Store(&props); err != nil {
		return nil, fmt.Errorf("reading %s on %s: %w", iface, path, err)
	}
	return props, nil
}

// maxSeconds is the most seconds a time.Duration can hold.
const maxSeconds = math.MaxInt64 / int64(time.Second)

// seconds turns one of UPower's estimates into a duration. Zero and below
// mean no estimate.
func seconds(s int64) time.Duration {
	switch {
	case s <= 0:
		return 0
	case s > maxSeconds:
		return time.Duration(maxSeconds) * time.Second
	}
	return time.Duration(s) * time.Second
}

// value takes one property out of a GetAll reply, checking its type.
func value[T any](props map[string]dbus.Variant, path dbus.ObjectPath, key string) (T, error) {
	var zero T
	v, ok := props[key]
	if !ok {
		return zero, fmt.Errorf("%s: no %s", path, key)
	}
	got, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("%s: %s has type %s, want %T", path, key, v.Signature(), zero)
	}
	return got, nil
}

// gone reports whether err says the object is no longer there: a device
// unplugged between being listed and being read, which is ordinary.
func gone(err error) bool {
	if err == nil {
		return false
	}
	var name string
	var dbusErr dbus.Error
	var dbusErrPtr *dbus.Error
	switch {
	case errors.As(err, &dbusErr):
		name = dbusErr.Name
	case errors.As(err, &dbusErrPtr):
		name = dbusErrPtr.Name
	default:
		return false
	}
	switch name {
	case "org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.NoSuchObject":
		return true
	}
	return false
}
