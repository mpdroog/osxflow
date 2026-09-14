package netmgr

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

func TestDisplayName(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{[]byte("K&M2"), "K&M2"},
		{[]byte("Café"), "Café"},
		{[]byte("two\nlines"), "two lines"},
		{[]byte("tab\there"), "tab here"},
		{[]byte{'a', 0xff, 'b'}, "a�b"},
		{[]byte{0}, " "},
		{nil, ""},
	} {
		if got := DisplayName(tc.in); got != tc.want {
			t.Errorf("DisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func FuzzDisplayName(f *testing.F) {
	for _, s := range []string{"K&M2", "", "\x00\xff", "a\nb", "日本語"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, ssid []byte) {
		got := DisplayName(ssid)
		if !utf8.ValidString(got) {
			t.Fatalf("DisplayName(%q) = %q, not valid UTF-8", ssid, got)
		}
		if strings.IndexFunc(got, unicode.IsControl) >= 0 {
			t.Fatalf("DisplayName(%q) = %q, contains a control character", ssid, got)
		}
		if (len(ssid) == 0) != (got == "") {
			t.Fatalf("DisplayName(%q) = %q: empty in and out must go together", ssid, got)
		}
	})
}

func TestBars(t *testing.T) {
	for _, tc := range []struct {
		strength uint8
		want     int
	}{{0, 1}, {30, 1}, {31, 2}, {55, 2}, {56, 3}, {100, 3}, {255, 3}} {
		if got := Bars(tc.strength); got != tc.want {
			t.Errorf("Bars(%d) = %d, want %d", tc.strength, got, tc.want)
		}
	}
}

func TestSecured(t *testing.T) {
	for _, tc := range []struct {
		ap   AccessPoint
		want bool
	}{
		{AccessPoint{}, false},
		{AccessPoint{Flags: apFlagPrivacy}, true},
		// Flags other than privacy (WPS, say) do not mean a key.
		{AccessPoint{Flags: 0x2}, false},
		{AccessPoint{WPAFlags: 0x100}, true},
		{AccessPoint{RSNFlags: 0x188}, true},
	} {
		if got := tc.ap.Secured(); got != tc.want {
			t.Errorf("%+v.Secured() = %t, want %t", tc.ap, got, tc.want)
		}
	}
}

func TestNetworks(t *testing.T) {
	aps := []AccessPoint{
		{Path: "/ap/1", SSID: []byte("home"), Strength: 40, RSNFlags: 0x188},
		{Path: "/ap/2", SSID: []byte("home"), Strength: 80, RSNFlags: 0x188},
		{Path: "/ap/3", SSID: []byte("cafe"), Strength: 90},
		{Path: "/ap/4", SSID: nil, Strength: 99}, // hidden
		{Path: "/ap/5", SSID: []byte("office"), Strength: 20, WPAFlags: 1},
		{Path: "/ap/6", SSID: []byte("zebra"), Strength: 90},
		// One access point of a name wanting a key makes the name secured.
		{Path: "/ap/7", SSID: []byte("cafe"), Strength: 10, Flags: apFlagPrivacy},
	}
	saved := []Saved{
		{Path: "/s/1", ID: "home", Type: TypeWifi, SSID: []byte("home")},
		{Path: "/s/2", ID: "office old", Type: TypeWifi, SSID: []byte("office")},
		{Path: "/s/3", ID: "office", Type: TypeWifi, SSID: []byte("office")},
		{Path: "/s/4", ID: "away", Type: TypeWifi, SSID: []byte("not in range")},
		{Path: "/s/5", ID: "vpn", Type: TypeVPN},
	}
	active := []Active{
		{Path: "/a/1", Connection: "/s/3", Type: TypeWifi, State: StateActivating},
	}

	got := Networks(aps, "", saved, active)
	want := []struct {
		name     string
		strength uint8
		ap       dbus.ObjectPath
		secured  bool
		saved    dbus.ObjectPath
		active   dbus.ObjectPath
	}{
		// Active first, and the profile that is up wins over an earlier one.
		{"office", 20, "/ap/5", true, "/s/3", "/a/1"},
		{"home", 80, "/ap/2", true, "/s/1", ""},
		// Unsaved by strength, then name.
		{"cafe", 90, "/ap/3", true, "", ""},
		{"zebra", 90, "/ap/6", false, "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d networks, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := &got[i]
		if g.Name != w.name || g.Strength != w.strength || g.AP != w.ap || g.Secured != w.secured ||
			g.Saved != w.saved || g.Active != w.active {
			t.Errorf("network %d = %+v, want %+v", i, *g, w)
		}
	}
	if !got[0].Connecting() || got[0].Connected() {
		t.Errorf("office: Connecting %t Connected %t, want true false", got[0].Connecting(), got[0].Connected())
	}
}

// The raw SSID is a copy: a caller holding a Network must not see it change
// when NetworkManager's reply buffer is reused.
func TestNetworksCopiesSSID(t *testing.T) {
	ssid := []byte("home")
	nets := Networks([]AccessPoint{{SSID: ssid}}, "", nil, nil)
	ssid[0] = 'X'
	if string(nets[0].Raw) != "home" {
		t.Errorf("Raw = %q after the input changed, want %q", nets[0].Raw, "home")
	}
}

// The access point in use sets a network's strength, whichever order the
// access points arrive in and however strong the others are. This is the
// case on this machine: K&M2 connected on 5 GHz at 36, with its 2.4 GHz
// radio at 82.
func TestNetworksActiveAccessPoint(t *testing.T) {
	weak := AccessPoint{Path: "/ap/5ghz", SSID: []byte("K&M2"), Strength: 36}
	strong := AccessPoint{Path: "/ap/24ghz", SSID: []byte("K&M2"), Strength: 82}
	other := AccessPoint{Path: "/ap/other", SSID: []byte("K&M2"), Strength: 57}
	for _, order := range [][]AccessPoint{
		{weak, strong, other},
		{strong, weak, other},
		{strong, other, weak},
	} {
		nets := Networks(order, "/ap/5ghz", nil, nil)
		if len(nets) != 1 || nets[0].Strength != 36 || nets[0].AP != "/ap/5ghz" {
			t.Errorf("order %v: got %+v, want strength 36 from /ap/5ghz", order, nets)
		}
	}
	// "/" is NetworkManager's "none": the strongest speaks for the network.
	for _, none := range []dbus.ObjectPath{"", "/"} {
		nets := Networks([]AccessPoint{weak, strong}, none, nil, nil)
		if len(nets) != 1 || nets[0].Strength != 82 {
			t.Errorf("active %q: got %+v, want strength 82", none, nets)
		}
	}
}

func FuzzNetworks(f *testing.F) {
	f.Add([]byte("home\x50cafe\x10home\x60"), uint8(1))
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, savedEvery uint8) {
		// Every two bytes are an access point: a one-byte SSID (so names
		// collide often) and a strength.
		var aps []AccessPoint
		var saved []Saved
		for i := 0; i+1 < len(data); i += 2 {
			var ssid []byte
			if data[i] != 0 {
				ssid = []byte{data[i]}
			}
			aps = append(aps, AccessPoint{Path: dbus.ObjectPath(fmt.Sprintf("/ap/%d", i)), SSID: ssid, Strength: data[i+1]})
			if savedEvery != 0 && i%int(savedEvery) == 0 {
				saved = append(saved, Saved{Path: dbus.ObjectPath(fmt.Sprintf("/s/%d", i)), Type: TypeWifi, SSID: ssid})
			}
		}
		nets := Networks(aps, "", saved, nil)
		if len(nets) > len(aps) {
			t.Fatalf("%d networks from %d access points", len(nets), len(aps))
		}
		seen := map[string]bool{}
		for i := range nets {
			n := &nets[i]
			if len(n.Raw) == 0 {
				t.Fatalf("hidden network listed: %+v", *n)
			}
			if seen[string(n.Raw)] {
				t.Fatalf("SSID %q listed twice", n.Raw)
			}
			seen[string(n.Raw)] = true
			if i > 0 {
				prev := &nets[i-1]
				if rank(prev) > rank(n) || (rank(prev) == rank(n) && prev.Strength < n.Strength) {
					t.Fatalf("out of order at %d: %+v before %+v", i, *prev, *n)
				}
			}
		}
	})
}

