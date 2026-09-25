package netmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// BusName is NetworkManager's name on the system bus.
const BusName = "org.freedesktop.NetworkManager"

const (
	rootPath     = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	settingsPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")

	ifaceRoot       = BusName
	ifaceDevice     = BusName + ".Device"
	ifaceWireless   = BusName + ".Device.Wireless"
	ifaceAP         = BusName + ".AccessPoint"
	ifaceSettings   = BusName + ".Settings"
	ifaceConnection = BusName + ".Settings.Connection"
	ifaceActive     = BusName + ".Connection.Active"
	ifaceIP4        = BusName + ".IP4Config"

	propsGetAll = "org.freedesktop.DBus.Properties.GetAll"
	propsSet    = "org.freedesktop.DBus.Properties.Set"
)

// deviceTypeWifi is NM_DEVICE_TYPE_WIFI.
const deviceTypeWifi = 2

// noObject is the path NetworkManager takes to mean "none": no particular
// device, no particular access point.
const noObject = dbus.ObjectPath("/")

// ErrNoState means NetworkManager could not be read at all, as opposed to
// one of its objects being unreadable. The State that comes with it is
// empty and describes nothing; keep the previous one.
var ErrNoState = errors.New("NetworkManager state unavailable")

// Client talks to NetworkManager.
type Client struct {
	conn *dbus.Conn
}

// New returns a client on conn, which should be the system bus.
func New(conn *dbus.Conn) *Client {
	return &Client{conn: conn}
}

// Snapshot reads everything a menu needs.
//
// A failure to read NetworkManager itself returns ErrNoState. A failure
// with one object -- a malformed profile, say -- leaves that object out and
// is returned alongside a State that is otherwise complete. An object that
// disappeared between being listed and being read is not a failure: access
// points go out of range and connections finish going down all the time.
func (c *Client) Snapshot(ctx context.Context) (State, error) {
	root, err := c.getAll(ctx, rootPath, ifaceRoot)
	if err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrNoState, err)
	}
	enabled, enabledErr := value[bool](root, rootPath, "WirelessEnabled")
	hardware, hardwareErr := value[bool](root, rootPath, "WirelessHardwareEnabled")
	devices, devicesErr := value[[]dbus.ObjectPath](root, rootPath, "Devices")
	activePaths, activeListErr := value[[]dbus.ObjectPath](root, rootPath, "ActiveConnections")

	st := State{WifiEnabled: enabled, WifiHardware: hardware}
	aps, activeAP, apErr := c.accessPoints(ctx, devices, &st)
	saved, savedErr := c.saved(ctx)
	active, activeErr := c.active(ctx, activePaths)

	// PrimaryConnection is read without insisting on it: NetworkManager
	// older than 0.9.10 has no such property, and for the one thing it is
	// wanted for -- an address to show -- a missing property and nothing
	// routed come to the same empty answer.
	primary, _ := value[dbus.ObjectPath](root, rootPath, "PrimaryConnection")
	address, addressErr := c.address(ctx, primary)

	st.Networks = Networks(aps, activeAP, saved, active)
	st.VPNs = VPNs(saved, active)
	st.Wired = Wired(active)
	st.Address = address
	return st, errors.Join(enabledErr, hardwareErr, devicesErr, activeListErr,
		apErr, savedErr, activeErr, addressErr)
}

// accessPoints finds the first Wi-Fi device, records it in st, and reads
// the access points it can hear and the one it is associated with.
func (c *Client) accessPoints(ctx context.Context, devices []dbus.ObjectPath, st *State) ([]AccessPoint, dbus.ObjectPath, error) {
	var errs []error
	for _, dev := range devices {
		props, err := c.getAll(ctx, dev, ifaceDevice)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		kind, err := value[uint32](props, dev, "DeviceType")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if kind != deviceTypeWifi {
			continue
		}

		wireless, err := c.getAll(ctx, dev, ifaceWireless)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		paths, err := value[[]dbus.ObjectPath](wireless, dev, "AccessPoints")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		activeAP, err := value[dbus.ObjectPath](wireless, dev, "ActiveAccessPoint")
		if err != nil {
			// The network's strength falls back to its strongest access
			// point, which is wrong only while connected to a weaker one.
			errs = append(errs, err)
		}
		st.WifiDevice = dev

		aps := make([]AccessPoint, 0, len(paths))
		for _, p := range paths {
			ap, apErr := c.accessPoint(ctx, p)
			switch {
			case gone(apErr):
			case apErr != nil:
				errs = append(errs, apErr)
			default:
				aps = append(aps, ap)
			}
		}
		// The first adapter only: a menu with two lists of the same
		// networks helps nobody, and this machine has one.
		return aps, activeAP, errors.Join(errs...)
	}
	return nil, "", errors.Join(errs...)
}

