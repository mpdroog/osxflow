package main

// What the menu lists and what each row does. Kept free of X11 and of
// NetworkManager, so the part a user notices -- which networks show, what
// a click does -- is tested with neither.

import (
	"bytes"
	"errors"
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
	// askPassword opens the password field under a network, typePassword
	// is what has been typed into it so far, and joinSecured is Return.
	askPassword(n *netmgr.Network)
	typePassword(text string)
	joinSecured(n *netmgr.Network, password string)
	toggleVPN(v *netmgr.VPN)
	settings()
}

// prompt is the password field open under a network that has no saved
// profile yet, as macOS opens one in its menu.
type prompt struct {
	// ssid is the network being asked about, as broadcast.
	ssid []byte
	text string

	// note is a line under the field: why the last attempt did not work.
	note string

	// failed marks a prompt put back by a join that did not work, which
	// outlives the menu closing: that is how the note gets read.
	failed bool
}

// Notes a prompt can carry.
const (
	noteBadLength = "A password is 8 to 63 characters"
	noteRefused   = "Couldn't join. Check the password."
	noteFailed    = "Couldn't join this network"
)

// noteFor is what to say under the field after a join failed with err.
// Only NetworkManager giving up on the connection points at the password;
// anything else -- no permission, no answer -- is not something retyping
// it fixes.
func noteFor(err error) string {
	if errors.Is(err, netmgr.ErrJoinFailed) {
		return noteRefused
	}
	return noteFailed
}

// asks reports whether the prompt is open for n.
func (p *prompt) asks(n *netmgr.Network) bool {
	return p != nil && len(p.ssid) > 0 && bytes.Equal(p.ssid, n.Raw)
}

// A glance at what is nearby, not a site survey. The network in use sorts
// first, so a cap never hides it.
const (
	maxKnown = 6
	maxOther = 8
)

// buildRows lays out the menu for a state. p is the password prompt, or
// nil when none is open.
func buildRows(st *netmgr.State, p *prompt, act actions) []menu.Row {
	rows := make([]menu.Row, 0, 10+maxKnown+maxOther+len(st.VPNs))
	if st.WifiDevice != "" {
		rows = append(rows, wifiRows(st, p, act)...)
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

func wifiRows(st *netmgr.State, p *prompt, act actions) []menu.Row {
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

	// others counts the networks in other, which also holds the prompt's
	// rows.
	var known, other []menu.Row
	others := 0
	for i := range st.Networks {
		n := st.Networks[i]
		r := menu.Row{Kind: menu.Item, Label: n.Name, On: n.Connected(), Glyph: wifiGlyph(netmgr.Bars(n.Strength))}
		if n.Secured {
			r.Trailing = menu.TrailingLock
		}
		asking := false
		switch {
		case n.Connected():
		case n.Connecting():
			r.Detail = "Connecting…"
		case n.Saved != "":
			r.Click = func() { act.join(&n) }
		case n.Secured && !n.Security.Passphrase():
			// More than one password can answer -- an enterprise login,
			// say -- and nm-connection-editor is where that is set up.
			r.Click = act.settings
		case n.Secured:
			// The field opens under the network, in the menu, which stays
			// open for it. With it open the row has nothing left to do.
			if asking = p.asks(&n); !asking {
				r.Click, r.KeepOpen = func() { act.askPassword(&n) }, true
			}
		default:
			r.Click = func() { act.joinOpen(&n) }
		}
		if n.Saved != "" {
			if len(known) < maxKnown {
				known = append(known, r)
			}
			continue
		}
		// The network being asked about is listed whatever the cap: a scan
		// finishing must not take the field away from under the typing.
		if others >= maxOther && !asking {
			continue
		}
		others++
		other = append(other, r)
		if !asking {
			continue
		}
		other = append(other, menu.Row{
			Kind: menu.Input, Label: "Password", Text: p.text, Secret: true,
			Edit:   act.typePassword,
			Submit: func(text string) { act.joinSecured(&n, text) },
		})
		if p.note != "" {
			other = append(other, menu.Row{Kind: menu.Note, Label: p.note})
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
