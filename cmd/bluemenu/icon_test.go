package main

import (
	"image"
	"testing"
)

func TestIconFor(t *testing.T) {
	off := on()
	off.soft = true
	idle := on()
	idle.state.Devices[0].Connected = false
	for _, tc := range []struct {
		name string
		v    view
		want iconState
	}{
		{"connected", on(), iconState{on: true, connected: true}},
		{"on, nothing connected", idle, iconState{on: true}},
		// Off is off, whatever BlueZ last said was connected.
		{"off", off, iconState{}},
		{"not running", view{}, iconState{}},
	} {
		if got := iconFor(&tc.v); got != tc.want {
			t.Errorf("%s: iconFor = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTooltipFor(t *testing.T) {
	v := on()
	if got := tooltipFor(&v).Text; got != "Bluetooth: on\nConnected: Razer Kraken (80%)" {
		t.Errorf("tooltip on = %q", got)
	}
	v.soft = true
	if got := tooltipFor(&v).Text; got != "Bluetooth: off" {
		t.Errorf("tooltip off = %q", got)
	}
	v = on()
	v.state.Adapter = nil
	if got := tooltipFor(&v).Text; got != "Bluetooth adapter not responding" {
		t.Errorf("tooltip stuck = %q", got)
	}
}

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
		onImg := renderIcon(size, iconState{on: true})
		if onImg.Bounds().Dx() != size || opaque(onImg) == 0 {
			t.Errorf("size %d: bounds %v, %d opaque pixels", size, onImg.Bounds(), opaque(onImg))
		}
		if n := opaque(renderIcon(size, iconState{})); n != 0 {
			t.Errorf("size %d: off icon has %d opaque pixels, want faint only", size, n)
		}
	}
	// The connection dot sits in the bottom-right corner.
	const size = 44
	plain, dotted := renderIcon(size, iconState{on: true}), renderIcon(size, iconState{on: true, connected: true})
	if dotted.RGBAAt(size*84/100, size*84/100).A < 0xe0 {
		t.Error("no dot at the bottom right when connected")
	}
	if plain.RGBAAt(size*84/100, size*84/100).A >= 0xe0 {
		t.Error("a dot at the bottom right with nothing connected")
	}
}
