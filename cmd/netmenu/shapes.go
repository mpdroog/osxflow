package main

// Shapes drawn from signed distance functions: the Wi-Fi arcs, the lock,
// the switches, the badges.
//
// A shape is a function giving any point's distance to its edge, in
// pixels, negative inside. One routine turns that into anti-aliased
// coverage, so every shape gets the same clean edge for a line or two of
// maths, at any scale -- the approach internal/paint takes for rounded
// rectangles, carried over to shapes it does not have.

import (
	"image"
	"image/color"
	"math"
)

// sdf is a signed distance function over absolute pixel coordinates.
type sdf func(x, y float64) float64

func circle(cx, cy, r float64) sdf {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - r }
}

// box is a rectangle centred on cx, cy with half-extents hw, hh and
// corners rounded to radius.
func box(cx, cy, hw, hh, radius float64) sdf {
	return func(x, y float64) float64 {
		qx := math.Abs(x-cx) - hw + radius
		qy := math.Abs(y-cy) - hh + radius
		outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
		inside := math.Min(math.Max(qx, qy), 0)
		return outside + inside - radius
	}
}

func union(shapes ...sdf) sdf {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for _, s := range shapes {
			d = math.Min(d, s(x, y))
		}
		return d
	}
}

// wifiBands are the Wi-Fi symbol's dot and three arcs, as inner and outer
// radii in fractions of the glyph's side. The outermost arc at 45 degrees
// reaches 0.70 * sin 45 = 0.495 either side of centre, just inside the
// square.
var wifiBands = [...]struct{ inner, outer float64 }{
	{0, 0.13},
	{0.22, 0.33},
	{0.40, 0.51},
	{0.59, 0.70},
}

// wifiBand is band i of a Wi-Fi symbol filling the square at x0, y0 with
// side s: a ring cut to the 90-degree wedge opening upward from a point
// near the bottom.
func wifiBand(x0, y0, s float64, i int) sdf {
	cx, cy := x0+s/2, y0+s*0.86
	inner, outer := wifiBands[i].inner*s, wifiBands[i].outer*s
	return func(x, y float64) float64 {
		dx, dy := x-cx, cy-y
		r := math.Hypot(dx, dy)
		radial := math.Max(inner-r, r-outer)
		wedge := (math.Abs(dx) - dy) * math.Sqrt2 / 2
		return math.Max(radial, wedge)
	}
}

// lock is a padlock filling the square at x0, y0 with side s.
func lock(x0, y0, s float64) sdf {
	body := box(x0+s*0.5, y0+s*0.72, s*0.34, s*0.25, s*0.08)
	cx, cy := x0+s*0.5, y0+s*0.44
	top := y0 + s*0.5
	shackle := func(x, y float64) float64 {
		r := math.Hypot(x-cx, y-cy)
		ring := math.Max(s*0.14-r, r-s*0.25)
		// Only the part above the body: the rest would be hidden in it
		// anyway, and cutting it keeps the legs from poking out below.
		return math.Max(ring, y-top)
	}
	return union(body, shackle)
}

// wired is three connected boxes, the usual symbol for a wired network,
// filling the square at x0, y0 with side s.
func wired(x0, y0, s float64) sdf {
	part := func(u, v, hu, hv, r float64) sdf { return box(x0+u*s, y0+v*s, hu*s, hv*s, r*s) }
	return union(
		part(0.5, 0.22, 0.15, 0.12, 0.04),
		part(0.2, 0.78, 0.15, 0.12, 0.04),
		part(0.8, 0.78, 0.15, 0.12, 0.04),
		part(0.5, 0.42, 0.04, 0.10, 0),
		part(0.5, 0.53, 0.34, 0.04, 0),
		part(0.2, 0.60, 0.04, 0.08, 0),
		part(0.8, 0.60, 0.04, 0.08, 0),
	)
}

// coverage turns a distance into how much of a pixel the shape covers,
// with a one-pixel ramp across the edge.
func coverage(d float64) float64 {
	return math.Max(0, math.Min(1, 0.5-d))
}

// fill paints a shape in col over what dst already holds, within bounds.
// Only the pixels in bounds are visited, so give the shape's own box: a
// shape tested against the whole menu costs a hundred thousand calls.
func fill(dst *image.RGBA, bounds image.Rectangle, col color.RGBA, shape sdf) {
	b := bounds.Intersect(dst.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if cov := coverage(shape(float64(x)+0.5, float64(y)+0.5)); cov > 0 {
				over(dst, x, y, col, cov)
			}
		}
	}
}

// erase clears a shape grown by margin, so a badge drawn over a glyph
// reads as separate from it rather than merged into it.
func erase(dst *image.RGBA, bounds image.Rectangle, margin float64, shape sdf) {
	b := bounds.Intersect(dst.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cov := coverage(shape(float64(x)+0.5, float64(y)+0.5) - margin)
			if cov == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			for c := range 4 {
				dst.Pix[i+c] = channel(float64(dst.Pix[i+c]) * (1 - cov))
			}
		}
	}
}

// over composites straight-alpha col at coverage cov onto dst's
// premultiplied pixel.
func over(dst *image.RGBA, x, y int, col color.RGBA, cov float64) {
	i := dst.PixOffset(x, y)
	a := float64(col.A) / 255 * cov
	keep := 1 - a
	px := dst.Pix[i : i+4 : i+4]
	px[0] = channel(float64(col.R)*a + float64(px[0])*keep)
	px[1] = channel(float64(col.G)*a + float64(px[1])*keep)
	px[2] = channel(float64(col.B)*a + float64(px[2])*keep)
	px[3] = channel(255*a + float64(px[3])*keep)
}

func channel(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, v+0.5)))
}

// area is the pixel rectangle covering a float box, with a pixel to spare
// on each side for the anti-aliased edge.
func area(x0, y0, w, h float64) image.Rectangle {
	return image.Rect(int(math.Floor(x0))-1, int(math.Floor(y0))-1,
		int(math.Ceil(x0+w))+1, int(math.Ceil(y0+h))+1)
}

// drawWifi paints a Wi-Fi symbol in the square at x0, y0 with side s: bands
// up to and including lit in on, the rest in off. lit is -1 for none, 0 for
// the dot alone, 3 for all.
func drawWifi(dst *image.RGBA, x0, y0, s float64, lit int, on, off color.RGBA) {
	bounds := area(x0, y0, s, s)
	for i := range wifiBands {
		col := off
		if i <= lit {
			col = on
		}
		fill(dst, bounds, col, wifiBand(x0, y0, s, i))
	}
}
