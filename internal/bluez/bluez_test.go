package bluez

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

type objects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func sampleObjects() objects {
	return objects{
		"/org/bluez": {ifaceAgentManager: {}},
		"/org/bluez/hci0": {ifaceAdapter: {
			"Alias": v("mbp"), "Name": v("BlueZ 5.72"), "Powered": v(true),
		}},
		"/org/bluez/hci1": {ifaceAdapter: {"Alias": v("dongle"), "Powered": v(false)}},
		"/org/bluez/hci0/dev_AA_AA_AA_AA_AA_AA": {
			ifaceDevice: {
				"Adapter": v(dbus.ObjectPath("/org/bluez/hci0")), "Address": v("AA:AA:AA:AA:AA:AA"),
				"Alias": v("Kraken"), "Icon": v("audio-headset"),
				"Paired": v(true), "Trusted": v(true), "Connected": v(true),
			},
			ifaceBattery: {"Percentage": v(byte(80))},
		},
		"/org/bluez/hci0/dev_BB_BB_BB_BB_BB_BB": {ifaceDevice: {
			"Adapter": v(dbus.ObjectPath("/org/bluez/hci0")), "Address": v("BB:BB:BB:BB:BB:BB"),
			"Name": v("Magic Keyboard"), "Icon": v("input-keyboard"), "Paired": v(true),
		}},
		"/org/bluez/hci0/dev_CC_CC_CC_CC_CC_CC": {ifaceDevice: {
			"Adapter": v(dbus.ObjectPath("/org/bluez/hci0")), "Address": v("CC:CC:CC:CC:CC:CC"),
			"Alias": v("Neighbour's phone"), "Paired": v(false),
		}},
		"/org/bluez/hci1/dev_DD_DD_DD_DD_DD_DD": {ifaceDevice: {
			"Adapter": v(dbus.ObjectPath("/org/bluez/hci1")), "Address": v("DD:DD:DD:DD:DD:DD"),
			"Alias": v("On the other adapter"), "Paired": v(true),
		}},
	}
}

func TestParseManaged(t *testing.T) {
	st, err := ParseManaged(sampleObjects())
	if err != nil {
		t.Fatal(err)
	}
	if st.Adapter == nil || st.Adapter.Path != "/org/bluez/hci0" || st.Adapter.Name != "mbp" || !st.Adapter.Powered {
		t.Fatalf("adapter = %+v, want hci0 'mbp' powered", st.Adapter)
	}
	want := []struct {
		name      string
		connected bool
		battery   int
		icon      string
	}{
		{"Kraken", true, 80, "audio-headset"},
		{"Magic Keyboard", false, -1, "input-keyboard"},
	}
	if len(st.Devices) != len(want) {
		t.Fatalf("devices = %+v, want %d", st.Devices, len(want))
	}
	for i, w := range want {
		d := st.Devices[i]
		if d.Name != w.name || d.Connected != w.connected || d.Battery != w.battery || d.Icon != w.icon || !d.Paired {
			t.Errorf("device %d = %+v, want %+v", i, d, w)
		}
	}
}

func TestParseManagedNoAdapter(t *testing.T) {
	st, err := ParseManaged(objects{"/org/bluez": {ifaceAgentManager: {}}})
	if err != nil || st.Adapter != nil || st.Devices != nil {
		t.Errorf("no adapter: %+v, %v", st, err)
	}
	st, err = ParseManaged(nil)
	if err != nil || st.Adapter != nil {
		t.Errorf("nil objects: %+v, %v", st, err)
	}
}

func TestParseManagedMalformed(t *testing.T) {
	objs := sampleObjects()
	// A mistyped first adapter is skipped for the next.
	objs["/org/bluez/hci0"][ifaceAdapter]["Powered"] = v("yes")
	st, err := ParseManaged(objs)
	if err == nil || !strings.Contains(err.Error(), "Powered") {
		t.Errorf("error = %v, want one naming Powered", err)
	}
	if st.Adapter == nil || st.Adapter.Path != "/org/bluez/hci1" {
		t.Fatalf("adapter = %+v, want the second one", st.Adapter)
	}
	if len(st.Devices) != 1 || st.Devices[0].Name != "On the other adapter" {
		t.Errorf("devices = %+v, want hci1's", st.Devices)
	}

	objs = sampleObjects()
	objs["/org/bluez/hci0/dev_BB_BB_BB_BB_BB_BB"][ifaceDevice]["Paired"] = v(int32(1))
	objs["/org/bluez/hci0/dev_AA_AA_AA_AA_AA_AA"][ifaceBattery]["Percentage"] = v("80%")
	st, err = ParseManaged(objs)
	if err == nil {
		t.Fatal("no error for a mistyped device and battery")
	}
	// The mistyped device goes; the one with a mistyped battery stays,
	// without a battery.
	if len(st.Devices) != 1 || st.Devices[0].Name != "Kraken" || st.Devices[0].Battery != -1 {
		t.Errorf("devices = %+v, want Kraken without a battery", st.Devices)
	}
}

