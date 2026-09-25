package main

// What the menu lists and what each row does. Kept free of X11 and of
// NetworkManager, so the part a user notices -- which networks show, what
// a click does -- is tested with neither.

import (
	"image"
	"image/color"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/netmgr"
)

// actions is what the menu's rows do.
type actions interface {
	setWifi(on bool)
	join(n *netmgr.Network)
	joinOpen(n *netmgr.Network)
	toggleVPN(v *netmgr.VPN)
	settings()
}

// A glance at what is nearby, not a site survey. The network in use sorts
// first, so a cap never hides it.
const (
	maxKnown = 6
	maxOther = 8
)

// buildRows lays out the menu for a state.
func buildRows(st *netmgr.State, act actions) []menu.Row {
	rows := make([]menu.Row, 0, 8+maxKnown+maxOther+len(st.VPNs))
	if st.WifiDevice != "" {
		rows = append(rows, wifiRows(st, act)...)
	}
	if len(st.VPNs) > 0 {
		if len(rows) > 0 {
			rows = append(rows, menu.Row{Kind: menu.Separator})
		}
		rows = append(rows, menu.Row{Kind: menu.Section, Label: "VPN"})
		for i := range st.VPNs {
			v := st.VPNs[i]
			rows = append(rows, menu.Row{
				Kind:     menu.Item,
				Label:    v.Name,
				Detail:   vpnDetail(&v),
				On:       v.On(),
				Glyph:    menu.Symbol(glyph.Lock),
				Trailing: menu.TrailingSwitch,
				// A switch stays open to show itself flip, as on macOS.
				KeepOpen: true,
				Click:    func() { act.toggleVPN(&v) },
			})
		}
	}
	if len(rows) > 0 {
		rows = append(rows, menu.Row{Kind: menu.Separator})
	}
	// The address last, above Settings, the way macOS keeps the details of
	// a connection below the list of them. It is the one thing here that
	// answers a question rather than offering an action -- "what is my
	// IP" -- and waybar's network module, which this bar replaced, put it
	// on the bar itself.
	if st.Address != "" {
		rows = append(rows,
			menu.Row{Kind: menu.Separator},
			menu.Row{Kind: menu.Header, Label: "IP Address", Detail: st.Address})
	}
	return append(rows, menu.Row{Kind: menu.Action, Label: "Network Settings…", Click: act.settings})
}

func wifiRows(st *netmgr.State, act actions) []menu.Row {
	on := st.WifiEnabled && st.WifiHardware
	toggle := menu.Row{Kind: menu.Toggle, Label: "Wi-Fi", On: on, KeepOpen: true}
	if !st.WifiHardware {
		// The software switch cannot turn on a radio a hardware switch has
		// off, so it is shown but does nothing.
		return []menu.Row{toggle, {Kind: menu.Note, Label: "Turned off by a hardware switch"}}
	}
	toggle.Click = func() { act.setWifi(!on) }
	if !st.WifiEnabled {
		return []menu.Row{toggle}
	}

	var known, other []menu.Row
	for i := range st.Networks {
		n := st.Networks[i]
		r := menu.Row{Kind: menu.Item, Label: n.Name, On: n.Connected(), Glyph: wifiGlyph(netmgr.Bars(n.Strength))}
		if n.Secured {
			r.Trailing = menu.TrailingLock
		}
		switch {
		case n.Connected():
		case n.Connecting():
			r.Detail = "Connecting…"
		case n.Saved != "":
			r.Click = func() { act.join(&n) }
		case n.Secured:
			// Joining needs a password, and there is no prompt here by
			// design: nm-connection-editor asks for it and saves it, after
			// which the network is a known one.
			r.Click = act.settings
		default:
			r.Click = func() { act.joinOpen(&n) }
		}
		if n.Saved != "" {
			if len(known) < maxKnown {
				known = append(known, r)
			}
		} else if len(other) < maxOther {
			other = append(other, r)
		}
	}

	rows := make([]menu.Row, 0, 3+len(known)+len(other))
	rows = append(rows, toggle)
	if len(known)+len(other) == 0 {
		return append(rows, menu.Row{Kind: menu.Note, Label: "Looking for networks…"})
	}
	if len(known) > 0 {
		rows = append(rows, menu.Row{Kind: menu.Section, Label: "Known Networks"})
		rows = append(rows, known...)
	}
	if len(other) > 0 {
		rows = append(rows, menu.Row{Kind: menu.Section, Label: "Other Networks"})
		rows = append(rows, other...)
	}
	return rows
}

// wifiGlyph is a network's badge: the Wi-Fi symbol with its signal lit.
func wifiGlyph(bars int) menu.Glyph {
	return func(dst *image.RGBA, x0, y0, s float64, col color.RGBA) {
		glyph.Wifi(dst, x0, y0, s, bars, col, glyph.Dim(col, 0.33))
	}
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
