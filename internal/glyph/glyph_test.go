package glyph

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCircleBoxSegment(t *testing.T) {
	c := Circle(0, 0, 2)
	if d := c(3, 4); !near(d, 3) {
		t.Errorf("circle distance = %v, want 3", d)
	}
	b := Box(10, 10, 5, 3, 1)
	if d := b(10, 10); d >= 0 {
		t.Errorf("box centre distance %v, want negative", d)
	}
	if d := b(20, 10); !near(d, 5) {
		t.Errorf("box distance 5 right of its edge = %v", d)
	}
	s := Segment(0, 0, 10, 0, 1)
	for _, tc := range []struct{ x, y, want float64 }{
		{5, 0, -1},  // on the line
		{5, 3, 2},   // beside it
		{13, 4, 4},  // past the end: round cap
		{-3, -4, 4}, // before the start
	} {
		if d := s(tc.x, tc.y); !near(d, tc.want) {
			t.Errorf("segment distance at %v,%v = %v, want %v", tc.x, tc.y, d, tc.want)
		}
	}
	// A zero-length segment is a dot, not a division by zero.
	if d := Segment(1, 1, 1, 1, 1)(1, 4); !near(d, 2) {
		t.Errorf("zero-length segment distance = %v, want 2", d)
	}
}

func TestPolygon(t *testing.T) {
	square := [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	reversed := [][2]float64{{0, 10}, {10, 10}, {10, 0}, {0, 0}}
	for name, pts := range map[string][][2]float64{"one winding": square, "the other": reversed} {
		p := Polygon(pts...)
		if d := p(5, 5); !near(d, -5) {
			t.Errorf("%s: centre distance = %v, want -5", name, d)
		}
		if d := p(15, 5); !near(d, 5) {
			t.Errorf("%s: distance right of the square = %v, want 5", name, d)
		}
		if d := p(5, -2); !near(d, 2) {
			t.Errorf("%s: distance above the square = %v, want 2", name, d)
		}
	}
	for name, pts := range map[string][][2]float64{
		"two points": {{0, 0}, {1, 1}},
		"collinear":  {{0, 0}, {1, 1}, {2, 2}},
		"repeated":   {{3, 3}, {3, 3}, {3, 3}},
	} {
		if d := Polygon(pts...)(0, 0); !math.IsInf(d, 1) {
			t.Errorf("%s: distance = %v, want +Inf (empty)", name, d)
		}
	}
}

func TestCombinators(t *testing.T) {
	a := Circle(0, 0, 5)
	b := Circle(6, 0, 5)
	if d := Union(a, b)(9, 0); d >= 0 {
		t.Errorf("union: point in b only is outside (%v)", d)
	}
	if d := Intersect(a, b)(-3, 0); d <= 0 {
		t.Errorf("intersect: point in a only is inside (%v)", d)
	}
	if d := Intersect(a, b)(3, 0); d >= 0 {
		t.Errorf("intersect: point in both is outside (%v)", d)
	}
	if d := Subtract(a, b)(3, 0); d <= 0 {
		t.Errorf("subtract: point in b is still inside (%v)", d)
	}
	if d := Subtract(a, b)(-3, 0); d >= 0 {
		t.Errorf("subtract: point in a only is outside (%v)", d)
	}
	o := Outline(Circle(0, 0, 5), 2)
	if d := o(0, 0); d <= 0 {
		t.Errorf("outline: the centre is inside the stroke (%v)", d)
	}
	if d := o(5, 0); d >= 0 {
		t.Errorf("outline: the edge is outside the stroke (%v)", d)
	}
}

func TestCoverage(t *testing.T) {
	for _, tc := range []struct{ d, want float64 }{{-5, 1}, {0, 0.5}, {5, 0}, {0.25, 0.25}} {
		if got := Coverage(tc.d); got != tc.want {
			t.Errorf("Coverage(%v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}

func TestFillEraseBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	disc := Circle(10, 10, 6)
	// Bounds covering only the left half: the right half stays untouched.
	Fill(img, image.Rect(0, 0, 10, 20), white, disc)
	if a := img.RGBAAt(8, 10).A; a != 255 {
		t.Errorf("inside the bounds and the disc: alpha %d", a)
	}
	if a := img.RGBAAt(12, 10).A; a != 0 {
		t.Errorf("outside the bounds: alpha %d", a)
	}
	// Bounds larger than the image are clipped, not a panic.
	Fill(img, image.Rect(-50, -50, 50, 50), white, disc)
	if a := img.RGBAAt(12, 10).A; a != 255 {
		t.Errorf("after filling the whole disc: alpha %d", a)
	}
	Erase(img, img.Bounds(), 1, Circle(10, 10, 2))
	if px := img.RGBAAt(10, 10); px != (color.RGBA{}) {
		t.Errorf("erased centre = %v, want transparent", px)
	}
	if a := img.RGBAAt(10, 5).A; a != 255 {
		t.Errorf("outside the erased margin: alpha %d", a)
	}
}

// Straight alpha in, premultiplied out: half-transparent white over nothing
// is premultiplied grey at half alpha.
func TestFillPremultiplies(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	Fill(img, img.Bounds(), color.RGBA{R: 255, G: 255, B: 255, A: 128}, Box(2, 2, 10, 10, 0))
	if px := img.RGBAAt(1, 1); px.A != 128 || px.R != 128 {
		t.Errorf("pixel = %v, want R 128 A 128", px)
	}
}

func TestDim(t *testing.T) {
	if got := Dim(color.RGBA{R: 1, G: 2, B: 3, A: 200}, 0.5); got != (color.RGBA{R: 1, G: 2, B: 3, A: 100}) {
		t.Errorf("Dim = %v", got)
	}
}

// opaque counts pixels at least nearly opaque.
func opaque(img *image.RGBA) int {
	n := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0xe0 {
			n++
		}
	}
	return n
}

// Every symbol draws something, and nothing outside its square.
func TestSymbolsStayInTheirSquare(t *testing.T) {
	const pad, side = 8, 32
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	grey := Dim(white, 0.4)
	for name, draw := range map[string]func(dst *image.RGBA, x0, y0, s float64){
		"lock":          sdfDraw(Lock),
		"wired":         sdfDraw(Wired),
		"headphones":    sdfDraw(Headphones),
		"mic":           sdfDraw(Mic),
		"bolt":          sdfDraw(Bolt),
		"sun":           sdfDraw(Sun),
		"keyboardLight": sdfDraw(KeyboardLight),
		"play":          sdfDraw(Play),
		"pause":         sdfDraw(Pause),
		"next":          sdfDraw(Next),
		"previous":      sdfDraw(Previous),
		"presentation":  sdfDraw(Presentation),
		"wifi": func(dst *image.RGBA, x0, y0, s float64) {
			Wifi(dst, x0, y0, s, WifiBands-1, white, grey)
		},
		"speaker": func(dst *image.RGBA, x0, y0, s float64) {
			Speaker(dst, x0, y0, s, SpeakerWaves, false, white, grey)
		},
		"speaker muted": func(dst *image.RGBA, x0, y0, s float64) {
			Speaker(dst, x0, y0, s, 0, true, white, grey)
		},
		"mic muted": func(dst *image.RGBA, x0, y0, s float64) {
			Slashed(dst, x0, y0, s, white, Mic(x0, y0, s))
		},
		"battery charging": func(dst *image.RGBA, x0, y0, s float64) {
			Battery(dst, x0, y0, s, 0.5, true, white, white)
		},
	} {
		img := image.NewRGBA(image.Rect(0, 0, side+2*pad, side+2*pad))
		draw(img, pad, pad, side)
		if opaque(img) == 0 {
			t.Errorf("%s draws nothing", name)
		}
		inside := image.Rect(pad-1, pad-1, pad+side+1, pad+side+1)
	scan:
		for y := range img.Bounds().Dy() {
			for x := range img.Bounds().Dx() {
				if img.RGBAAt(x, y).A != 0 && !image.Pt(x, y).In(inside) {
					t.Errorf("%s paints %d,%d, outside its square", name, x, y)
					break scan
				}
			}
		}
	}
}

func sdfDraw(shape func(x0, y0, s float64) SDF) func(dst *image.RGBA, x0, y0, s float64) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	return func(dst *image.RGBA, x0, y0, s float64) {
		Fill(dst, Area(x0, y0, s, s), white, shape(x0, y0, s))
	}
}

// More arcs lit means more lit pixels; the same for sound waves.
func TestWifiAndSpeakerLevels(t *testing.T) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	grey := Dim(white, 0.3)
	prev := -1
	for lit := -1; lit < WifiBands; lit++ {
		img := image.NewRGBA(image.Rect(0, 0, 44, 44))
		Wifi(img, 0, 0, 44, lit, white, grey)
		if n := opaque(img); n <= prev {
			t.Errorf("Wi-Fi with %d lit: %d opaque pixels, no more than with one fewer (%d)", lit, n, prev)
		} else {
			prev = n
		}
	}
	prev = -1
	for waves := 0; waves <= SpeakerWaves; waves++ {
		img := image.NewRGBA(image.Rect(0, 0, 44, 44))
		Speaker(img, 0, 0, 44, waves, false, white, grey)
		if n := opaque(img); n <= prev {
			t.Errorf("speaker with %d waves: %d opaque pixels, no more than with one fewer (%d)", waves, n, prev)
		} else {
			prev = n
		}
	}
}

// The charge grows left to right with the fraction, and stays inside the
// outline at both ends of its range.
func TestBatteryFill(t *testing.T) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	greens := func(fraction float64) int {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		Battery(img, 0, 0, 64, fraction, false, white, green)
		n := 0
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i+1] > 0xe0 && img.Pix[i] < 0x20 {
				n++
			}
		}
		return n
	}
	empty, half, full, over := greens(0), greens(0.5), greens(1), greens(7)
	if empty != 0 {
		t.Errorf("empty battery has %d charge pixels", empty)
	}
	if half <= 0 || half >= full {
		t.Errorf("charge pixels: half %d, full %d; want 0 < half < full", half, full)
	}
	if over != full {
		t.Errorf("fraction 7 draws %d charge pixels, want the same as full (%d)", over, full)
	}
}
