// Package netmgr reads NetworkManager's state and asks it to connect and
// disconnect, over the system bus.
//
// It is deliberately a small slice of NetworkManager: Wi-Fi on or off, the
// networks in range, joining one, and turning a saved VPN on or off.
// Anything more -- creating a profile, typing a password -- is left to
// nm-connection-editor. Nothing here knows about a display, so the same
// package serves an X11 menu today and a Wayland one if that ever comes.
//
// This file is the pure half: turning what NetworkManager reports into what
// a menu shows, testable without a bus. client.go talks to the bus.
package netmgr

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// Connection types, as NetworkManager spells them in connection.type.
const (
	TypeWifi      = "802-11-wireless"
	TypeEthernet  = "802-3-ethernet"
	TypeVPN       = "vpn"
	TypeWireGuard = "wireguard"
)

// ActiveState is NMActiveConnectionState.
type ActiveState uint32

// The states an active connection passes through.
const (
	StateUnknown ActiveState = iota
	StateActivating
	StateActivated
	StateDeactivating
	StateDeactivated
)

// apFlagPrivacy is NM_802_11_AP_FLAGS_PRIVACY: the network wants a key,
// WEP or better.
const apFlagPrivacy = 0x1

// AccessPoint is one radio NetworkManager can hear.
type AccessPoint struct {
	Path     dbus.ObjectPath
	SSID     []byte
	Strength uint8

	// The three flag words, straight from NetworkManager.
	Flags, WPAFlags, RSNFlags uint32
}

// Secured reports whether joining the network needs a key. Any of the
// three flag words says so: Flags for WEP-style privacy, the other two for
// WPA and WPA2/3.
func (ap *AccessPoint) Secured() bool {
	return ap.Flags&apFlagPrivacy != 0 || ap.WPAFlags != 0 || ap.RSNFlags != 0
}

// Saved is one saved connection profile.
type Saved struct {
	Path dbus.ObjectPath
	ID   string
	Type string

	// SSID is set for Wi-Fi profiles only.
	SSID []byte
}

// Active is one connection that is up, or on its way up or down.
type Active struct {
	Path       dbus.ObjectPath
	Connection dbus.ObjectPath
	Type       string
	State      ActiveState
}

// Network is one Wi-Fi network as a menu shows it: every access point
// broadcasting the same name folded into one entry.
type Network struct {
	// Name is the SSID made safe to draw. Raw is the SSID as broadcast,
	// which is what joining needs.
	Name string
	Raw  []byte

	// Strength is 0 to 100, and AP is the access point it was read from:
	// the one in use when the network is connected, otherwise the
	// strongest.
	Strength uint8
	AP       dbus.ObjectPath

	Secured bool

	// Saved is the profile that joins this network, or "" when there is
	// none.
	Saved dbus.ObjectPath

	// Active is the active connection while this network is up or coming
	// up, or "", and State is where that connection has got to.
	Active dbus.ObjectPath
	State  ActiveState
}

// Connected reports whether the network is the one in use.
func (n *Network) Connected() bool { return n.Active != "" && n.State == StateActivated }

// Connecting reports whether NetworkManager is joining it right now.
func (n *Network) Connecting() bool { return n.Active != "" && n.State == StateActivating }

// VPN is one saved VPN profile.
type VPN struct {
	Name       string
	Connection dbus.ObjectPath

	// Active is the active connection while the VPN is up or changing, or
	// "", and State is where that connection has got to.
	Active dbus.ObjectPath
	State  ActiveState
}

// On reports whether the VPN is up or coming up: the position a switch for
// it shows.
func (v *VPN) On() bool {
	return v.Active != "" && (v.State == StateActivating || v.State == StateActivated)
}

// State is everything a menu and its icon need, at one moment.
type State struct {
	// WifiDevice is the first Wi-Fi adapter, or "" when there is none.
	WifiDevice dbus.ObjectPath

	// WifiEnabled is the software switch. WifiHardware is false when a
	// hardware switch or rfkill has the radio off, which the software
	// switch cannot override.
	WifiEnabled  bool
	WifiHardware bool

	// Networks is sorted: the one in use first, then saved ones, then by
	// signal strength.
	Networks []Network

	// VPNs is sorted by name.
	VPNs []VPN

	// Wired reports an activated Ethernet connection.
	Wired bool
}

// Wifi returns the network in use or being joined, or nil.
func (s *State) Wifi() *Network {
	// Networks sorts active ones first, so only the first can be.
	if len(s.Networks) > 0 && s.Networks[0].Active != "" {
		return &s.Networks[0]
	}
	return nil
}

// Bars turns a signal strength into how many arcs of a Wi-Fi symbol to
// light, 1 to 3. The cut-offs are nmcli's, with its bottom two levels of
// four folded together.
func Bars(strength uint8) int {
	switch {
	case strength > 55:
		return 3
	case strength > 30:
		return 2
	}
	return 1
}

