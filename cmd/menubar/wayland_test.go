package main

import (
	"testing"

	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/wl"
)

func TestButtonMapsKernelCodesToXButtons(t *testing.T) {
	for _, tc := range []struct {
		name string
		code uint32
		want xproto.Button
	}{
		{"left", wl.ButtonLeft, xproto.ButtonIndex1},
		{"right", 0x111, xproto.ButtonIndex3},
		{"middle", 0x112, xproto.ButtonIndex2},
		// A side button on a gaming mouse is not a button any menu here
		// has a meaning for, and must not be mistaken for one that is.
		{"side", 0x113, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := button(tc.code); got != tc.want {
				t.Errorf("button(%#x) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestBarToScreenCrossesMonitorsAndScales(t *testing.T) {
	for _, tc := range []struct {
		name  string
		x     int
		monX  int
		scale float64
		want  int
	}{
		// The left-hand monitor at scale 1: the two spaces are the same.
		{"unscaled at the origin", 1200, 0, 1, 1200},
		// The 4K monitor: 1500 device pixels along a bar at 1.5 is 1000
		// logical pixels in, and that monitor starts 3440 across.
		{"scaled and offset", 1500, 3440, 1.5, 4440},
		{"left edge is the monitor's own", 0, 3440, 1.5, 3440},
		// A scale that never arrived must not divide by zero.
		{"no scale yet", 100, 0, 0, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := barToScreen(tc.x, tc.monX, tc.scale); got != tc.want {
				t.Errorf("barToScreen(%d, %d, %v) = %d, want %d", tc.x, tc.monX, tc.scale, got, tc.want)
			}
		})
	}
}
