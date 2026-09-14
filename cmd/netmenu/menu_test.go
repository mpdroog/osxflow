package main

import (
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/netmgr"
)

// kinds summarises rows as kind:label, which is what a test reads most
// easily against the menu it expects.
func kinds(rows []row) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = fmt.Sprintf("%d:%s", rows[i].kind, rows[i].label)
	}
	return out
}

func wantRows(t *testing.T, got []row, want ...string) {
	t.Helper()
	g := kinds(got)
	if len(g) != len(want) {
		t.Fatalf("rows:\n got %q\nwant %q", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("rows:\n got %q\nwant %q", g, want)
		}
	}
}

var (
	toggle   = fmt.Sprintf("%d:Wi-Fi", rowToggle)
	sep      = fmt.Sprintf("%d:", rowSeparator)
	settings = fmt.Sprintf("%d:Network Settings…", rowAction)
)

func section(label string) string { return fmt.Sprintf("%d:%s", rowSection, label) }
func network(label string) string { return fmt.Sprintf("%d:%s", rowNetwork, label) }
func vpn(label string) string     { return fmt.Sprintf("%d:%s", rowVPN, label) }
func note(label string) string    { return fmt.Sprintf("%d:%s", rowNote, label) }

func TestBuildRowsNothing(t *testing.T) {
	wantRows(t, buildRows(&netmgr.State{}), settings)
}

func TestBuildRowsWifiStates(t *testing.T) {
	st := netmgr.State{WifiDevice: "/d/2", WifiHardware: true}
	rows := buildRows(&st)
	wantRows(t, rows, toggle, sep, settings)
	if rows[0].on || rows[0].act != actWifi {
		t.Errorf("switch with Wi-Fi off: on %t act %d", rows[0].on, rows[0].act)
	}

	st.WifiEnabled = true
	rows = buildRows(&st)
	wantRows(t, rows, toggle, note("Looking for networks…"), sep, settings)
	if !rows[0].on {
		t.Error("switch off with Wi-Fi on")
	}

	st.WifiHardware = false
	rows = buildRows(&st)
	wantRows(t, rows, toggle, note("Turned off by a hardware switch"), sep, settings)
	if rows[0].on || rows[0].act != actNone {
		t.Errorf("switch with the hardware off: on %t act %d, want off and inert", rows[0].on, rows[0].act)
	}
}

func TestBuildRowsNetworks(t *testing.T) {
	st := netmgr.State{
		WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true,
		Networks: []netmgr.Network{
			{Name: "home", Saved: "/s/1", Active: "/a/1", State: netmgr.StateActivated, Secured: true},
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
	rows := buildRows(&st)
	wantRows(t, rows,
		toggle,
		section("Known Networks"), network("home"), network("office"), network("phone"),
		section("Other Networks"), network("neighbour"), network("cafe"),
		sep, section("VPN"), vpn("XSN"), vpn("Work"),
		sep, settings)

	for _, tc := range []struct {
		i      int
		act    action
		on     bool
		detail string
	}{
		{2, actNone, true, ""},             // in use: nothing to do
		{3, actJoin, false, ""},            // saved
		{4, actNone, false, "Connecting…"}, // on its way
		{6, actSettings, false, ""},        // needs a password
		{7, actJoinOpen, false, ""},        // open
		{10, actVPN, true, "Connecting…"},  // switch shows on while coming up
		{11, actVPN, false, ""},            // off
		{13, actSettings, false, ""},       // the settings row
	} {
		r := &rows[tc.i]
		if r.act != tc.act || r.on != tc.on || r.detail != tc.detail {
			t.Errorf("row %d %q: act %d on %t detail %q; want %d %t %q",
				tc.i, r.label, r.act, r.on, r.detail, tc.act, tc.on, tc.detail)
		}
	}
	if rows[10].vpn.Active != "/a/9" || rows[3].net.Saved != "/s/2" {
		t.Error("rows do not carry the network or VPN they act on")
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
	for _, r := range buildRows(&st) {
		if r.kind != rowNetwork {
			continue
		}
		if r.net.Saved != "" {
			known++
		} else {
			other++
		}
	}
	if known != maxKnown || other != maxOther {
		t.Errorf("showed %d known and %d other, want %d and %d", known, other, maxKnown, maxOther)
	}
}

func TestLayoutAndHit(t *testing.T) {
	h := &rowHeights{row: 30, toggle: 40, section: 20, note: 25, sep: 10, action: 28}
	rows := []row{
		{kind: rowToggle, act: actWifi},
		{kind: rowSection},
		{kind: rowNetwork, act: actJoin},
		{kind: rowSeparator},
		{kind: rowAction, act: actSettings},
	}
	tops, total := layout(rows, h, 5)
	wantTops := []float64{5, 45, 65, 95, 105}
	for i := range wantTops {
		if tops[i] != wantTops[i] {
			t.Fatalf("tops = %v, want %v", tops, wantTops)
		}
	}
	if total != 138 {
		t.Errorf("total = %v, want 138", total)
	}

	for _, tc := range []struct {
		y    float64
		want int
	}{
		{0, -1},   // padding
		{5, 0},    // top edge of the toggle
		{44.9, 0}, // bottom of the toggle
		{50, -1},  // a section heading does nothing
		{65, 2},   // a network
		{100, -1}, // separator
		{110, 4},  // settings
		{133, -1}, // bottom padding
		{500, -1}, // outside
	} {
		if got := hit(rows, tops, h, tc.y); got != tc.want {
			t.Errorf("hit(y=%v) = %d, want %d", tc.y, got, tc.want)
		}
	}
}
