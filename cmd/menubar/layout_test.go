package main

import (
	"image"
	"slices"
	"testing"
)

func kinds(slots []slot) []slotKind {
	out := make([]slotKind, len(slots))
	for i := range slots {
		out[i] = slots[i].kind
	}
	return out
}

func TestNewMetricsScales(t *testing.T) {
	m1, m2 := newMetrics(1), newMetrics(2)
	if m1.height != baseHeight || m2.height != 2*baseHeight || m2.icon != 2*baseIcon {
		t.Errorf("1x %+v, 2x %+v", m1, m2)
	}
	if newMetrics(0).height != baseHeight {
		t.Error("a zero scale is not treated as 1")
	}
}

func TestLayoutBar(t *testing.T) {
	m := newMetrics(2)
	const width = 2560
	slots := layoutBar(width, m, 150, 250, 3)
	want := []slotKind{slotClock, slotItem, slotItem, slotItem, slotMenu, slotApp}
	if !slices.Equal(kinds(slots), want) {
		t.Fatalf("kinds = %v, want %v", kinds(slots), want)
	}
	if slots[0].r.Max.X != width-m.edge {
		t.Errorf("clock ends at %d, want %d", slots[0].r.Max.X, width-m.edge)
	}
	// Right to left, no overlaps, item 0 next to the clock.
	for i := 1; i < 4; i++ {
		if slots[i].item != i-1 || slots[i].r.Max.X > slots[i-1].r.Min.X {
			t.Errorf("item slot %d = %+v after %+v", i, slots[i], slots[i-1])
		}
		if slots[i].r.Dx() != m.icon+2*m.pad {
			t.Errorf("item slot %d is %d wide", i, slots[i].r.Dx())
		}
	}
	if slots[4].r.Min.X != m.edge {
		t.Errorf("Tux starts at %d", slots[4].r.Min.X)
	}
	if slots[5].r.Dx() != 150+2*m.pad || slots[5].r.Min.X <= slots[4].r.Max.X {
		t.Errorf("app slot %+v", slots[5])
	}
	for i := range slots {
		if slots[i].r.Min.Y != m.insetY || slots[i].r.Max.Y != m.height-m.insetY {
			t.Errorf("slot %d is %v, not inset from the bar's height", i, slots[i].r)
		}
	}
}

// On a narrow bar the application's name gives way to the tray.
func TestLayoutBarCutsTheName(t *testing.T) {
	m := newMetrics(1)
	slots := layoutBar(400, m, 1000, 100, 4)
	app := slices.IndexFunc(slots, func(s slot) bool { return s.kind == slotApp })
	if app < 0 {
		t.Fatal("no app slot")
	}
	if slots[app].r.Max.X > slots[3].r.Min.X {
		t.Errorf("app %v overlaps the last item %v", slots[app].r, slots[3].r)
	}
	slots = layoutBar(200, m, 1000, 100, 4)
	if slices.Contains(kinds(slots), slotApp) {
		t.Error("app slot kept with no room for it")
	}
	if slices.Contains(kinds(layoutBar(2000, m, 0, 100, 0)), slotApp) {
		t.Error("app slot for an empty name")
	}
}

func TestAt(t *testing.T) {
	m := newMetrics(2)
	slots := layoutBar(2560, m, 150, 250, 2)
	menu := slices.IndexFunc(slots, func(s slot) bool { return s.kind == slotMenu })
	for _, tc := range []struct {
		p    image.Point
		want int
	}{
		{image.Pt(0, 0), menu}, // the corner
		{image.Pt(slots[menu].r.Min.X+1, m.height-1), menu},
		{image.Pt(slots[1].r.Min.X, 0), 1}, // the very top edge
		{image.Pt(slots[0].r.Min.X, 20), 0},
		{image.Pt(slots[1].r.Max.X, 20), -1}, // the gap between slots
		{image.Pt(2559, 20), -1},             // beyond the clock's edge
		{image.Pt(1200, 20), -1},
		{image.Pt(slots[1].r.Min.X, m.height), -1},
		{image.Pt(slots[1].r.Min.X, -1), -1},
	} {
		if got := at(slots, tc.p, m.height); got != tc.want {
			t.Errorf("at(%v) = %d, want %d", tc.p, got, tc.want)
		}
	}
}

func TestOrder(t *testing.T) {
	ids := []string{"ezhours", "osxflow-soundmenu", "osxflow-netmenu", "other", "osxflow-powermenu"}
	got := order(ids)
	want := []int{4, 2, 1, 0, 3}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if len(order(nil)) != 0 {
		t.Error("order(nil) not empty")
	}
}

func TestClickable(t *testing.T) {
	for k, want := range map[slotKind]bool{slotMenu: true, slotItem: true, slotApp: false, slotClock: true} {
		s := slot{kind: k}
		if s.clickable() != want {
			t.Errorf("kind %d clickable = %v", k, !want)
		}
	}
}