func TestParseManagedFallbacks(t *testing.T) {
	st, err := ParseManaged(objects{
		"/org/bluez/hci0": {ifaceAdapter: {}},
		// No Adapter property, no names: the path and the address stand in.
		"/org/bluez/hci0/dev_EE_EE_EE_EE_EE_EE": {
			ifaceDevice:  {"Address": v("EE:EE:EE:EE:EE:EE"), "Paired": v(true), "Alias": v(" \t")},
			ifaceBattery: {"Percentage": v(byte(250))},
		},
		"/org/bluez/hci0/dev_FF_FF_FF_FF_FF_FF": {ifaceDevice: {"Alias": v("zed"), "Paired": v(true), "Connected": v(false)}},
		"/org/bluez/hci0/dev_11_11_11_11_11_11": {ifaceDevice: {"Alias": v("Alpha"), "Paired": v(true)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Adapter.Name != "hci0" || st.Adapter.Powered {
		t.Errorf("adapter = %+v, want hci0, off", st.Adapter)
	}
	got := make([]string, len(st.Devices))
	for i := range st.Devices {
		got[i] = fmt.Sprintf("%s/%d", st.Devices[i].Name, st.Devices[i].Battery)
	}
	if strings.Join(got, " ") != "Alpha/-1 EE:EE:EE:EE:EE:EE/100 zed/-1" {
		t.Errorf("devices = %v", got)
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"Kraken":         "Kraken",
		"  padded\n":     "padded",
		"two\nlines":     "two lines",
		"bad\xffbyte":    "bad�byte",
		"\x00\x01":       "",
		"Café Écouteurs": "Café Écouteurs",
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzDisplayName(f *testing.F) {
	for _, s := range []string{"Kraken", "", "\xff\x00", "a\nb", "  x  "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := DisplayName(s)
		if !utf8.ValidString(got) {
			t.Fatalf("DisplayName(%q) = %q, not valid UTF-8", s, got)
		}
		if strings.IndexFunc(got, unicode.IsControl) >= 0 {
			t.Fatalf("DisplayName(%q) = %q, contains a control character", s, got)
		}
		if got != strings.TrimSpace(got) {
			t.Fatalf("DisplayName(%q) = %q, not trimmed", s, got)
		}
	})
}

func TestAutoAuthorize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		props map[string]dbus.Variant
		want  bool
	}{
		{"paired and trusted", map[string]dbus.Variant{"Paired": v(true), "Trusted": v(true)}, true},
		{"paired only", map[string]dbus.Variant{"Paired": v(true), "Trusted": v(false)}, false},
		{"trusted only", map[string]dbus.Variant{"Paired": v(false), "Trusted": v(true)}, false},
		{"missing", map[string]dbus.Variant{}, false},
		{"mistyped", map[string]dbus.Variant{"Paired": v("true"), "Trusted": v(true)}, false},
		{"nil", nil, false},
	} {
		if got := autoAuthorize(tc.props); got != tc.want {
			t.Errorf("%s: autoAuthorize = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestRelevant(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  dbus.Signal
		want bool
	}{
		{"device added", dbus.Signal{Name: sigInterfacesAdded, Path: "/", Body: []any{dbus.ObjectPath("/org/bluez/hci0/dev_AA")}}, true},
		{"adapter removed", dbus.Signal{Name: sigInterfacesRemoved, Path: "/", Body: []any{dbus.ObjectPath("/org/bluez/hci0")}}, true},
		{"property", dbus.Signal{Name: sigPropertiesChanged, Path: "/org/bluez/hci0"}, true},
		{"NetworkManager property", dbus.Signal{Name: sigPropertiesChanged, Path: "/org/freedesktop/NetworkManager"}, false},
		{"lookalike path", dbus.Signal{Name: sigPropertiesChanged, Path: "/org/bluezzz"}, false},
		{"someone else's object added", dbus.Signal{Name: sigInterfacesAdded, Path: "/", Body: []any{dbus.ObjectPath("/org/freedesktop/UDisks2/x")}}, false},
		{"added without a body", dbus.Signal{Name: sigInterfacesAdded, Path: "/"}, false},
		{"added with a mistyped body", dbus.Signal{Name: sigInterfacesAdded, Path: "/", Body: []any{"/org/bluez/hci0"}}, false},
		{"NameAcquired", dbus.Signal{Name: "org.freedesktop.DBus.NameAcquired", Path: "/org/freedesktop/DBus"}, false},
	} {
		if got := Relevant(&tc.sig); got != tc.want {
			t.Errorf("%s: Relevant = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("plain"), false},
		{fmt.Errorf("w: %w", dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}), true},
		{fmt.Errorf("w: %w", &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}), true},
		{dbus.Error{Name: "org.bluez.Error.NotReady"}, false},
	} {
		if got := gone(tc.err); got != tc.want {
			t.Errorf("gone(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}
