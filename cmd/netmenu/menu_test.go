package main

import (
	"errors"
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
func (f *fakeActions) askPassword(n *netmgr.Network) {
	f.calls = append(f.calls, "ask "+n.Name)
}
func (f *fakeActions) typePassword(text string) { f.calls = append(f.calls, "type "+text) }
func (f *fakeActions) joinSecured(n *netmgr.Network, password string) {
	f.calls = append(f.calls, "secured "+n.Name+" "+password)
}
func (f *fakeActions) toggleVPN(v *netmgr.VPN) { f.calls = append(f.calls, "vpn "+v.Name) }
func (f *fakeActions) settings()               { f.calls = append(f.calls, "settings") }

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
	wantRows(t, buildRows(&netmgr.State{}, nil, &fakeActions{}), settings)
}

func TestBuildRowsWifiStates(t *testing.T) {
	f := &fakeActions{}
	st := netmgr.State{WifiDevice: "/d/2", WifiHardware: true}
	rows := buildRows(&st, nil, f)
	wantRows(t, rows, toggle, sep, settings)
	if rows[0].On || !rows[0].KeepOpen {
		t.Errorf("switch with Wi-Fi off: on %t keepOpen %t", rows[0].On, rows[0].KeepOpen)
	}
	if got := f.click(&rows[0]); got != "wifi true" {
		t.Errorf("flipping the switch with Wi-Fi off asked for %q", got)
	}

	st.WifiEnabled = true
	rows = buildRows(&st, nil, f)
	wantRows(t, rows, toggle, note("Looking for networks…"), sep, settings)
	if got := f.click(&rows[0]); !rows[0].On || got != "wifi false" {
		t.Errorf("switch with Wi-Fi on: on %t, click asked for %q", rows[0].On, got)
	}

	st.WifiHardware = false
	rows = buildRows(&st, nil, f)
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
			{Name: "neighbour", Secured: true, Security: netmgr.SecurityPSK},
			{Name: "campus", Secured: true, Security: netmgr.SecurityOther},
			{Name: "cafe"},
		},
		VPNs: []netmgr.VPN{
			{Name: "XSN", Connection: "/s/9", Active: "/a/9", State: netmgr.StateActivating},
			{Name: "Work", Connection: "/s/8"},
		},
	}
	f := &fakeActions{}
	rows := buildRows(&st, nil, f)
	wantRows(t, rows,
		toggle,
		section("Known Networks"), item("home"), item("office"), item("phone"),
		section("Other Networks"), item("neighbour"), item("campus"), item("cafe"),
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
		{6, "ask neighbour", false, "", menu.TrailingLock},        // needs a password
		{7, "settings", false, "", menu.TrailingLock},             // needs more than a password
		{8, "open cafe", false, "", menu.TrailingNone},            // open
		{11, "vpn XSN", true, "Connecting…", menu.TrailingSwitch}, // switch shows on while coming up
		{12, "vpn Work", false, "", menu.TrailingSwitch},          // off
		{14, "settings", false, "", menu.TrailingNone},            // the settings row
	} {
		r := &rows[tc.i]
		got := f.click(r)
		if got != tc.click || r.On != tc.on || r.Detail != tc.detail || r.Trailing != tc.trailing {
			t.Errorf("row %d %q: click %q on %t detail %q trailing %d; want %q %t %q %d",
				tc.i, r.Label, got, r.On, r.Detail, r.Trailing, tc.click, tc.on, tc.detail, tc.trailing)
		}
	}
	if !rows[11].KeepOpen || rows[3].KeepOpen {
		t.Error("a VPN switch must keep the menu open, and joining a network must not")
	}
	if !rows[6].KeepOpen {
		t.Error("asking for a password must keep the menu open: the field is in it")
	}
	for _, i := range []int{2, 3, 6, 7, 8} {
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
	rows := buildRows(&st, nil, f)
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
	for _, r := range buildRows(&st, nil, &fakeActions{}) {
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

func TestAddressRowShowsTheIP(t *testing.T) {
	st := &netmgr.State{Address: "192.168.2.15"}
	rows := buildRows(st, nil, &fakeActions{})

	var found *menu.Row
	for i := range rows {
		if rows[i].Kind == menu.Header && rows[i].Label == "IP Address" {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("no IP Address row in %d rows", len(rows))
	}
	if found.Detail != "192.168.2.15" {
		t.Errorf("IP Address detail = %q, want 192.168.2.15", found.Detail)
	}
	if found.Click != nil {
		t.Error("the address row is there to be read, not clicked")
	}
}

// Nothing routed shows no row at all, rather than an empty one under a
// heading that promises an address.
func TestNoAddressRowWithoutAnAddress(t *testing.T) {
	for _, r := range buildRows(&netmgr.State{}, nil, &fakeActions{}) {
		if r.Kind == menu.Header && r.Label == "IP Address" {
			t.Fatal("an IP Address row with no address")
		}
	}
}

func input(label string) string { return fmt.Sprintf("%d:%s", menu.Input, label) }

func TestBuildRowsPasswordPrompt(t *testing.T) {
	st := netmgr.State{
		WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true,
		Networks: []netmgr.Network{
			{Name: "neighbour", Raw: []byte("neighbour"), Secured: true, Security: netmgr.SecurityPSK},
			{Name: "upstairs", Raw: []byte("upstairs"), Secured: true, Security: netmgr.SecuritySAE},
		},
	}
	f := &fakeActions{}
	rows := buildRows(&st, &prompt{ssid: []byte("neighbour"), text: "hunter2"}, f)
	wantRows(t, rows,
		toggle, section("Other Networks"),
		item("neighbour"), input("Password"), item("upstairs"),
		sep, settings)

	// The network being asked about has nothing more to do; the other one
	// moves the field to itself.
	if got := f.click(&rows[2]); got != "inert" {
		t.Errorf("the network being asked about asked for %q", got)
	}
	if got := f.click(&rows[4]); got != "ask upstairs" {
		t.Errorf("another secured network asked for %q", got)
	}

	field := &rows[3]
	if field.Text != "hunter2" || !field.Secret {
		t.Errorf("field: text %q secret %t; want what was typed, hidden", field.Text, field.Secret)
	}
	f.calls = nil
	field.Edit("hunter22")
	field.Submit("hunter22")
	if got := strings.Join(f.calls, "; "); got != "type hunter22; secured neighbour hunter22" {
		t.Errorf("typing and Return asked for %q", got)
	}

	// Why the last attempt failed goes under the field.
	rows = buildRows(&st, &prompt{ssid: []byte("upstairs"), note: noteRefused, failed: true}, f)
	wantRows(t, rows,
		toggle, section("Other Networks"),
		item("neighbour"), item("upstairs"), input("Password"), note(noteRefused),
		sep, settings)

	// A prompt for a network no longer in range shows nothing.
	rows = buildRows(&st, &prompt{ssid: []byte("gone")}, f)
	wantRows(t, rows, toggle, section("Other Networks"), item("neighbour"), item("upstairs"), sep, settings)
}

// A scan can push the network being typed into past the cap; it stays.
func TestBuildRowsPromptSurvivesTheCap(t *testing.T) {
	st := netmgr.State{WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true}
	for i := range maxOther + 3 {
		name := fmt.Sprintf("net%02d", i)
		st.Networks = append(st.Networks, netmgr.Network{
			Name: name, Raw: []byte(name), Secured: true, Security: netmgr.SecurityPSK,
		})
	}
	last := st.Networks[len(st.Networks)-1]
	rows := buildRows(&st, &prompt{ssid: last.Raw}, &fakeActions{})
	var items, inputs int
	for i := range rows {
		//nolint:exhaustive // only the two kinds being counted
		switch rows[i].Kind {
		case menu.Input:
			inputs++
			if rows[i-1].Label != last.Name {
				t.Errorf("the field is under %q, want %q", rows[i-1].Label, last.Name)
			}
		case menu.Item:
			items++
		}
	}
	if items != maxOther+1 || inputs != 1 {
		t.Errorf("%d networks and %d fields, want %d and 1", items, inputs, maxOther+1)
	}
}

func TestPromptAsks(t *testing.T) {
	n := &netmgr.Network{Raw: []byte("home")}
	for _, tc := range []struct {
		name string
		p    *prompt
		want bool
	}{
		{"no prompt", nil, false},
		{"this network", &prompt{ssid: []byte("home")}, true},
		{"another", &prompt{ssid: []byte("away")}, false},
	} {
		if got := tc.p.asks(n); got != tc.want {
			t.Errorf("%s: asks = %t, want %t", tc.name, got, tc.want)
		}
	}
	// A prompt with no network must not match a network with no name.
	if (&prompt{}).asks(&netmgr.Network{}) {
		t.Error("an empty prompt asks about a nameless network")
	}
}

func TestNoteFor(t *testing.T) {
	if got := noteFor(fmt.Errorf("joining: %w", netmgr.ErrJoinFailed)); got != noteRefused {
		t.Errorf("a refused join says %q", got)
	}
	if got := noteFor(errors.New("no permission")); got != noteFailed {
		t.Errorf("any other failure says %q", got)
	}
}
