package glyph

// The symbols themselves. Each fills the square at x0, y0 with side s, and
// is laid out in fractions of that square, so one definition serves a
// 16-pixel menu badge and a 64-pixel tray icon alike.
//
// Single-colour symbols are SDFs, for the caller to Fill, Erase around or
// combine. Symbols with parts in two colours -- the lit and unlit arcs of
// Wi-Fi, a battery's charge -- draw themselves.

import (
	"image"
	"image/color"
	"math"
)

// unit maps a unit square onto the square a symbol fills.
type unit struct{ x0, y0, s float64 }

func (u unit) x(v float64) float64 { return u.x0 + v*u.s }
func (u unit) y(v float64) float64 { return u.y0 + v*u.s }
func (u unit) d(v float64) float64 { return v * u.s }

func (u unit) bounds() image.Rectangle { return Area(u.x0, u.y0, u.s, u.s) }

func (u unit) box(cx, cy, hw, hh, r float64) SDF {
	return Box(u.x(cx), u.y(cy), u.d(hw), u.d(hh), u.d(r))
}

// stroke is the half-width of every line in a symbol, so that strokes read
// as one weight throughout a menu.
const stroke = 0.045

func (u unit) seg(ax, ay, bx, by float64) SDF {
	return Segment(u.x(ax), u.y(ay), u.x(bx), u.y(by), u.d(stroke))
}

func (u unit) poly(points ...[2]float64) SDF {
	mapped := make([][2]float64, len(points))
	for i, p := range points {
		mapped[i] = [2]float64{u.x(p[0]), u.y(p[1])}
	}
	return Polygon(mapped...)
}

// WifiBands is how many parts Wifi draws: the dot and three arcs.
const WifiBands = 4

var wifiBands = [WifiBands]struct{ inner, outer float64 }{
	// The outermost arc at 45 degrees reaches 0.70 * sin 45 = 0.495 either
	// side of centre, just inside the square.
	{0, 0.13},
	{0.22, 0.33},
	{0.40, 0.51},
	{0.59, 0.70},
}

// WifiBand is part i of the Wi-Fi symbol, 0 (the dot) to WifiBands-1.
func WifiBand(x0, y0, s float64, i int) SDF {
	u := unit{x0, y0, s}
	b := wifiBands[i]
	return arcBand(u.x(0.5), u.y(0.86), u.d(b.inner), u.d(b.outer), 0, -1)
}

// Wifi draws the Wi-Fi symbol: parts up to and including lit in on, the
// rest in off. lit is -1 for none, 0 for the dot alone, 3 for all.
func Wifi(dst *image.RGBA, x0, y0, s float64, lit int, on, off color.RGBA) {
	bounds := Area(x0, y0, s, s)
	for i := range WifiBands {
		col := off
		if i <= lit {
			col = on
		}
		Fill(dst, bounds, col, WifiBand(x0, y0, s, i))
	}
}

// Lock is a padlock.
func Lock(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	body := u.box(0.5, 0.72, 0.34, 0.25, 0.08)
	// Only the part of the shackle above the body: the rest would be hidden
	// in it anyway, and cutting it keeps the legs from poking out below.
	shackle := Intersect(ring(u.x(0.5), u.y(0.44), u.d(0.14), u.d(0.25)), above(u.y(0.5)))
	return Union(body, shackle)
}

// Wired is three connected boxes, the usual symbol for a wired network.
func Wired(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.box(0.5, 0.22, 0.15, 0.12, 0.04),
		u.box(0.2, 0.78, 0.15, 0.12, 0.04),
		u.box(0.8, 0.78, 0.15, 0.12, 0.04),
		u.box(0.5, 0.42, 0.04, 0.10, 0),
		u.box(0.5, 0.53, 0.34, 0.04, 0),
		u.box(0.2, 0.60, 0.04, 0.08, 0),
		u.box(0.8, 0.60, 0.04, 0.08, 0),
	)
}