// DisplayName makes an SSID drawable.
//
// An SSID is up to 32 arbitrary bytes, not text. Invalid UTF-8 becomes
// U+FFFD, and control characters -- which a font draws as boxes, or as a
// second line in the case of a newline -- become spaces.
func DisplayName(ssid []byte) string {
	s := strings.ToValidUTF8(string(ssid), "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// Networks folds access points into networks and marks the ones a saved
// profile can join and the one in use.
//
// activeAP is the access point the adapter is associated with, or "" or
// "/" for none. It sets its network's strength even when another access
// point with the same name is stronger: a network with a 2.4 GHz radio at
// 82 and a 5 GHz one at 36, connected on the 5 GHz one, has a signal of 36
// -- which is what nm-applet shows too.
//
// Hidden networks are left out: they broadcast no name to show and none to
// join by. A saved profile for a network not in range is left out too,
// since there is nothing to connect it to.
func Networks(aps []AccessPoint, activeAP dbus.ObjectPath, saved []Saved, active []Active) []Network {
	inUse := func(p dbus.ObjectPath) bool {
		return activeAP != "" && activeAP != noObject && p == activeAP
	}
	activeBy := activeByConnection(active)
	index := make(map[string]int, len(aps))
	nets := make([]Network, 0, len(aps))
	for i := range aps {
		ap := &aps[i]
		if len(ap.SSID) == 0 {
			continue
		}
		j, seen := index[string(ap.SSID)]
		if !seen {
			index[string(ap.SSID)] = len(nets)
			nets = append(nets, Network{
				Name:     DisplayName(ap.SSID),
				Raw:      slices.Clone(ap.SSID),
				Strength: ap.Strength,
				AP:       ap.Path,
				Secured:  ap.Secured(),
			})
			continue
		}
		n := &nets[j]
		switch {
		case inUse(n.AP):
			// The access point in use speaks for the network.
		case inUse(ap.Path) || ap.Strength > n.Strength:
			n.Strength, n.AP = ap.Strength, ap.Path
		}
		// One access point of several wanting a key makes the name one
		// that may: which access point a join lands on is not ours to pick.
		n.Secured = n.Secured || ap.Secured()
	}

	for i := range saved {
		s := &saved[i]
		if s.Type != TypeWifi {
			continue
		}
		j, inRange := index[string(s.SSID)]
		if !inRange {
			continue
		}
		n := &nets[j]
		// Several profiles can name one SSID. The one that is up wins;
		// otherwise the first is as good as any, and NetworkManager's
		// ordering is stable.
		a, up := activeBy[s.Path]
		switch {
		case up:
			n.Saved, n.Active, n.State = s.Path, a.Path, a.State
		case n.Saved == "":
			n.Saved = s.Path
		}
	}

	sort.SliceStable(nets, func(i, j int) bool {
		a, b := &nets[i], &nets[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if a.Strength != b.Strength {
			return a.Strength > b.Strength
		}
		return a.Name < b.Name
	})
	return nets
}

// rank orders networks into the three groups a menu shows them in.
func rank(n *Network) int {
	switch {
	case n.Active != "":
		return 0
	case n.Saved != "":
		return 1
	}
	return 2
}

// VPNs lists the saved VPN profiles, WireGuard included, with their state.
func VPNs(saved []Saved, active []Active) []VPN {
	activeBy := activeByConnection(active)
	var vpns []VPN
	for i := range saved {
		s := &saved[i]
		if s.Type != TypeVPN && s.Type != TypeWireGuard {
			continue
		}
		v := VPN{Name: s.ID, Connection: s.Path}
		if a, up := activeBy[s.Path]; up {
			v.Active, v.State = a.Path, a.State
		}
		vpns = append(vpns, v)
	}
	sort.SliceStable(vpns, func(i, j int) bool { return vpns[i].Name < vpns[j].Name })
	return vpns
}

// Wired reports whether an Ethernet connection is up.
func Wired(active []Active) bool {
	for i := range active {
		if active[i].Type == TypeEthernet && active[i].State == StateActivated {
			return true
		}
	}
	return false
}

// activeByConnection indexes active connections by the profile they came
// from.
//
// For a moment during a reconnect one profile has two: the old one
// deactivating and the new one activating. The one on its way up is the
// one that describes the profile.
func activeByConnection(active []Active) map[dbus.ObjectPath]Active {
	by := make(map[dbus.ObjectPath]Active, len(active))
	for i := range active {
		a := active[i]
		if prev, ok := by[a.Connection]; ok && prev.State < StateDeactivating {
			continue
		}
		by[a.Connection] = a
	}
	return by
}
