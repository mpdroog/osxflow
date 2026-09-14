package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/netmgr"
)

// fakeActions records what the rows ask for.
type fakeActions struct{ calls []string }

func (f *fakeActions) setWifi(on bool)            { f.calls = append(f.calls, fmt.Sprintf("wifi %t", on)) }
func (f *fakeActions) join(n *netmgr.Network)     { f.calls = append(f.calls, "join "+n.Name) }
func (f *fakeActions) joinOpen(n *netmgr.Network) { f.calls = append(f.calls, "open "+n.Name) }
func (f *fakeActions) toggleVPN(v *netmgr.VPN)    { f.calls = append(f.calls, "vpn "+v.Name) }
func (f *fakeActions) settings()                  { f.calls = append(f.calls, "settings") }

// click presses a row and reports what it asked for, or "inert".
func (f *fakeActions) click(r *menu.Row) string {
	if r.Click == nil {
		return "inert"
	}
	f.calls = nil
	r.Click()
	return strings.Join(f.calls, "; ")
}

// kinds summarises rows as kind:label, which is what a test reads most
// easily against the menu it expects.
func kinds(rows []menu.Row) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = fmt.Sprintf("%d:%s", rows[i].Kind, rows[i].Label)
	}
	return out
}

func wantRows(t *testing.T, got []menu.Row, want ...string) {
	t.Helper()
	g := kinds(got)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Fatalf("rows:\n got %q\nwant %q", g, want)
	}
}

var (
	toggle   = fmt.Sprintf("%d:Wi-Fi", menu.Toggle)
	sep      = fmt.Sprintf("%d:", menu.Separator)
	settings = fmt.Sprintf("%d:Network Settings…", menu.Action)
)

func section(label string) string { return fmt.Sprintf("%d:%s", menu.Section, label) }
func item(label string) string    { return fmt.Sprintf("%d:%s", menu.Item, label) }
func note(label string) string    { return fmt.Sprintf("%d:%s", menu.Note, label) }

func TestBuildRowsNothing(t *testing.T) {
	wantRows(t, buildRows(&netmgr.State{}, &fakeActions{}), settings)
}

func TestBuildRowsWifiStates(t *testing.T) {
	f := &fakeActions{}
	st := netmgr.State{WifiDevice: "/d/2", WifiHardware: true}
	rows := buildRows(&st, f)
	wantRows(t, rows, toggle, sep, settings)
	if rows[0].On || !rows[0].KeepOpen {
		t.Errorf("switch with Wi-Fi off: on %t keepOpen %t", rows[0].On, rows[0].KeepOpen)
	}
	if got := f.click(&rows[0]); got != "wifi true" {
		t.Errorf("flipping the switch with Wi-Fi off asked for %q", got)
	}

	st.WifiEnabled = true
	rows = buildRows(&st, f)
	wantRows(t, rows, toggle, note("Looking for networks…"), sep, settings)
	if got := f.click(&rows[0]); !rows[0].On || got != "wifi false" {
		t.Errorf("switch with Wi-Fi on: on %t, click asked for %q", rows[0].On, got)
	}

	st.WifiHardware = false
	rows = buildRows(&st, f)
	wantRows(t, rows, toggle, note("Turned off by a hardware switch"), sep, settings)
	if got := f.click(&rows[0]); rows[0].On || got != "inert" {
		t.Errorf("switch with the hardware off: on %t, click %q; want off and inert", rows[0].On, got)
	}
}

func TestBuildRowsNetworks(t *testing.T) {
	st := netmgr.State{
		WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true,
		Networks: []netmgr.Network{
			{Name: "home", Saved: "/s/1", Active: "/a/1", State: netmgr.StateActivated, Secured: true, Strength: 80},
			{Name: "office", Saved: "/s/2", Secured: true},
			{Name: "phone", Saved: "/s/3", Active: "/a/3", State: netmgr.StateActivating},
			{Name: "neighbour", Secured: true},
			{Name: "cafe"},
		},
		VPNs: []netmgr.VPN{
			{Name: "XSN", Connection: "/s/9", Active: "/a/9", State: netmgr.StateActivating},
			{Name: "Work", Connection: "/s/8"},
		},
	}
	f := &fakeActions{}
	rows := buildRows(&st, f)
	wantRows(t, rows,
		toggle,
		section("Known Networks"), item("home"), item("office"), item("phone"),
		section("Other Networks"), item("neighbour"), item("cafe"),
		sep, section("VPN"), item("XSN"), item("Work"),
		sep, settings)

	for _, tc := range []struct {
		i        int
		click    string
		on       bool
		detail   string
		trailing menu.Trailing
	}{
		{2, "inert", true, "", menu.TrailingLock},                 // in use: nothing to do
		{3, "join office", false, "", menu.TrailingLock},          // saved
		{4, "inert", false, "Connecting…", menu.TrailingNone},     // on its way
		{6, "settings", false, "", menu.TrailingLock},             // needs a password
		{7, "open cafe", false, "", menu.TrailingNone},            // open
		{10, "vpn XSN", true, "Connecting…", menu.TrailingSwitch}, // switch shows on while coming up
		{11, "vpn Work", false, "", menu.TrailingSwitch},          // off
		{13, "settings", false, "", menu.TrailingNone},            // the settings row
	} {
		r := &rows[tc.i]
		got := f.click(r)
		if got != tc.click || r.On != tc.on || r.Detail != tc.detail || r.Trailing != tc.trailing {
			t.Errorf("row %d %q: click %q on %t detail %q trailing %d; want %q %t %q %d",
				tc.i, r.Label, got, r.On, r.Detail, r.Trailing, tc.click, tc.on, tc.detail, tc.trailing)
		}
	}
	if !rows[10].KeepOpen || rows[3].KeepOpen {
		t.Error("a VPN switch must keep the menu open, and joining a network must not")
	}
	for _, i := range []int{2, 3, 6, 7} {
		if rows[i].Glyph == nil {
			t.Errorf("network row %d has no badge", i)
		}
	}
}

// Every row's click acts on its own network, not on whichever network the
// loop that built the rows ended on.
func TestBuildRowsClosuresCaptureTheirNetwork(t *testing.T) {
	st := netmgr.State{WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true}
	for _, name := range []string{"a", "b", "c"} {
		st.Networks = append(st.Networks, netmgr.Network{Name: name, Saved: dbus.ObjectPath("/s/" + name)})
	}
	f := &fakeActions{}
	rows := buildRows(&st, f)
	for i, want := range []string{"join a", "join b", "join c"} {
		if got := f.click(&rows[2+i]); got != want {
			t.Errorf("row %d asked for %q, want %q", 2+i, got, want)
		}
	}
}

func TestBuildRowsCaps(t *testing.T) {
	st := netmgr.State{WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true}
	for i := range maxKnown + 3 {
		st.Networks = append(st.Networks, netmgr.Network{Name: fmt.Sprintf("k%d", i), Saved: dbus.ObjectPath(fmt.Sprintf("/s/%d", i))})
	}
	for i := range maxOther + 3 {
		st.Networks = append(st.Networks, netmgr.Network{Name: fmt.Sprintf("o%d", i)})
	}
	var known, other int
	for _, r := range buildRows(&st, &fakeActions{}) {
		switch {
		case r.Kind != menu.Item:
		case strings.HasPrefix(r.Label, "k"):
			known++
		default:
			other++
		}
	}
	if known != maxKnown || other != maxOther {
		t.Errorf("showed %d known and %d other, want %d and %d", known, other, maxKnown, maxOther)
	}
}
