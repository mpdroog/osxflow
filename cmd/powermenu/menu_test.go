package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mpdroog/osxflow/internal/backlight"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/upower"
)

type fakeActions struct{ calls []string }

func (f *fakeActions) setScreen(v float64, final bool) {
	f.calls = append(f.calls, fmt.Sprintf("screen %.2f %t", v, final))
}

func (f *fakeActions) setKeyboard(v float64, final bool) {
	f.calls = append(f.calls, fmt.Sprintf("keyboard %.2f %t", v, final))
}
func (f *fakeActions) setPresentation(on bool) {
	f.calls = append(f.calls, fmt.Sprintf("presentation %t", on))
}
func (f *fakeActions) settings() { f.calls = append(f.calls, "settings") }

func kinds(rows []menu.Row) string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = fmt.Sprintf("%d:%s", rows[i].Kind, rows[i].Label)
	}
	return strings.Join(out, "|")
}

func want(parts ...string) string { return strings.Join(parts, "|") }

func row(k menu.Kind, label string) string { return fmt.Sprintf("%d:%s", k, label) }

// macBook is this machine as it was when powermenu was written: 46%,
// discharging, 1:54 left, a battery at 67% of its design capacity.
func macBook() view {
	return view{
		battery: upower.Battery{
			Percentage:  45.53,
			State:       upower.StateDischarging,
			TimeToEmpty: 114 * time.Minute,
			Capacity:    67.2651,
			Cycles:      424,
			OnBattery:   true,
		},
		hasBattery: true,
		screen:     &backlight.Device{Subsystem: "backlight", Name: "acpi_video0", Max: 90, Brightness: 9, MinRaw: 1},
		keyboard:   &backlight.Device{Subsystem: "leds", Name: "spi::kbd_backlight", Max: 255, Brightness: 36},
	}
}

func TestBuildRows(t *testing.T) {
	v := macBook()
	f := &fakeActions{}
	rows := buildRows(&v, f)
	if got := kinds(rows); got != want(
		row(menu.Header, "Battery"),
		row(menu.Note, "1:54 remaining"),
		row(menu.Note, "Power source: Battery"),
		row(menu.Note, "Health 67% · 424 cycles"),
		row(menu.Separator, ""),
		row(menu.Section, "Display"),
		row(menu.Slider, ""),
		row(menu.Section, "Keyboard"),
		row(menu.Slider, ""),
		row(menu.Separator, ""),
		row(menu.Item, "Presentation mode"),
		row(menu.Separator, ""),
		row(menu.Action, "Power Settings…"),
	) {
		t.Fatalf("rows:\n%s", got)
	}
	if rows[0].Detail != "46%" {
		t.Errorf("header detail = %q, want 46%%", rows[0].Detail)
	}
	if rows[6].Value != 0.1 || rows[8].Value != 36.0/255 {
		t.Errorf("slider values %v and %v, want 0.1 and %v", rows[6].Value, rows[8].Value, 36.0/255)
	}

	rows[6].Slide(0.5, false)
	rows[8].Slide(1, true)
	rows[10].Click()
	rows[12].Click()
	if got := strings.Join(f.calls, "; "); got != "screen 0.50 false; keyboard 1.00 true; presentation true; settings" {
		t.Errorf("actions: %s", got)
	}
	if !rows[10].KeepOpen || rows[10].Trailing != menu.TrailingSwitch || rows[10].On {
		t.Errorf("presentation row: keepOpen %t trailing %d on %t", rows[10].KeepOpen, rows[10].Trailing, rows[10].On)
	}
	if rows[12].KeepOpen {
		t.Error("opening settings should close the menu")
	}

	v.presentation = true
	f.calls = nil
	rows = buildRows(&v, f)
	rows[10].Click()
	if !rows[10].On || f.calls[0] != "presentation false" {
		t.Errorf("with presentation mode on: on %t, click asked for %v", rows[10].On, f.calls)
	}
}

func TestBuildRowsCharging(t *testing.T) {
	v := macBook()
	v.battery.State = upower.StateCharging
	v.battery.OnBattery = false
	v.battery.TimeToEmpty, v.battery.TimeToFull = 0, 65*time.Minute
	got := kinds(buildRows(&v, &fakeActions{}))
	for _, part := range []string{row(menu.Note, "1:05 until full"), row(menu.Note, "Power source: Power adapter")} {
		if !strings.Contains(got, part) {
			t.Errorf("charging rows lack %q:\n%s", part, got)
		}
	}
}

func TestBuildRowsWithout(t *testing.T) {
	v := view{}
	got := kinds(buildRows(&v, &fakeActions{}))
	if got != want(
		row(menu.Header, "Battery"),
		row(menu.Note, "No battery found"),
		row(menu.Separator, ""),
		row(menu.Item, "Presentation mode"),
		row(menu.Separator, ""),
		row(menu.Action, "Power Settings…"),
	) {
		t.Errorf("rows with nothing:\n%s", got)
	}

	v = macBook()
	v.keyboard = nil
	if got := kinds(buildRows(&v, &fakeActions{})); strings.Contains(got, "Keyboard") || !strings.Contains(got, "Display") {
		t.Errorf("rows without a keyboard light:\n%s", got)
	}
	// No health figures: no health line, rather than an empty one.
	v.battery.Capacity, v.battery.Cycles = 0, -1
	if got := kinds(buildRows(&v, &fakeActions{})); strings.Contains(got, "Health") {
		t.Errorf("rows with unknown health:\n%s", got)
	}
}