// SpeakerWaves is how many sound waves Speaker can light.
const SpeakerWaves = 3

var speakerBands = [SpeakerWaves]struct{ inner, outer float64 }{
	{0.14, 0.21},
	{0.28, 0.35},
	{0.42, 0.49},
}

// Speaker draws a loudspeaker with waves sound waves in on and the rest in
// off, or with a cross instead of waves when muted.
func Speaker(dst *image.RGBA, x0, y0, s float64, waves int, muted bool, on, off color.RGBA) {
	u := unit{x0, y0, s}
	bounds := u.bounds()
	Fill(dst, bounds, on, speakerBody(u))
	if muted {
		Fill(dst, bounds, on, Union(
			u.seg(0.62, 0.38, 0.86, 0.62),
			u.seg(0.62, 0.62, 0.86, 0.38)))
		return
	}
	for i, b := range speakerBands {
		col := off
		if i < waves {
			col = on
		}
		Fill(dst, bounds, col, arcBand(u.x(0.40), u.y(0.5), u.d(b.inner), u.d(b.outer), 1, 0))
	}
}

// speakerBody is the box and the cone. They are two shapes because
// together they are not convex: the cone flares out from the box.
func speakerBody(u unit) SDF {
	return Union(
		u.box(0.17, 0.5, 0.09, 0.12, 0.02),
		u.poly([2]float64{0.26, 0.38}, [2]float64{0.46, 0.2}, [2]float64{0.46, 0.8}, [2]float64{0.26, 0.62}),
	)
}

// Bluetooth is the Bluetooth rune: a vertical stroke with the two arrow
// heads crossing it, drawn as strokes so it matches the other symbols'
// weight.
func Bluetooth(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.seg(0.5, 0.1, 0.5, 0.9),
		u.seg(0.5, 0.1, 0.76, 0.32),
		u.seg(0.76, 0.32, 0.26, 0.7),
		u.seg(0.5, 0.9, 0.76, 0.68),
		u.seg(0.76, 0.68, 0.26, 0.3),
	)
}

// Headphones is a pair of headphones, for an output that is one.
func Headphones(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	cy := u.y(0.56)
	band := Intersect(ring(u.x(0.5), cy, u.d(0.30), u.d(0.37)), above(cy))
	return Union(band, u.box(0.2, 0.7, 0.09, 0.16, 0.05), u.box(0.8, 0.7, 0.09, 0.16, 0.05))
}

// Mic is a microphone on a stand.
func Mic(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	cy := u.y(0.44)
	holder := Intersect(ring(u.x(0.5), cy, u.d(0.22), u.d(0.28)), below(cy))
	return Union(
		u.box(0.5, 0.34, 0.12, 0.22, 0.12),
		holder,
		u.box(0.5, 0.8, 0.035, 0.08, 0),
		u.box(0.5, 0.88, 0.16, 0.035, 0.02),
	)
}

// Slash is the diagonal stroke across a symbol that is turned off.
func Slash(x0, y0, s float64) SDF {
	return unit{x0, y0, s}.seg(0.16, 0.12, 0.84, 0.88)
}

// Slashed fills shape with a Slash across it, cut clear of the shape so
// the two do not blur into one.
func Slashed(dst *image.RGBA, x0, y0, s float64, col color.RGBA, shape SDF) {
	bounds := Area(x0, y0, s, s)
	slash := Slash(x0, y0, s)
	Fill(dst, bounds, col, shape)
	Erase(dst, bounds, s*0.06, slash)
	Fill(dst, bounds, col, slash)
}

