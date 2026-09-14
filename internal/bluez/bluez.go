// Package bluez reads BlueZ's Bluetooth state, asks it to power the
// adapter and connect devices, and answers its pairing requests as an
// agent -- over the system bus.
//
// It is the small slice a tray menu needs: one adapter, the devices paired
// with it, their batteries, and yes-or-no pairing prompts. Discovery and
// typed PIN entry are left to bluetoothctl or blueman-manager.
//
// This file is the pure half: turning BlueZ's object tree into what a menu
// shows, testable without a bus. client.go and agent.go talk to the bus.
package bluez

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// BusName is bluetoothd's name on the system bus.
const BusName = "org.bluez"

const (
	ifaceAdapter      = BusName + ".Adapter1"
	ifaceDevice       = BusName + ".Device1"
	ifaceBattery      = BusName + ".Battery1"
	ifaceAgentManager = BusName + ".AgentManager1"
	ifaceAgent        = BusName + ".Agent1"
)

// Adapter is a Bluetooth controller.
type Adapter struct {
	Path    dbus.ObjectPath
	Name    string
	Powered bool
}

// Device is a device paired with the adapter.
type Device struct {
	Path    dbus.ObjectPath
	Adapter dbus.ObjectPath
	Address string

	// Name is the user's alias for it, or failing that the name it
	// announces, or failing that its address -- made safe to draw.
	Name string

	// Icon is BlueZ's freedesktop icon name: "audio-headset",
	// "input-keyboard". Empty when the device does not say.
	Icon string

	Paired, Trusted, Connected bool

	// Battery is the charge in percent, or -1 when the device reports none.
	Battery int
}

// State is everything a menu needs at one moment.
type State struct {
	// Adapter is the first adapter by path, or nil when there is none --
	// no controller, or one the kernel failed to bring up.
	Adapter *Adapter

	// Devices are the adapter's paired devices: connected ones first, then
	// by name.
	Devices []Device
}

// ParseManaged turns GetManagedObjects' reply into a State.
//
// It is tolerant: an object whose properties have the wrong types is left
// out, and why is returned alongside a State that is otherwise complete.
// Missing optional properties are not errors.
func ParseManaged(objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant) (State, error) {
	paths := make([]dbus.ObjectPath, 0, len(objects))
	for p := range objects {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })

	var (
		st   State
		errs []error
	)
	for _, p := range paths {
		props, ok := objects[p][ifaceAdapter]
		if !ok {
			continue
		}
		a, err := parseAdapter(p, props)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		st.Adapter = &a
		break
	}
	if st.Adapter == nil {
		return st, errors.Join(errs...)
	}

	for _, p := range paths {
		ifaces := objects[p]
		props, ok := ifaces[ifaceDevice]
		if !ok {
			continue
		}
		d, usable, err := parseDevice(p, props, ifaces[ifaceBattery])
		if err != nil {
			errs = append(errs, err)
		}
		if !usable || !d.Paired || d.Adapter != st.Adapter.Path {
			continue
		}
		st.Devices = append(st.Devices, d)
	}
	sort.SliceStable(st.Devices, func(i, j int) bool {
		a, b := &st.Devices[i], &st.Devices[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		if na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name); na != nb {
			return na < nb
		}
		return a.Address < b.Address
	})
	return st, errors.Join(errs...)
}

func parseAdapter(p dbus.ObjectPath, props map[string]dbus.Variant) (Adapter, error) {
	alias, _, aliasErr := opt[string](props, p, "Alias")
	name, _, nameErr := opt[string](props, p, "Name")
	powered, _, poweredErr := opt[bool](props, p, "Powered")
	if err := errors.Join(aliasErr, nameErr, poweredErr); err != nil {
		return Adapter{}, err
	}
	return Adapter{
		Path:    p,
		Name:    firstNonEmpty(DisplayName(alias), DisplayName(name), path.Base(string(p))),
		Powered: powered,
	}, nil
}

// parseDevice reads one device. usable is false when the device itself is
// malformed; a malformed battery keeps the device, without a battery, and
// is reported in err.
func parseDevice(p dbus.ObjectPath, props, battery map[string]dbus.Variant) (d Device, usable bool, err error) {
	adapter, hasAdapter, adapterErr := opt[dbus.ObjectPath](props, p, "Adapter")
	address, _, addressErr := opt[string](props, p, "Address")
	alias, _, aliasErr := opt[string](props, p, "Alias")
	name, _, nameErr := opt[string](props, p, "Name")
	icon, _, iconErr := opt[string](props, p, "Icon")
	paired, _, pairedErr := opt[bool](props, p, "Paired")
	trusted, _, trustedErr := opt[bool](props, p, "Trusted")
	connected, _, connectedErr := opt[bool](props, p, "Connected")
	if err := errors.Join(adapterErr, addressErr, aliasErr, nameErr, iconErr, pairedErr, trustedErr, connectedErr); err != nil {
		return Device{}, false, err
	}
	if !hasAdapter {
		// BlueZ always sets it; its object paths nest devices under their
		// adapter anyway, which is the next best answer.
		adapter = dbus.ObjectPath(path.Dir(string(p)))
	}
	d = Device{
		Path:      p,
		Adapter:   adapter,
		Address:   address,
		Name:      firstNonEmpty(DisplayName(alias), DisplayName(name), address, path.Base(string(p))),
		Icon:      icon,
		Paired:    paired,
		Trusted:   trusted,
		Connected: connected,
		Battery:   -1,
	}
	if battery != nil {
		pct, ok, batteryErr := opt[byte](battery, p, "Percentage")
		if batteryErr != nil {
			return d, true, batteryErr
		}
		if ok {
			d.Battery = min(int(pct), 100)
		}
	}
	return d, true, nil
}

// DisplayName makes a name a remote device announced safe to draw. It is
// the other device's text, not ours: invalid UTF-8 becomes U+FFFD, control
// characters become spaces, and the ends are trimmed.
func DisplayName(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// opt takes an optional property out of a properties map, checking its
// type. A missing property is not an error; a mistyped one is.
func opt[T any](props map[string]dbus.Variant, p dbus.ObjectPath, key string) (value T, present bool, err error) {
	var zero T
	v, ok := props[key]
	if !ok {
		return zero, false, nil
	}
	got, ok := v.Value().(T)
	if !ok {
		return zero, false, fmt.Errorf("%s: %s has type %s, want %T", p, key, v.Signature(), zero)
	}
	return got, true, nil
}

// autoAuthorize decides whether a service connection from a device may go
// ahead without asking: when the device is paired and the user has marked
// it trusted, as every device paired through this menu is. Anything less,
// or properties that cannot be read, asks.
func autoAuthorize(props map[string]dbus.Variant) bool {
	paired, _, pairedErr := opt[bool](props, "", "Paired")
	trusted, _, trustedErr := opt[bool](props, "", "Trusted")
	return pairedErr == nil && trustedErr == nil && paired && trusted
}

// gone reports whether err says bluetoothd, or the object, is not there.
//
// GDBus-style services answer a call on an unexported path with
// UnknownMethod ("Object does not exist at path"); others say
// UnknownObject or UnknownInterface. ServiceUnknown and NameHasNoOwner mean
// bluetoothd itself is not running.
func gone(err error) bool {
	switch errorName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.NoSuchObject":
		return true
	}
	return false
}

// errorName is the D-Bus error name inside err, or "".
func errorName(err error) string {
	if err == nil {
		return ""
	}
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name
	}
	var dbusErrPtr *dbus.Error
	if errors.As(err, &dbusErrPtr) {
		return dbusErrPtr.Name
	}
	return ""
}