func (c *Client) accessPoint(ctx context.Context, path dbus.ObjectPath) (AccessPoint, error) {
	props, err := c.getAll(ctx, path, ifaceAP)
	if err != nil {
		return AccessPoint{}, err
	}
	ssid, ssidErr := value[[]byte](props, path, "Ssid")
	strength, strengthErr := value[uint8](props, path, "Strength")
	flags, flagsErr := value[uint32](props, path, "Flags")
	wpa, wpaErr := value[uint32](props, path, "WpaFlags")
	rsn, rsnErr := value[uint32](props, path, "RsnFlags")
	if err := errors.Join(ssidErr, strengthErr, flagsErr, wpaErr, rsnErr); err != nil {
		return AccessPoint{}, err
	}
	return AccessPoint{Path: path, SSID: ssid, Strength: strength, Flags: flags, WPAFlags: wpa, RSNFlags: rsn}, nil
}

// saved reads every saved profile's id, type and, for Wi-Fi, SSID.
// GetSettings never includes secrets, which is as it should be: nothing
// here needs them.
func (c *Client) saved(ctx context.Context) ([]Saved, error) {
	var paths []dbus.ObjectPath
	if err := c.conn.Object(BusName, settingsPath).
		CallWithContext(ctx, ifaceSettings+".ListConnections", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("listing saved connections: %w", err)
	}
	var errs []error
	out := make([]Saved, 0, len(paths))
	for _, p := range paths {
		var settings map[string]map[string]dbus.Variant
		err := c.conn.Object(BusName, p).
			CallWithContext(ctx, ifaceConnection+".GetSettings", 0).Store(&settings)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("reading saved connection %s: %w", p, err))
			continue
		}
		s, err := parseSaved(p, settings)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	return out, errors.Join(errs...)
}

// parseSaved picks out of a profile's settings what a menu needs.
func parseSaved(path dbus.ObjectPath, settings map[string]map[string]dbus.Variant) (Saved, error) {
	conn := settings["connection"]
	id, idErr := value[string](conn, path, "connection.id")
	typ, typeErr := value[string](conn, path, "connection.type")
	if err := errors.Join(idErr, typeErr); err != nil {
		return Saved{}, err
	}
	s := Saved{Path: path, ID: id, Type: typ}
	if typ == TypeWifi {
		ssid, err := value[[]byte](settings[TypeWifi], path, "802-11-wireless.ssid")
		if err != nil {
			return Saved{}, err
		}
		s.SSID = ssid
	}
	return s, nil
}

func (c *Client) active(ctx context.Context, paths []dbus.ObjectPath) ([]Active, error) {
	var errs []error
	out := make([]Active, 0, len(paths))
	for _, p := range paths {
		props, err := c.getAll(ctx, p, ifaceActive)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		conn, connErr := value[dbus.ObjectPath](props, p, "Connection")
		typ, typeErr := value[string](props, p, "Type")
		state, stateErr := value[uint32](props, p, "State")
		if joined := errors.Join(connErr, typeErr, stateErr); joined != nil {
			errs = append(errs, joined)
			continue
		}
		out = append(out, Active{Path: p, Connection: conn, Type: typ, State: ActiveState(state)})
	}
	return out, errors.Join(errs...)
}

