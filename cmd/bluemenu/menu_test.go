package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/bluez"
	"github.com/mpdroog/osxflow/internal/menu"
)

type fakeActions struct{ calls []string }

func (f *fakeActions) setPower(on bool)             { f.calls = append(f.calls, fmt.Sprintf("power %t", on)) }
func (f *fakeActions) toggleDevice(d *bluez.Device) { f.calls = append(f.calls, "toggle "+d.Name) }
func (f *fakeActions) settings()                    { f.calls = append(f.calls, "settings") }
func (f *fakeActions) answer(accept bool) {
	f.calls = append(f.calls, fmt.Sprintf("answer %t", accept))
}
func (f *fakeActions) reloadDriver() { f.calls = append(f.calls, "reload") }

func (f *fakeActions) click(fn func()) string {
	if fn == nil {
		return "inert"
	}
	f.calls = nil
	fn()
	return strings.Join(f.calls, "; ")
}

func kinds(rows []menu.Row) string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = fmt.Sprintf("%d:%s", rows[i].Kind, rows[i].Label)
	}
	return strings.Join(out, "|")
}

func row(k menu.Kind, label string) string { return fmt.Sprintf("%d:%s", k, label) }

func joined(parts ...string) string { return strings.Join(parts, "|") }

// on is a working adapter with a connected headset reporting its battery
// and a paired keyboard that is not connected.
func on() view {
	return view{
		running: true,
		present: true,
		state: bluez.State{
			Adapter: &bluez.Adapter{Path: "/org/bluez/hci0", Name: "mbp", Powered: true},
			Devices: []bluez.Device{
				{Path: "/org/bluez/hci0/dev_AA", Name: "Razer Kraken", Icon: "audio-headset", Paired: true, Connected: true, Battery: 80},
				{Path: "/org/bluez/hci0/dev_BB", Name: "Keyboard", Icon: "input-keyboard", Paired: true, Battery: -1},
			},
		},
	}
}

