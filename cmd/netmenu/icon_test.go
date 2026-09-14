package main

import (
	"image"
	"math"
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/netmgr"
)

func TestIconFor(t *testing.T) {
	on := netmgr.State{WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true}
	connected := on
	connected.Networks = []netmgr.Network{{Name: "home", Active: "/a/1", State: netmgr.StateActivated, Strength: 45}}
	joining := on
	joining.Networks = []netmgr.Network{{Name: "home", Active: "/a/1", State: netmgr.StateActivating}}
	withVPN := connected
	withVPN.VPNs = []netmgr.VPN{{Name: "XSN", Active: "/a/9", State: netmgr.StateActivated}}
	vpnComing := connected
	vpnComing.VPNs = []netmgr.VPN{{Name: "XSN", Active: "/a/9", State: netmgr.StateActivating}}
	off := on
	off.WifiEnabled = false
	hardwareOff := on
	hardwareOff.WifiHardware = false
	wiredOnly := netmgr.State{Wired: true}

	for _, tc := range []struct {
		name string
		st   netmgr.State
		want iconState
	}{
		{"no device", netmgr.State{}, iconState{level: levelOff}},
		{"on, not connected", on, iconState{}},
		{"connected", connected, iconState{level: 2}},
		{"joining", joining, iconState{connecting: true}},
		{"VPN up", withVPN, iconState{level: 2, vpn: true}},
		// The padlock means the VPN protects traffic, which it does not yet.
		{"VPN coming up", vpnComing, iconState{level: 2}},
		{"off", off, iconState{level: levelOff}},
		{"hardware off", hardwareOff, iconState{level: levelOff}},
		{"wired", wiredOnly, iconState{level: levelOff, wired: true}},
	} {
		if got := iconFor(&tc.st); got != tc.want {
			t.Errorf("%s: iconFor = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTooltipFor(t *testing.T) {
	st := netmgr.State{
		WifiDevice: "/d/2", WifiEnabled: true, WifiHardware: true,
		Networks: []netmgr.Network{{Name: "home", Active: "/a/1", State: netmgr.StateActivated}},
		VPNs: []netmgr.VPN{
			{Name: "XSN", Active: "/a/9", State: netmgr.StateActivated},
			{Name: "Work", Active: "/a/8", State: netmgr.StateActivating},
			{Name: "Idle"},
		},
		Wired: true,
	}
	tip := tooltipFor(&st)
	want := "Wi-Fi: home\nEthernet: connected\nVPN: XSN\nVPN: connecting to Work"
	if tip.Title != "Network" || tip.Text != want {
		t.Errorf("tooltip = %q / %q, want Network / %q", tip.Title, tip.Text, want)
	}
	if got := tooltipFor(&netmgr.State{}).Text; got != "No network" {
		t.Errorf("tooltip with nothing = %q", got)
	}
	st.WifiEnabled = false
	if got := tooltipFor(&st).Text; !strings.HasPrefix(got, "Wi-Fi: off\n") {
		t.Errorf("tooltip with Wi-Fi off = %q", got)
	}
}

// opaque counts pixels at least nearly opaque: lit parts of the icon.
func opaque(img *image.RGBA) int {
	n := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0xe0 {
			n++
		}
	}
	return n
}

func TestRenderIcon(t *testing.T) {
	for _, size := range iconSizes {
		prev := -1
		for level := 1; level <= 3; level++ {
			img := renderIcon(size, iconState{level: level})
			if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
				t.Fatalf("size %d: image is %v", size, img.Bounds())
			}
			lit := opaque(img)
			if lit <= prev {
				t.Errorf("size %d: level %d lights %d pixels, no more than level %d's %d", size, level, lit, level-1, prev)
			}
			prev = lit
			// The corners are outside the symbol.
			if a := img.RGBAAt(0, 0).A; a != 0 {
				t.Errorf("size %d level %d: top-left corner alpha %d", size, level, a)
			}
		}
		if lit := opaque(renderIcon(size, iconState{level: levelOff})); lit != 0 {
			t.Errorf("size %d: Wi-Fi off lights %d pixels", size, lit)
		}
		if lit := opaque(renderIcon(size, iconState{})); lit != 0 {
			t.Errorf("size %d: not connected lights %d pixels", size, lit)
		}
		if opaque(renderIcon(size, iconState{connecting: true})) == 0 {
			t.Errorf("size %d: joining lights nothing", size)
		}
		if opaque(renderIcon(size, iconState{wired: true})) == 0 {
			t.Errorf("size %d: wired lights nothing", size)
		}
	}
}

// The padlock goes in the bottom-right corner and nowhere else.
func TestRenderIconVPN(t *testing.T) {
	const size = 44
	plain := renderIcon(size, iconState{level: levelOff})
	badged := renderIcon(size, iconState{level: levelOff, vpn: true})
	quadrant := image.Rect(size/2, size/2, size, size)
	for y := range size {
		for x := range size {
			changed := plain.RGBAAt(x, y) != badged.RGBAAt(x, y)
			// Generous: the lock's margin reaches a pixel or two past the
			// quadrant's edge.
			if changed && !image.Pt(x, y).In(quadrant.Inset(-3)) {
				t.Fatalf("VPN badge changed pixel %d,%d outside the corner", x, y)
			}
		}
	}
	if opaque(badged) == 0 {
		t.Error("the padlock lights nothing")
	}
}

func TestShapes(t *testing.T) {
	b := box(10, 10, 5, 3, 1)
	if d := b(10, 10); d >= 0 {
		t.Errorf("box centre distance %v, want negative", d)
	}
	if d := b(20, 10); math.Abs(d-5) > 1e-9 {
		t.Errorf("box distance 5 to the right of its edge = %v", d)
	}
	c := circle(0, 0, 2)
	if d := c(3, 4); math.Abs(d-3) > 1e-9 {
		t.Errorf("circle distance = %v, want 3", d)
	}
	if d := union(c, b)(10, 10); d >= 0 {
		t.Errorf("union inside the box = %v", d)
	}
	for _, tc := range []struct {
		d, want float64
	}{{-5, 1}, {0, 0.5}, {5, 0}} {
		if got := coverage(tc.d); got != tc.want {
			t.Errorf("coverage(%v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}