// address is the IPv4 address of the connection carrying the traffic.
//
// NetworkManager's PrimaryConnection is the one it is actually routing
// through, which is the address a user means by "my IP" when the machine
// is on Ethernet and Wi-Fi at once. Nothing routed is not a failure: a
// laptop between networks has no primary connection, and an empty string
// is the honest answer.
//
// The address lives two objects away -- the active connection names an
// IP4Config, which holds the addresses -- and either hop can be a path
// that has just gone away, which is ordinary rather than an error.
func (c *Client) address(ctx context.Context, primary dbus.ObjectPath) (string, error) {
	if primary == "" || primary == noObject {
		return "", nil
	}
	props, err := c.getAll(ctx, primary, ifaceActive)
	if gone(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	ip4, err := value[dbus.ObjectPath](props, primary, "Ip4Config")
	if err != nil {
		return "", err
	}
	if ip4 == "" || ip4 == noObject {
		return "", nil
	}
	ipProps, err := c.getAll(ctx, ip4, ifaceIP4)
	if gone(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// AddressData is aa{sv}: one dictionary per address, each with an
	// "address" and a "prefix". The first is the one NetworkManager
	// considers primary, and an interface with several is rare enough
	// that showing the first is right and showing all of them is clutter.
	data, err := value[[]map[string]dbus.Variant](ipProps, ip4, "AddressData")
	if err != nil {
		return "", err
	}
	for _, d := range data {
		v, ok := d["address"]
		if !ok {
			continue
		}
		if addr, ok := v.Value().(string); ok && addr != "" {
			return addr, nil
		}
	}
	return "", nil
}

// SetWifi turns the Wi-Fi radio on or off.
func (c *Client) SetWifi(ctx context.Context, on bool) error {
	err := c.conn.Object(BusName, rootPath).
		CallWithContext(ctx, propsSet, 0, ifaceRoot, "WirelessEnabled", dbus.MakeVariant(on)).Err
	if err != nil {
		return fmt.Errorf("turning Wi-Fi %s: %w", onOff(on), err)
	}
	return nil
}

// Activate brings a saved profile up. device is the adapter to use, or ""
// to let NetworkManager choose, which is what a VPN wants.
func (c *Client) Activate(ctx context.Context, profile, device dbus.ObjectPath) error {
	if device == "" {
		device = noObject
	}
	var active dbus.ObjectPath
	if err := c.conn.Object(BusName, rootPath).
		CallWithContext(ctx, ifaceRoot+".ActivateConnection", 0, profile, device, noObject).
		Store(&active); err != nil {
		return fmt.Errorf("activating %s: %w", profile, err)
	}
	return nil
}

// Deactivate takes an active connection down.
func (c *Client) Deactivate(ctx context.Context, active dbus.ObjectPath) error {
	if err := c.conn.Object(BusName, rootPath).
		CallWithContext(ctx, ifaceRoot+".DeactivateConnection", 0, active).Err; err != nil {
		return fmt.Errorf("deactivating %s: %w", active, err)
	}
	return nil
}

// JoinOpen saves a profile for an open network and joins it. The profile
// is the least NetworkManager accepts -- the SSID -- and it fills in the
// rest, as it does for nmcli.
func (c *Client) JoinOpen(ctx context.Context, ssid []byte, device, ap dbus.ObjectPath) error {
	if ap == "" {
		ap = noObject
	}
	settings := map[string]map[string]dbus.Variant{
		TypeWifi: {"ssid": dbus.MakeVariant(ssid)},
	}
	var profile, active dbus.ObjectPath
	if err := c.conn.Object(BusName, rootPath).
		CallWithContext(ctx, ifaceRoot+".AddAndActivateConnection", 0, settings, device, ap).
		Store(&profile, &active); err != nil {
		return fmt.Errorf("joining %q: %w", DisplayName(ssid), err)
	}
	return nil
}

// RequestScan asks a Wi-Fi device to look for networks now rather than at
// its next periodic scan. NetworkManager refuses one that follows another
// too closely, and says so in the error.
func (c *Client) RequestScan(ctx context.Context, device dbus.ObjectPath) error {
	if err := c.conn.Object(BusName, device).
		CallWithContext(ctx, ifaceWireless+".RequestScan", 0, map[string]dbus.Variant{}).Err; err != nil {
		return fmt.Errorf("scanning on %s: %w", device, err)
	}
	return nil
}

// Watch reports that something in NetworkManager changed.
//
// It says nothing about what: every signal NetworkManager sends is folded
// into one receive, and a burst of them -- a scan finishing updates every
// access point -- becomes one or two. Read a Snapshot after it fires. The
// channel closes when the connection does.
func (c *Client) Watch() (<-chan struct{}, error) {
	if err := c.conn.AddMatchSignal(dbus.WithMatchSender(BusName)); err != nil {
		return nil, fmt.Errorf("watching NetworkManager: %w", err)
	}
	signals := make(chan *dbus.Signal, 64)
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

// value takes one property out of a GetAll reply, checking its type. key
// only has to be unique within props; what it says in the error is what a
// person reading the log will search for.
func value[T any](props map[string]dbus.Variant, path dbus.ObjectPath, key string) (T, error) {
	var zero T
	name := key
	if i := lastDot(key); i >= 0 {
		key = key[i+1:]
	}
	v, ok := props[key]
	if !ok {
		return zero, fmt.Errorf("%s: no %s", path, name)
	}
	got, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("%s: %s has type %s, want %T", path, name, v.Signature(), zero)
	}
	return got, nil
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// gone reports whether err says the object is no longer there.
//
// GDBus, which NetworkManager uses, answers a call on a path it no longer
// exports with UnknownMethod ("no such interface on object"); other
// implementations say UnknownObject or UnknownInterface. All mean the same
// here: the thing was listed a moment ago and has since gone away.
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

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
