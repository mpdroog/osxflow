package main

// What the menu and the pairing prompt list, and what each row does, kept
// free of X11, BlueZ and rfkill so they are tested with none of them.

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/bluez"
	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/menu"
)

// view is everything the menu, the prompt and the icon show.
type view struct {
	// running reports whether bluetoothd answered the last time it was
	// asked.
	running bool
	state   bluez.State

	// The Bluetooth radio's kill switch: whether there is one, and whether
	// software or a hardware switch has it off.
	present, soft, hard bool
}

// actions is what the rows do.
type actions interface {
	setPower(on bool)
	toggleDevice(d *bluez.Device)
	settings()
	answer(accept bool)
	reloadDriver()
}

// power is what the Bluetooth switch shows and whether it can be flipped.
type power struct {
	on         bool
	switchable bool
	// note explains a switch that cannot be flipped, or is "" when it can.
	note string
	// reloadable marks a controller that is there but never answered,
	// which reloading its driver brings back.
	reloadable bool
}

func powerOf(v *view) power {
	adapter := v.state.Adapter
	switch {
	case !v.running:
		return power{note: "Bluetooth service not running"}
	case v.hard:
		return power{note: "Turned off by a hardware switch"}
	case v.soft:
		// Off in software: the switch turns the radio back on.
		return power{switchable: true}
	case adapter == nil && v.present:
		// The radio is on but the controller never answered. On this
		// MacBook that happens on some boots; reloading hci_uart or
		// restarting brings it back.
		return power{note: "Bluetooth adapter not responding", reloadable: true}
	case adapter == nil:
		return power{note: "No Bluetooth adapter found"}
	}
	return power{on: adapter.Powered, switchable: true}
}

func buildRows(v *view, act actions) []menu.Row {
	p := powerOf(v)
	toggle := menu.Row{Kind: menu.Toggle, Label: "Bluetooth", On: p.on, KeepOpen: true}
	if p.switchable {
		on := p.on
		toggle.Click = func() { act.setPower(!on) }
	}
	rows := make([]menu.Row, 0, 6+len(v.state.Devices))
	rows = append(rows, toggle)
	if p.note != "" {
		rows = append(rows, menu.Row{Kind: menu.Note, Label: p.note})
	}
	if p.reloadable {
		rows = append(rows, menu.Row{Kind: menu.Action, Label: "Reload Bluetooth Driver…", Click: act.reloadDriver})
	}

	if p.on {
		rows = append(rows, menu.Row{Kind: menu.Separator}, menu.Row{Kind: menu.Section, Label: "Devices"})
		if len(v.state.Devices) == 0 {
			rows = append(rows, menu.Row{Kind: menu.Note, Label: "No paired devices"})
		}
		for i := range v.state.Devices {
			d := v.state.Devices[i]
			rows = append(rows, menu.Row{
				Kind:   menu.Item,
				Label:  d.Name,
				Detail: batteryText(&d),
				On:     d.Connected,
				Glyph:  deviceGlyph(d.Icon),
				// Connecting takes a few seconds; the badge lights when it
				// is done, which the menu staying open lets the user see.
				KeepOpen: true,
				Click:    func() { act.toggleDevice(&d) },
			})
		}
	}

	return append(rows,
		menu.Row{Kind: menu.Separator},
		menu.Row{Kind: menu.Action, Label: "Bluetooth Settings…", Click: act.settings})
}

// batteryText is a connected device's battery, when it reports one.
func batteryText(d *bluez.Device) string {
	if !d.Connected || d.Battery < 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", d.Battery)
}

// deviceGlyph picks a device's badge from the icon name BlueZ gives it.
func deviceGlyph(icon string) menu.Glyph {
	if strings.HasPrefix(icon, "audio-") {
		return menu.Symbol(glyph.Headphones)
	}
	return menu.Symbol(glyph.Bluetooth)
}

// promptRows is the pairing prompt for an agent request: what is being
// asked, and the buttons that answer it. Clicking away from the prompt
// answers no.
func promptRows(req *bluez.Request, name string, act actions) []menu.Row {
	yes := func(label string) menu.Row {
		return menu.Row{Kind: menu.Action, Label: label, Click: func() { act.answer(true) }}
	}
	no := func(label string) menu.Row {
		return menu.Row{Kind: menu.Action, Label: label, Click: func() { act.answer(false) }}
	}
	header := menu.Row{Kind: menu.Header, Label: "Bluetooth", Detail: name}

	switch req.Kind {
	case bluez.Confirm:
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "Pair if this code matches the device:"},
			{Kind: menu.Header, Label: formatPasskey(req.Passkey)},
			{Kind: menu.Separator},
			yes("Pair"), no("Cancel"),
		}
	case bluez.Authorize:
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "wants to pair with this computer."},
			{Kind: menu.Separator},
			yes("Pair"), no("Cancel"),
		}
	case bluez.AuthorizeService:
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "wants to use " + serviceName(req.UUID) + "."},
			{Kind: menu.Separator},
			yes("Allow"), no("Deny"),
		}
	case bluez.DisplayPasskey:
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "Type this code on the device, then Enter:"},
			{Kind: menu.Header, Label: formatPasskey(req.Passkey)},
			{Kind: menu.Separator},
			no("Dismiss"),
		}
	case bluez.DisplayPinCode:
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "Type this PIN on the device:"},
			{Kind: menu.Header, Label: req.PinCode},
			{Kind: menu.Separator},
			no("Dismiss"),
		}
	case bluez.PinCode, bluez.Passkey:
		// Typing a code in is not something this menu can do; BlueZ has
		// already been told no.
		return []menu.Row{
			header,
			{Kind: menu.Note, Label: "needs a code typed in here, which"},
			{Kind: menu.Note, Label: "this menu cannot take. Use bluetoothctl."},
			{Kind: menu.Separator},
			no("Dismiss"),
		}
	case bluez.Released, bluez.Cancelled:
	}
	return nil
}

// formatPasskey groups a six-digit passkey in threes, as phones show it.
func formatPasskey(p uint32) string {
	s := fmt.Sprintf("%06d", p%1000000)
	return s[:3] + " " + s[3:]
}

// serviceName says in words what a Bluetooth profile is for, for the few a
// person is likely to be asked about. Everything else is "a service".
func serviceName(uuid string) string {
	short := strings.ToLower(uuid)
	if len(short) >= 8 && strings.HasSuffix(short, "-0000-1000-8000-00805f9b34fb") {
		short = short[4:8]
	}
	switch short {
	case "110a", "110b", "110d":
		return "audio"
	case "1108", "1112", "111e", "111f":
		return "calls"
	case "110c", "110e", "110f":
		return "media controls"
	case "1124", "1812":
		return "input"
	case "1105", "1106":
		return "file transfer"
	case "1115", "1116":
		return "networking"
	}
	return "a service"
}

// addressFromPath is a device's address from its BlueZ object path
// (/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF), for naming a device that is not
// paired yet and so not in the state.
func addressFromPath(p dbus.ObjectPath) string {
	s := string(p)
	i := strings.LastIndex(s, "/dev_")
	if i < 0 {
		return s
	}
	return strings.ReplaceAll(s[i+len("/dev_"):], "_", ":")
}

func deviceName(st *bluez.State, p dbus.ObjectPath) string {
	for i := range st.Devices {
		if st.Devices[i].Path == p {
			return st.Devices[i].Name
		}
	}
	return addressFromPath(p)
}
