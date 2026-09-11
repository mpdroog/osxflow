package main

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/text"
)

func testFaces(t *testing.T, th *theme) *faces {
	t.Helper()
	f, err := loadFaces(th)
	if err != nil {
		t.Skipf("no usable system font: %v", err)
	}
	t.Cleanup(f.close)
	return f
}

func setup(t *testing.T, scale float64) (*theme, *faces) {
	t.Helper()
	th := newTheme(scale)
	return th, testFaces(t, th)
}

func TestAShortNotificationIsAsTallAsItsIcon(t *testing.T) {
	th, f := setup(t, 1)
	l := measure(th, f, &notify.Notification{Summary: "Hello", Value: -1})
	if got, want := l.plate.Dy(), th.icon+2*th.pad; got != want {
		t.Errorf("plate height = %d, want icon plus padding %d", got, want)
	}
	if got, want := l.size, image.Pt(th.width+2*th.winPadX, l.plate.Max.Y+th.winPadY); got != want {
		t.Errorf("window = %v, want %v", got, want)
	}
	// Centred against the icon, not hung from its top edge.
	if l.titleBase <= l.icon.Min.Y+th.icon/3 || l.titleBase >= l.icon.Max.Y {
		t.Errorf("title baseline %d is not in the middle of the icon band %v", l.titleBase, l.icon)
	}
}

func TestALongBodyWrapsToTheLineLimit(t *testing.T) {
	th, f := setup(t, 2)
	l := measure(th, f, &notify.Notification{Summary: "S", Body: strings.Repeat("lorem ipsum ", 100), Value: -1})
	if len(l.body) != bodyLines {
		t.Fatalf("%d body lines, want %d", len(l.body), bodyLines)
	}
	for _, line := range l.body {
		if w := text.Width(f.body, line); w > l.textW {
			t.Errorf("line %q is %dpx, wider than the text column's %d", line, w, l.textW)
		}
	}
	if !strings.HasSuffix(l.body[bodyLines-1], "…") {
		t.Errorf("last line %q does not say the text was cut", l.body[bodyLines-1])
	}
	for i := 1; i < len(l.bodyBase); i++ {
		if got := l.bodyBase[i] - l.bodyBase[i-1]; got != th.lineH {
			t.Errorf("baselines %d and %d are %dpx apart, want %d", i-1, i, got, th.lineH)
		}
	}
	if last := l.bodyBase[len(l.bodyBase)-1]; last >= l.plate.Max.Y-th.pad/2 {
		t.Errorf("last baseline %d is not inside the plate %v", last, l.plate)
	}
}

func TestButtonsShareTheRowBelowTheContent(t *testing.T) {
	th, f := setup(t, 1)
	n := &notify.Notification{Summary: "Call", Value: -1, Actions: []notify.Action{
		{Key: notify.DefaultAction},
		{Key: "accept", Label: "Accept"},
		{Key: "decline", Label: "Decline"},
	}}
	l := measure(th, f, n)
	if len(l.buttons) != 2 || l.buttons[0].key != "accept" || l.buttons[1].key != "decline" {
		t.Fatalf("buttons = %+v, want accept and decline, and no button for the default", l.buttons)
	}
	a, b := l.buttons[0].rect, l.buttons[1].rect
	if a.Dx() != b.Dx() || a.Max.X > b.Min.X {
		t.Errorf("buttons %v and %v are not two equal, separate halves", a, b)
	}
	for _, r := range []image.Rectangle{a, b} {
		if !r.In(l.plate) {
			t.Errorf("button %v is outside the plate %v", r, l.plate)
		}
		if r.Min.Y < l.icon.Max.Y {
			t.Errorf("button %v overlaps the content above it", r)
		}
	}
}

func TestButtonsAreCapped(t *testing.T) {
	th, f := setup(t, 1)
	n := &notify.Notification{Summary: "Many", Value: -1}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		n.Actions = append(n.Actions, notify.Action{Key: k, Label: k})
	}
	if l := measure(th, f, n); len(l.buttons) != maxButtons {
		t.Fatalf("%d buttons, want %d", len(l.buttons), maxButtons)
	}
}

func TestValueBar(t *testing.T) {
	th, f := setup(t, 1)
	without := measure(th, f, &notify.Notification{Summary: "Volume", Value: -1})
	if !without.bar.Empty() {
		t.Error("a bar was laid out for a notification without a value")
	}
	with := measure(th, f, &notify.Notification{Summary: "Volume", Value: 40})
	if with.bar.Empty() || with.bar.Dx() != with.textW || !with.bar.In(with.plate) {
		t.Errorf("bar = %v, want the width of the text column inside %v", with.bar, with.plate)
	}
}

func TestHit(t *testing.T) {
	th, f := setup(t, 1)
	l := measure(th, f, &notify.Notification{Summary: "S", Body: "B", Value: -1,
		Actions: []notify.Action{{Key: "open", Label: "Open"}}})
	btn := l.buttons[0].rect
	for _, tc := range []struct {
		name string
		x, y int
		want part
	}{
		{"close button", l.closeX, l.closeY, part{kind: partClose}},
		{"just outside the close button", l.closeX + int(l.closeR) + 1, l.closeY, part{kind: partClose}},
		{"action button", btn.Min.X + btn.Dx()/2, btn.Min.Y + btn.Dy()/2, part{kind: partButton}},
		{"text", l.textX + 5, l.titleBase, part{kind: partBody}},
		{"transparent margin", 1, l.size.Y - 1, part{}},
	} {
		if got := l.hit(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: hit(%d, %d) = %+v, want %+v", tc.name, tc.x, tc.y, got, tc.want)
		}
	}
}

func TestThemeScales(t *testing.T) {
	one, two := newTheme(1), newTheme(2)
	if two.width != 2*one.width || two.icon != 2*one.icon {
		t.Errorf("at 2x: width %d icon %d, want double %d and %d", two.width, two.icon, one.width, one.icon)
	}
	for _, s := range []float64{1, 1.25, 1.5, 2, 3} {
		th := newTheme(s)
		if th.gap < 2*th.winPadY {
			t.Errorf("at %gx the gap %d lets resting windows overlap (margins %d)", s, th.gap, th.winPadY)
		}
		if th.shadow > th.winPadY {
			t.Errorf("at %gx the shadow %d is cut off by the %d margin", s, th.shadow, th.winPadY)
		}
	}
	if newTheme(0).scale != 1 {
		t.Error("a zero scale was not treated as 1")
	}
}

func TestFitKeepsProportions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := range src.Pix {
		src.Pix[i] = 0xff
	}
	dst := fit(src, 40)
	if dst.Bounds() != image.Rect(0, 0, 40, 40) {
		t.Fatalf("bounds = %v, want 40x40", dst.Bounds())
	}
	// A 2:1 image in a square: 20 pixels tall, centred.
	if got := dst.RGBAAt(20, 5); got != (color.RGBA{}) {
		t.Errorf("pixel above the image = %v, want transparent", got)
	}
	if got := dst.RGBAAt(20, 20); got.A != 0xff {
		t.Errorf("pixel in the image = %v, want opaque", got)
	}
	if fit(image.NewRGBA(image.Rectangle{}), 40) != nil {
		t.Error("fit made something of an empty image")
	}
}