func TestPowerOf(t *testing.T) {
	stuck := on()
	stuck.state.Adapter, stuck.state.Devices = nil, nil
	missing := stuck
	missing.present = false
	soft := stuck
	soft.soft = true
	hard := on()
	hard.hard = true
	unpowered := on()
	unpowered.state.Adapter.Powered = false

	for _, tc := range []struct {
		name string
		v    view
		want power
	}{
		{"on", on(), power{on: true, switchable: true}},
		{"adapter powered off", unpowered, power{switchable: true}},
		{"radio blocked in software", soft, power{switchable: true}},
		{"hardware switch", hard, power{note: "Turned off by a hardware switch"}},
		{"controller not answering", stuck, power{note: "Bluetooth adapter not responding", reloadable: true}},
		{"no adapter at all", missing, power{note: "No Bluetooth adapter found"}},
		{"bluetoothd not running", view{}, power{note: "Bluetooth service not running"}},
	} {
		if got := powerOf(&tc.v); got != tc.want {
			t.Errorf("%s: powerOf = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestBuildRowsOn(t *testing.T) {
	v := on()
	f := &fakeActions{}
	rows := buildRows(&v, f)
	if got := kinds(rows); got != joined(
		row(menu.Toggle, "Bluetooth"),
		row(menu.Separator, ""),
		row(menu.Section, "Devices"),
		row(menu.Item, "Razer Kraken"),
		row(menu.Item, "Keyboard"),
		row(menu.Separator, ""),
		row(menu.Action, "Bluetooth Settings…"),
	) {
		t.Fatalf("rows:\n%s", got)
	}
	if !rows[0].On || f.click(rows[0].Click) != "power false" || !rows[0].KeepOpen {
		t.Error("the switch should be on, keep the menu open, and turn Bluetooth off")
	}
	headset, keyboard := &rows[3], &rows[4]
	if !headset.On || headset.Detail != "80%" || f.click(headset.Click) != "toggle Razer Kraken" {
		t.Errorf("headset row: on %t detail %q", headset.On, headset.Detail)
	}
	if keyboard.On || keyboard.Detail != "" || f.click(keyboard.Click) != "toggle Keyboard" {
		t.Errorf("keyboard row: on %t detail %q", keyboard.On, keyboard.Detail)
	}
	if f.click(rows[6].Click) != "settings" {
		t.Error("the settings row should open settings")
	}
}

func TestBuildRowsOffAndBroken(t *testing.T) {
	f := &fakeActions{}
	v := on()
	v.soft = true
	rows := buildRows(&v, f)
	if got := kinds(rows); got != joined(row(menu.Toggle, "Bluetooth"), row(menu.Separator, ""), row(menu.Action, "Bluetooth Settings…")) {
		t.Errorf("off rows:\n%s", got)
	}
	if rows[0].On || f.click(rows[0].Click) != "power true" {
		t.Error("with the radio off the switch should be off and turn it on")
	}

	v = on()
	v.state.Adapter, v.state.Devices = nil, nil
	rows = buildRows(&v, f)
	if got := kinds(rows); !strings.Contains(got, row(menu.Note, "Bluetooth adapter not responding")) {
		t.Errorf("stuck controller rows:\n%s", got)
	}
	if f.click(rows[0].Click) != "inert" {
		t.Error("a switch that can do nothing should be inert")
	}
	if len(rows) < 3 || rows[2].Label != "Reload Bluetooth Driver…" || f.click(rows[2].Click) != "reload" || rows[2].KeepOpen {
		t.Errorf("a stuck controller should offer a driver reload that closes the menu:\n%s", kinds(rows))
	}
	// No controller at all is not something a reload fixes.
	v.present = false
	if got := kinds(buildRows(&v, f)); strings.Contains(got, "Reload") {
		t.Errorf("no adapter at all offers a reload:\n%s", got)
	}
	// Nor is a working one.
	v = on()
	if got := kinds(buildRows(&v, f)); strings.Contains(got, "Reload") {
		t.Errorf("a working adapter offers a reload:\n%s", got)
	}

	v = on()
	v.state.Devices = nil
	if got := kinds(buildRows(&v, f)); !strings.Contains(got, row(menu.Note, "No paired devices")) {
		t.Errorf("no devices rows:\n%s", got)
	}
}

// Every device row acts on its own device.
func TestBuildRowsClosuresCaptureTheirDevice(t *testing.T) {
	v := on()
	f := &fakeActions{}
	rows := buildRows(&v, f)
	for i, want := range []string{"toggle Razer Kraken", "toggle Keyboard"} {
		if got := f.click(rows[3+i].Click); got != want {
			t.Errorf("row %d asked for %q, want %q", 3+i, got, want)
		}
	}
}

func TestPromptRows(t *testing.T) {
	f := &fakeActions{}
	for _, tc := range []struct {
		req     bluez.Request
		want    string
		buttons []string // what each Action row answers, in order
	}{
		{
			bluez.Request{Kind: bluez.Confirm, Passkey: 42917},
			joined(row(menu.Header, "Bluetooth"), row(menu.Note, "Pair if this code matches the device:"),
				row(menu.Header, "042 917"), row(menu.Separator, ""), row(menu.Action, "Pair"), row(menu.Action, "Cancel")),
			[]string{"answer true", "answer false"},
		},
		{
			bluez.Request{Kind: bluez.Authorize},
			joined(row(menu.Header, "Bluetooth"), row(menu.Note, "wants to pair with this computer."),
				row(menu.Separator, ""), row(menu.Action, "Pair"), row(menu.Action, "Cancel")),
			[]string{"answer true", "answer false"},
		},
		{
			bluez.Request{Kind: bluez.AuthorizeService, UUID: "0000110b-0000-1000-8000-00805f9b34fb"},
			joined(row(menu.Header, "Bluetooth"), row(menu.Note, "wants to use audio."),
				row(menu.Separator, ""), row(menu.Action, "Allow"), row(menu.Action, "Deny")),
			[]string{"answer true", "answer false"},
		},
		{
			bluez.Request{Kind: bluez.DisplayPasskey, Passkey: 123456},
			joined(row(menu.Header, "Bluetooth"), row(menu.Note, "Type this code on the device, then Enter:"),
				row(menu.Header, "123 456"), row(menu.Separator, ""), row(menu.Action, "Dismiss")),
			[]string{"answer false"},
		},
		{
			bluez.Request{Kind: bluez.PinCode},
			joined(row(menu.Header, "Bluetooth"), row(menu.Note, "needs a code typed in here, which"),
				row(menu.Note, "this menu cannot take. Use bluetoothctl."), row(menu.Separator, ""), row(menu.Action, "Dismiss")),
			[]string{"answer false"},
		},
	} {
		rows := promptRows(&tc.req, "Razer Kraken", f)
		if got := kinds(rows); got != tc.want {
			t.Errorf("kind %d:\n got %s\nwant %s", tc.req.Kind, got, tc.want)
			continue
		}
		if rows[0].Detail != "Razer Kraken" {
			t.Errorf("kind %d: header detail %q, want the device name", tc.req.Kind, rows[0].Detail)
		}
		var got []string
		for i := range rows {
			if rows[i].Kind == menu.Action {
				got = append(got, f.click(rows[i].Click))
				if rows[i].KeepOpen {
					t.Errorf("kind %d: %q keeps the prompt open", tc.req.Kind, rows[i].Label)
				}
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.buttons, ",") {
			t.Errorf("kind %d: buttons answer %v, want %v", tc.req.Kind, got, tc.buttons)
		}
	}
	for _, k := range []bluez.RequestKind{bluez.Released, bluez.Cancelled} {
		if rows := promptRows(&bluez.Request{Kind: k}, "x", f); rows != nil {
			t.Errorf("kind %d has a prompt: %s", k, kinds(rows))
		}
	}
}

func TestHelpers(t *testing.T) {
	for _, tc := range []struct {
		p    uint32
		want string
	}{{0, "000 000"}, {42917, "042 917"}, {999999, "999 999"}, {1234567, "234 567"}} {
		if got := formatPasskey(tc.p); got != tc.want {
			t.Errorf("formatPasskey(%d) = %q, want %q", tc.p, got, tc.want)
		}
	}
	for _, tc := range []struct{ uuid, want string }{
		{"0000110B-0000-1000-8000-00805F9B34FB", "audio"},
		{"00001124-0000-1000-8000-00805f9b34fb", "input"},
		{"0000111e-0000-1000-8000-00805f9b34fb", "calls"},
		{"12345678-1234-1234-1234-123456789abc", "a service"},
		{"", "a service"},
	} {
		if got := serviceName(tc.uuid); got != tc.want {
			t.Errorf("serviceName(%q) = %q, want %q", tc.uuid, got, tc.want)
		}
	}
	if got := addressFromPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF"); got != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("addressFromPath = %q", got)
	}
	if got := addressFromPath("/org/bluez/hci0"); got != "/org/bluez/hci0" {
		t.Errorf("addressFromPath of a non-device = %q", got)
	}
	v := on()
	if got := deviceName(&v.state, "/org/bluez/hci0/dev_AA"); got != "Razer Kraken" {
		t.Errorf("deviceName of a paired device = %q", got)
	}
	if got := deviceName(&v.state, "/org/bluez/hci0/dev_11_22_33_44_55_66"); got != "11:22:33:44:55:66" {
		t.Errorf("deviceName of an unknown device = %q", got)
	}
	if got := batteryText(&bluez.Device{Battery: 50}); got != "" {
		t.Errorf("battery of a disconnected device = %q, want none", got)
	}
}
