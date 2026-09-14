package main

// The tray icon, and what it and its tooltip say.

import (
	"image"
	"strings"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/netmgr"
	"github.com/mpdroog/osxflow/internal/sni"
)

// iconSizes are the pixmaps handed to the tray, which picks the one
// closest to what it draws. The panel here is 28 logical pixels at 2x, so
// 44 is the one that gets used; the others are for other trays and
// scales.
var iconSizes = [...]int{22, 32, 44, 64}

// levelOff marks Wi-Fi turned off, or no Wi-Fi at all.
const levelOff = -1

// iconState is everything the icon shows. It is comparable, so the icon is
// redrawn only when it would look different.
type iconState struct {
	// level is levelOff, 0 when on but not connected, or 1 to 3 bars.
	level      int
	connecting bool
	wired      bool
	vpn        bool
}

func iconFor(st *netmgr.State) iconState {
	s := iconState{wired: st.Wired}
	for i := range st.VPNs {
		if st.VPNs[i].Active != "" && st.VPNs[i].State == netmgr.StateActivated {
			s.vpn = true
		}
	}
	if st.WifiDevice == "" || !st.WifiEnabled || !st.WifiHardware {
		s.level = levelOff
		return s
	}
	if n := st.Wifi(); n != nil {
		if n.Connected() {
			s.level = netmgr.Bars(n.Strength)
		} else {
			s.connecting = true
		}
	}
	return s
}

// renderIcon draws the tray icon at one size.
//
// Wi-Fi is macOS's symbol with the unused arcs dimmed rather than removed,
// so the shape stays recognisable at one bar. Joining lights only the dot.
// An Ethernet connection with no Wi-Fi in use shows the wired symbol
// instead. A VPN that is up adds a padlock in the corner.
func renderIcon(size int, s iconState) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	b := img.Bounds()
	side := float64(size)

	switch {
	case s.wired && s.level <= 0 && !s.connecting:
		glyph.Fill(img, b, colIconOn, glyph.Wired(0, 0, side))
	case s.level == levelOff:
		glyph.Wifi(img, 0, 0, side, -1, colIconOn, colIconOff)
	case s.connecting:
		glyph.Wifi(img, 0, 0, side, 0, colIconOn, colIconDim)
	case s.level == 0:
		glyph.Wifi(img, 0, 0, side, -1, colIconOn, colIconDim)
	default:
		glyph.Wifi(img, 0, 0, side, s.level, colIconOn, colIconDim)
	}

	if s.vpn {
		badge := side * 0.5
		shape := glyph.Lock(side-badge, side-badge, badge)
		glyph.Erase(img, b, side*0.07, shape)
		glyph.Fill(img, b, colIconOn, shape)
	}
	return img
}

func iconPixmaps(s iconState) []sni.Pixmap {
	out := make([]sni.Pixmap, 0, len(iconSizes))
	for _, size := range iconSizes {
		out = append(out, sni.FromImage(renderIcon(size, s)))
	}
	return out
}

// tooltipFor says in words what the icon shows.
func tooltipFor(st *netmgr.State) sni.ToolTip {
	lines := make([]string, 0, 2+len(st.VPNs))
	switch n := st.Wifi(); {
	case st.WifiDevice == "":
	case !st.WifiEnabled || !st.WifiHardware:
		lines = append(lines, "Wi-Fi: off")
	case n == nil:
		lines = append(lines, "Wi-Fi: not connected")
	case n.Connecting():
		lines = append(lines, "Wi-Fi: connecting to "+n.Name)
	default:
		lines = append(lines, "Wi-Fi: "+n.Name)
	}
	if st.Wired {
		lines = append(lines, "Ethernet: connected")
	}
	for i := range st.VPNs {
		v := &st.VPNs[i]
		switch {
		case v.Active == "":
		case v.State == netmgr.StateActivated:
			lines = append(lines, "VPN: "+v.Name)
		case v.State == netmgr.StateActivating:
			lines = append(lines, "VPN: connecting to "+v.Name)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No network")
	}
	return sni.ToolTip{Title: "Network", Text: strings.Join(lines, "\n")}
}