// Battery draws a horizontal battery outlined in col, filled to fraction
// (0 to 1) in fillCol, with a bolt across it while charging.
func Battery(dst *image.RGBA, x0, y0, s, fraction float64, charging bool, col, fillCol color.RGBA) {
	u := unit{x0, y0, s}
	bounds := u.bounds()
	// Heavier than a line stroke: the battery is the whole symbol, and a
	// thin body reads as empty at tray size.
	const body = 0.065
	Fill(dst, bounds, col, Union(
		Outline(u.box(0.45, 0.5, 0.41, 0.21, 0.07), u.d(body)),
		u.box(0.925, 0.5, 0.025, 0.08, 0.02),
	))

	// The charge sits inside the stroke with a gap all round: the body's
	// inner edge is at 0.0725 and 0.8275 across, and the gap is 0.035.
	const left, right, half = 0.1075, 0.7925, 0.1425
	fraction = math.Max(0, math.Min(1, fraction))
	if w := (right - left) * fraction; w > 0 {
		Fill(dst, bounds, fillCol, u.box(left+w/2, 0.5, w/2, half, math.Min(0.03, w/2)))
	}
	if charging {
		bolt := Bolt(u.x(0.2), u.y(0.25), u.d(0.5))
		Erase(dst, bounds, u.d(0.05), bolt)
		Fill(dst, bounds, col, bolt)
	}
}

// Bolt is a lightning bolt: two triangles, since a bolt is not convex.
func Bolt(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.poly([2]float64{0.58, 0.04}, [2]float64{0.2, 0.56}, [2]float64{0.52, 0.56}),
		u.poly([2]float64{0.48, 0.44}, [2]float64{0.8, 0.44}, [2]float64{0.42, 0.96}),
	)
}

// Sun is a sun with eight rays, the symbol for screen brightness.
func Sun(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	shapes := make([]SDF, 0, 9)
	shapes = append(shapes, Circle(u.x(0.5), u.y(0.5), u.d(0.17)))
	for k := range 8 {
		a := float64(k) * math.Pi / 4
		cos, sin := math.Cos(a), math.Sin(a)
		shapes = append(shapes, u.seg(0.5+cos*0.29, 0.5+sin*0.29, 0.5+cos*0.43, 0.5+sin*0.43))
	}
	return Union(shapes...)
}

// KeyboardLight is a key with light rays above it, the symbol for keyboard
// brightness.
func KeyboardLight(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.box(0.5, 0.72, 0.32, 0.16, 0.05),
		u.seg(0.5, 0.44, 0.5, 0.2),
		u.seg(0.28, 0.48, 0.14, 0.32),
		u.seg(0.72, 0.48, 0.86, 0.32),
	)
}

// Play is a play triangle.
func Play(x0, y0, s float64) SDF {
	return unit{x0, y0, s}.poly([2]float64{0.3, 0.18}, [2]float64{0.84, 0.5}, [2]float64{0.3, 0.82})
}

// Pause is two bars.
func Pause(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(u.box(0.34, 0.5, 0.09, 0.3, 0.03), u.box(0.66, 0.5, 0.09, 0.3, 0.03))
}

// Next is two triangles and a bar, pointing right.
func Next(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.poly([2]float64{0.1, 0.22}, [2]float64{0.45, 0.5}, [2]float64{0.1, 0.78}),
		u.poly([2]float64{0.45, 0.22}, [2]float64{0.8, 0.5}, [2]float64{0.45, 0.78}),
		u.box(0.86, 0.5, 0.04, 0.28, 0.01),
	)
}

// Previous is Next pointing left.
func Previous(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		u.poly([2]float64{0.9, 0.22}, [2]float64{0.55, 0.5}, [2]float64{0.9, 0.78}),
		u.poly([2]float64{0.55, 0.22}, [2]float64{0.2, 0.5}, [2]float64{0.55, 0.78}),
		u.box(0.14, 0.5, 0.04, 0.28, 0.01),
	)
}

// Presentation is a screen on a stand, for presentation mode.
func Presentation(x0, y0, s float64) SDF {
	u := unit{x0, y0, s}
	return Union(
		Outline(u.box(0.5, 0.4, 0.4, 0.26, 0.05), u.d(0.08)),
		u.box(0.5, 0.77, 0.035, 0.1, 0),
		u.box(0.5, 0.87, 0.18, 0.035, 0.02),
	)
}