func TestVPNs(t *testing.T) {
	saved := []Saved{
		{Path: "/s/1", ID: "XSN", Type: TypeVPN},
		{Path: "/s/2", ID: "home", Type: TypeWifi, SSID: []byte("home")},
		{Path: "/s/3", ID: "Office WG", Type: TypeWireGuard},
		{Path: "/s/4", ID: "Alpha", Type: TypeVPN},
	}
	active := []Active{
		// The old connection going down and the new one coming up: the new
		// one describes the profile, in either order.
		{Path: "/a/new", Connection: "/s/1", Type: TypeVPN, State: StateActivating},
		{Path: "/a/old", Connection: "/s/1", Type: TypeVPN, State: StateDeactivating},
		{Path: "/a/wg", Connection: "/s/3", Type: TypeWireGuard, State: StateDeactivated},
	}
	got := VPNs(saved, active)
	if len(got) != 3 {
		t.Fatalf("got %d VPNs, want 3: %+v", len(got), got)
	}
	for i, w := range []struct {
		name   string
		active dbus.ObjectPath
		on     bool
	}{{"Alpha", "", false}, {"Office WG", "/a/wg", false}, {"XSN", "/a/new", true}} {
		if got[i].Name != w.name || got[i].Active != w.active || got[i].On() != w.on {
			t.Errorf("VPN %d = %+v (on %t), want %+v", i, got[i], got[i].On(), w)
		}
	}
}

func TestWired(t *testing.T) {
	if Wired([]Active{{Type: TypeEthernet, State: StateActivating}, {Type: TypeWifi, State: StateActivated}}) {
		t.Error("Wired with no activated Ethernet connection")
	}
	if !Wired([]Active{{Type: TypeEthernet, State: StateActivated}}) {
		t.Error("not Wired with an activated Ethernet connection")
	}
}

func TestStateWifi(t *testing.T) {
	var st State
	if st.Wifi() != nil {
		t.Error("Wifi() on an empty state is not nil")
	}
	st.Networks = []Network{{Name: "a"}}
	if st.Wifi() != nil {
		t.Error("Wifi() with no active network is not nil")
	}
	st.Networks[0].Active = "/a/1"
	if n := st.Wifi(); n == nil || n.Name != "a" {
		t.Errorf("Wifi() = %+v, want network a", n)
	}
}
