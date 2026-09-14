package main

import (
	"image"
	"testing"
	"time"

	"github.com/mpdroog/osxflow/internal/upower"
)

func TestIconFor(t *testing.T) {
	charging := macBook()
	charging.battery.State, charging.battery.OnBattery = upower.StateCharging, false
	charging.battery.Percentage = 12
	low := macBook()
	low.battery.Percentage = 19.6 // rounds to 20: low, as macOS draws it
	full := macBook()
	full.battery.State, full.battery.OnBattery, full.battery.Percentage = upower.StateFullyCharged, false, 100

	for _, tc := range []struct {
		name string
		v    view
		want iconState
	}{
		{"no battery", view{}, iconState{}},
		{"discharging", macBook(), iconState{present: true, percent: 46}},
		// Charging is never red, however low.
		{"charging", charging, iconState{present: true, percent: 12, charging: true}},
		{"low", low, iconState{present: true, percent: 20, low: true}},
		{"full on the adapter", full, iconState{present: true, percent: 100, charging: true}},
	} {
		if got := iconFor(&tc.v); got != tc.want {
			t.Errorf("%s: iconFor = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// count reports how many pixels are close to a colour.
func count(img *image.RGBA, r, g, b uint8) int {
	n := 0
	near := func(a, b uint8) bool { return a > b-24 && a < b+24 || a == b }
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i+3] > 0xe0 && near(img.Pix[i], r) && near(img.Pix[i+1], g) && near(img.Pix[i+2], b) {
			n++
		}
	}
	return n
}

func TestRenderIcon(t *testing.T) {
	for _, size := range iconSizes {
		img := renderIcon(size, iconState{present: true, percent: 46})
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("size %d: image is %v", size, img.Bounds())
		}
		if count(img, 0xff, 0xff, 0xff) == 0 {
			t.Errorf("size %d: nothing drawn", size)
		}
	}
	const size = 44
	if count(renderIcon(size, iconState{present: true, percent: 10, low: true}), colLow.R, colLow.G, colLow.B) == 0 {
		t.Error("a low battery has no red")
	}
	if count(renderIcon(size, iconState{present: true, percent: 50, charging: true}), colCharging.R, colCharging.G, colCharging.B) == 0 {
		t.Error("a charging battery has no green")
	}
	more := count(renderIcon(size, iconState{present: true, percent: 90}), 0xff, 0xff, 0xff)
	less := count(renderIcon(size, iconState{present: true, percent: 30}), 0xff, 0xff, 0xff)
	if more <= less {
		t.Errorf("90%% draws %d white pixels, 30%% draws %d; want more at 90%%", more, less)
	}
}

func TestTooltipFor(t *testing.T) {
	v := macBook()
	if tip := tooltipFor(&v); tip.Title != "Battery" || tip.Text != "46%, 1:54 remaining" {
		t.Errorf("tooltip = %q / %q", tip.Title, tip.Text)
	}
	v.battery.TimeToEmpty = 0
	v.battery.State = upower.StateDischarging
	if tip := tooltipFor(&v); tip.Text != "46%" {
		t.Errorf("tooltip without an estimate = %q", tip.Text)
	}
	v.battery.State, v.battery.TimeToFull = upower.StateCharging, 30*time.Minute
	if tip := tooltipFor(&v); tip.Text != "46%, 0:30 until full" {
		t.Errorf("tooltip while charging = %q", tip.Text)
	}
	if tip := tooltipFor(&view{}); tip.Text != "No battery found" {
		t.Errorf("tooltip without a battery = %q", tip.Text)
	}
}
