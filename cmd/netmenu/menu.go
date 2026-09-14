package main

// What the menu lists, and where each row sits. Kept free of X11 so the
// part a user notices -- which networks show, what a click does -- is
// tested without a display.

import (
	"github.com/mpdroog/osxflow/internal/netmgr"
)

type rowKind int

const (
	rowToggle    rowKind = iota // "Wi-Fi" with its switch
	rowSection                  // a small dim heading
	rowNote                     // a dim line of explanation
	rowNetwork                  // a Wi-Fi network
	rowVPN                      // a VPN with its switch
	rowSeparator                // a hairline
	rowAction                   // "Network Settings…"
)

// action is what clicking a row does. A row with actNone is not
// highlighted and ignores clicks.
type action int

const (
	actNone     action = iota
	actWifi            // flip the Wi-Fi switch
	actJoin            // bring a saved network's profile up
	actJoinOpen        // save a profile for an open network and join it
	actVPN             // flip a VPN
	actSettings        // open nm-connection-editor
)

type row struct {
	kind   rowKind
	label  string
	detail string
	act    action

	// on is a switch's position, or whether a network is the one in use.
	on bool

	net netmgr.Network
	vpn netmgr.VPN
}

// A glance at what is nearby, not a site survey. The network in use sorts
// first, so a cap never hides it.
const (
	maxKnown = 6
	maxOther = 8
)

// buildRows lays out the menu for a state.
func buildRows(st *netmgr.State) []row {
	rows := make([]row, 0, 8+maxKnown+maxOther+len(st.VPNs))
	if st.WifiDevice != "" {
		rows = append(rows, wifiRows(st)...)
	}
	if len(st.VPNs) > 0 {
		if len(rows) > 0 {
			rows = append(rows, row{kind: rowSeparator})
		}
		rows = append(rows, row{kind: rowSection, label: "VPN"})
		for i := range st.VPNs {
			v := st.VPNs[i]
			rows = append(rows, row{kind: rowVPN, label: v.Name, detail: vpnDetail(&v), on: v.On(), act: actVPN, vpn: v})
		}
	}
	if len(rows) > 0 {
		rows = append(rows, row{kind: rowSeparator})
	}
	return append(rows, row{kind: rowAction, label: "Network Settings…", act: actSettings})
}

func wifiRows(st *netmgr.State) []row {
	toggle := row{kind: rowToggle, label: "Wi-Fi", on: st.WifiEnabled && st.WifiHardware, act: actWifi}
	if !st.WifiHardware {
		// The software switch cannot turn on a radio a hardware switch has
		// off, so it is shown but does nothing.
		toggle.act = actNone
		return []row{toggle, {kind: rowNote, label: "Turned off by a hardware switch"}}
	}
	if !st.WifiEnabled {
		return []row{toggle}
	}

	var known, other []row
	for i := range st.Networks {
		n := &st.Networks[i]
		r := row{kind: rowNetwork, label: n.Name, on: n.Connected(), net: *n}
		switch {
		case n.Connected():
		case n.Connecting():
			r.detail = "Connecting…"
		case n.Saved != "":
			r.act = actJoin
		case n.Secured:
			// Joining needs a password, and there is no prompt here by
			// design: nm-connection-editor asks for it and saves it, after
			// which the network is a known one.
			r.act = actSettings
		default:
			r.act = actJoinOpen
		}
		if n.Saved != "" {
			if len(known) < maxKnown {
				known = append(known, r)
			}
		} else if len(other) < maxOther {
			other = append(other, r)
		}
	}

	rows := make([]row, 0, 3+len(known)+len(other))
	rows = append(rows, toggle)
	if len(known)+len(other) == 0 {
		return append(rows, row{kind: rowNote, label: "Looking for networks…"})
	}
	if len(known) > 0 {
		rows = append(rows, row{kind: rowSection, label: "Known Networks"})
		rows = append(rows, known...)
	}
	if len(other) > 0 {
		rows = append(rows, row{kind: rowSection, label: "Other Networks"})
		rows = append(rows, other...)
	}
	return rows
}

func vpnDetail(v *netmgr.VPN) string {
	if v.Active == "" {
		return ""
	}
	switch v.State {
	case netmgr.StateActivating:
		return "Connecting…"
	case netmgr.StateDeactivating:
		return "Disconnecting…"
	case netmgr.StateUnknown, netmgr.StateActivated, netmgr.StateDeactivated:
	}
	return ""
}

func (k rowKind) height(h *rowHeights) float64 {
	switch k {
	case rowToggle:
		return h.toggle
	case rowSection:
		return h.section
	case rowNote:
		return h.note
	case rowSeparator:
		return h.sep
	case rowAction:
		return h.action
	case rowNetwork, rowVPN:
	}
	return h.row
}

// layout places rows top to bottom and returns each row's top edge and the
// menu's height.
func layout(rows []row, h *rowHeights, padY float64) (tops []float64, total float64) {
	tops = make([]float64, len(rows))
	y := padY
	for i := range rows {
		tops[i] = y
		y += rows[i].kind.height(h)
	}
	return tops, y + padY
}

// hit returns the row a y coordinate falls in, or -1 when that is no row
// or one that does nothing.
func hit(rows []row, tops []float64, h *rowHeights, y float64) int {
	for i := range rows {
		if y >= tops[i] && y < tops[i]+rows[i].kind.height(h) {
			if rows[i].act == actNone {
				return -1
			}
			return i
		}
	}
	return -1
}
