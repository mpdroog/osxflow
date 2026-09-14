package menu

import (
	"math"
	"testing"
)

func noop()                 {}
func noslide(float64, bool) {}

func TestNewThemeScales(t *testing.T) {
	one, two := NewTheme(1, 300), NewTheme(2, 300)
	if one.Width != 300 || two.Width != 600 {
		t.Errorf("widths %d and %d, want 300 and 600", one.Width, two.Width)
	}
	if two.height(Item) != 2*one.height(Item) || two.textPt != 2*one.textPt {
		t.Error("a 2x theme is not twice a 1x one")
	}
	if NewTheme(0, 300).Scale != 1 || NewTheme(-3, 300).Scale != 1 {
		t.Error("a scale of zero or less is not treated as 1")
	}
	// An out-of-range kind gets a sensible height, not a panic.
	if got := one.height(Kind(99)); got != one.height(Item) {
		t.Errorf("height of an unknown kind = %v", got)
	}
}

func TestLayout(t *testing.T) {
	th := NewTheme(1, 300)
	rows := []Row{{Kind: Toggle}, {Kind: Section}, {Kind: Slider}, {Kind: Separator}, {Kind: Action}}
	tops, total := layout(rows, th)
	want := []float64{6, 44, 68, 102, 113}
	for i := range want {
		if tops[i] != want[i] {
			t.Fatalf("tops = %v, want %v", tops, want)
		}
	}
	if total != 149 {
		t.Errorf("total = %v, want 149", total)
	}
}

func TestInteractive(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  Row
		want bool
	}{
		{"item with click", Row{Kind: Item, Click: noop}, true},
		{"item without", Row{Kind: Item}, false},
		// Kinds that never do anything stay inert even given a click.
		{"section", Row{Kind: Section, Click: noop}, false},
		{"note", Row{Kind: Note, Click: noop}, false},
		{"separator", Row{Kind: Separator, Click: noop}, false},
		{"slider with slide", Row{Kind: Slider, Slide: noslide}, true},
		{"slider with only a click", Row{Kind: Slider, Click: noop}, false},
		{"transport, all inert", Row{Kind: Transport, Buttons: []Button{{}, {}}}, false},
		{"transport, one live", Row{Kind: Transport, Buttons: []Button{{}, {Click: noop}}}, true},
	} {
		if got := tc.row.interactive(); got != tc.want {
			t.Errorf("%s: interactive = %t, want %t", tc.name, got, tc.want)
		}
	}
	if (&Row{Kind: Slider, Slide: noslide}).hovers() || !(&Row{Kind: Action, Click: noop}).hovers() {
		t.Error("hovers: sliders must not light up, live actions must")
	}
}

func TestHit(t *testing.T) {
	th := NewTheme(1, 300)
	rows := []Row{
		{Kind: Toggle, Click: noop}, // 6..44
		{Kind: Section},             // 44..68
		{Kind: Transport, Buttons: []Button{{Click: noop}, {}, {Click: noop}}}, // 68..108
		{Kind: Slider, Slide: noslide},                                         // 108..142
	}
	tops, _ := layout(rows, th)
	centres := buttonCentres(th, 3)
	for _, tc := range []struct {
		name        string
		x, y        float64
		row, button int
	}{
		{"padding", 150, 2, -1, -1},
		{"toggle", 150, 20, 0, -1},
		{"section", 150, 50, -1, -1},
		{"first button", centres[0], 88, 2, 0},
		{"inert middle button", centres[1], 88, -1, -1},
		{"last button", centres[2] + th.button/2, 88, 2, 2},
		{"between buttons", (centres[0] + centres[1]) / 2, 88, -1, -1},
		{"slider", 20, 120, 3, -1},
		{"below everything", 150, 500, -1, -1},
	} {
		row, button := hit(rows, tops, th, tc.x, tc.y)
		if row != tc.row || button != tc.button {
			t.Errorf("%s: hit = %d, %d; want %d, %d", tc.name, row, button, tc.row, tc.button)
		}
	}
}

func TestButtonCentres(t *testing.T) {
	th := NewTheme(1, 300)
	c := buttonCentres(th, 3)
	if c[1] != 150 || math.Abs((c[1]-c[0])-(c[2]-c[1])) > 1e-9 || c[2]-c[1] <= th.button {
		t.Errorf("centres %v: want the middle at 150, even spacing wider than a button", c)
	}
	if one := buttonCentres(th, 1); one[0] != 150 {
		t.Errorf("a single button at %v, want 150", one[0])
	}
	if len(buttonCentres(th, 0)) != 0 {
		t.Error("no buttons, yet centres")
	}
}

func TestSliderValue(t *testing.T) {
	th := NewTheme(2, 300)
	lo, hi := sliderTrack(th)
	if lo <= th.padX+th.sliderGlyph || hi >= float64(th.Width)-th.padX {
		t.Errorf("track %v..%v runs into the symbol or the edge", lo, hi)
	}
	for _, tc := range []struct {
		x, want float64
	}{
		{lo, 0},
		{hi, 1},
		{(lo + hi) / 2, 0.5},
		{-100, 0},
		{1e9, 1},
	} {
		if got := sliderValue(th, tc.x); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("sliderValue(%v) = %v, want %v", tc.x, got, tc.want)
		}
	}
	// A menu too narrow for a track sets every slider to 0 rather than
	// dividing by nothing.
	if got := sliderValue(NewTheme(1, 10), 5); got != 0 {
		t.Errorf("sliderValue on a track with no room = %v", got)
	}
}

func TestClamp01(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-1, 0}, {0.25, 0.25}, {7, 1}, {math.NaN(), 0}, {math.Inf(1), 1}, {math.Inf(-1), 0},
	} {
		if got := clamp01(tc.in); got != tc.want {
			t.Errorf("clamp01(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
