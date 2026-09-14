// Package glyph draws small anti-aliased symbols -- tray icons and the
// symbols inside menus -- from signed distance functions.
//
// A shape is a function giving any point's distance to its edge, in
// pixels, negative inside. One routine turns that into coverage, so every
// shape gets the same clean edge for a line or two of maths, at any size.
// It is the approach internal/paint takes for rounded rectangles, carried
// over to the shapes it does not have: arcs, triangles, strokes.
package glyph

import (
	"image"
	"image/color"
	"math"
)

// SDF is a signed distance function over absolute pixel coordinates:
// negative inside the shape, positive outside, zero on its edge.
type SDF func(x, y float64) float64

// Circle is a disc centred on cx, cy.
func Circle(cx, cy, r float64) SDF {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - r }
}

// Box is a rectangle centred on cx, cy with half-extents hw, hh and corners
// rounded to radius.
func Box(cx, cy, hw, hh, radius float64) SDF {
	return func(x, y float64) float64 {
		qx := math.Abs(x-cx) - hw + radius
		qy := math.Abs(y-cy) - hh + radius
		outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
		inside := math.Min(math.Max(qx, qy), 0)
		return outside + inside - radius
	}
}

// Segment is a stroke from a to b with round ends, r thick either side of
// the line.
func Segment(ax, ay, bx, by, r float64) SDF {
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy
	return func(x, y float64) float64 {
		px, py := x-ax, y-ay
		h := 0.0
		if lenSq > 0 {
			h = math.Max(0, math.Min(1, (px*dx+py*dy)/lenSq))
		}
		return math.Hypot(px-dx*h, py-dy*h) - r
	}
}

// Polygon is a convex polygon through points, wound either way.
//
// The distance is the furthest of the edges' half-planes, which is exact
// along the edges and a little short beyond a corner. That only shows as a
// corner a fraction of a pixel sharper than true, which at these sizes is
// the right trade for a function this short. A polygon with fewer than
// three distinct corners is empty.
func Polygon(points ...[2]float64) SDF {
	empty := func(float64, float64) float64 { return math.Inf(1) }
	if len(points) < 3 {
		return empty
	}
	var area float64
	for i := range points {
		a, b := points[i], points[(i+1)%len(points)]
		area += a[0]*b[1] - b[0]*a[1]
	}
	if area == 0 {
		return empty
	}
	// The outward normal of an edge is on the side its winding leaves the
	// inside: which side that is depends on the direction of travel.
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	type edge struct{ ax, ay, nx, ny float64 }
	edges := make([]edge, 0, len(points))
	for i := range points {
		a, b := points[i], points[(i+1)%len(points)]
		ex, ey := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(ex, ey)
		if l == 0 {
			continue
		}
		edges = append(edges, edge{ax: a[0], ay: a[1], nx: sign * ey / l, ny: -sign * ex / l})
	}
	return func(x, y float64) float64 {
		d := math.Inf(-1)
		for i := range edges {
			e := &edges[i]
			d = math.Max(d, (x-e.ax)*e.nx+(y-e.ay)*e.ny)
		}
		return d
	}
}

// Union is everything inside any of the shapes.
func Union(shapes ...SDF) SDF {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for _, s := range shapes {
			d = math.Min(d, s(x, y))
		}
		return d
	}
}

// Intersect is what is inside both shapes.
func Intersect(a, b SDF) SDF {
	return func(x, y float64) float64 { return math.Max(a(x, y), b(x, y)) }
}

// Subtract is a with b cut out of it.
func Subtract(a, b SDF) SDF {
	return func(x, y float64) float64 { return math.Max(a(x, y), -b(x, y)) }
}

// Outline is a stroke of the given width centred on a shape's edge.
func Outline(shape SDF, width float64) SDF {
	return func(x, y float64) float64 { return math.Abs(shape(x, y)) - width/2 }
}

// ring is the band between two circles around cx, cy.
func ring(cx, cy, inner, outer float64) SDF {
	return func(x, y float64) float64 {
		r := math.Hypot(x-cx, y-cy)
		return math.Max(inner-r, r-outer)
	}
}

// arcBand is a ring cut to the 90-degree wedge opening from cx, cy in the
// direction ux, uy, which must be a unit vector.
func arcBand(cx, cy, inner, outer, ux, uy float64) SDF {
	band := ring(cx, cy, inner, outer)
	return func(x, y float64) float64 {
		px, py := x-cx, y-cy
		along := px*ux + py*uy
		across := math.Abs(py*ux - px*uy)
		wedge := (across - along) * math.Sqrt2 / 2
		return math.Max(band(x, y), wedge)
	}
}

// above keeps what lies above a horizontal line, and below what lies
// under it.
func above(line float64) SDF { return func(_, y float64) float64 { return y - line } }
func below(line float64) SDF { return func(_, y float64) float64 { return line - y } }

// Coverage turns a distance into how much of a pixel the shape covers,
// with a one-pixel ramp across the edge.
func Coverage(d float64) float64 {
	return math.Max(0, math.Min(1, 0.5-d))
}

// Fill paints a shape in col over what dst already holds, within bounds.
//
// col is straight (not premultiplied) alpha, as every colour in this
// repository is written; dst is premultiplied, as image.RGBA always is.
// Only pixels in bounds are visited, so pass the shape's own box: a shape
// tested against a whole menu costs a hundred thousand calls.
func Fill(dst *image.RGBA, bounds image.Rectangle, col color.RGBA, shape SDF) {
	b := bounds.Intersect(dst.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if cov := Coverage(shape(float64(x)+0.5, float64(y)+0.5)); cov > 0 {
				over(dst, x, y, col, cov)
			}
		}
	}
}

// Erase clears a shape grown by margin, so a symbol drawn over another
// reads as separate from it rather than merged into it.
func Erase(dst *image.RGBA, bounds image.Rectangle, margin float64, shape SDF) {
	b := bounds.Intersect(dst.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cov := Coverage(shape(float64(x)+0.5, float64(y)+0.5) - margin)
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

// Area is the pixel rectangle covering a float box, with a pixel to spare
// on each side for the anti-aliased edge.
func Area(x0, y0, w, h float64) image.Rectangle {
	return image.Rect(int(math.Floor(x0))-1, int(math.Floor(y0))-1,
		int(math.Ceil(x0+w))+1, int(math.Ceil(y0+h))+1)
}

// Dim returns col at a fraction of its alpha: the colour of the unlit part
// of a symbol drawn in col.
func Dim(col color.RGBA, fraction float64) color.RGBA {
	col.A = channel(float64(col.A) * fraction)
	return col
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
