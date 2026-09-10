// Package paint draws the handful of shapes these tools need onto an RGBA
// image: filled rectangles, anti-aliased rounded rectangles and circles,
// and alpha compositing of one image over another.
//
// It exists because the alternative is xgbutil's xgraphics, which drags in
// two unmaintained dependencies for scaling and text that nothing here
// wants. Shapes are drawn from a signed distance field rather than by
// scan-converting outlines: one short function covers every rounded shape
// on screen, the edges are anti-aliased for free, and there is no polygon
// rasteriser to get subtly wrong.
package paint

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Fill paints a solid rectangle, clipped to the image.
func Fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(img, r.Intersect(img.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

// Clear resets every pixel to fully transparent. On an ARGB window that is
// what makes the parts of the surface outside the dock invisible rather
// than black.
func Clear(img *image.RGBA) {
	for i := range img.Pix {
		img.Pix[i] = 0
	}
}

// RoundRect composites an anti-aliased rounded rectangle onto img.
//
// The radius is clamped to half the shorter side, so passing a very large
// radius yields a capsule (or a circle, for a square) rather than a
// rendering artefact -- which is exactly how the dock draws its indicator
// dots.
//
// Only the four corners are evaluated through the distance field. An
// earlier version ran it over every pixel, which is the obvious way to
// write this and cost 5.4 ms for the dock's panel -- most of a frame,
// every frame, spent computing that the middle of a rectangle is inside
// the rectangle. The straight edges and the interior are filled with a
// flat blend instead, and only the corner squares, a few thousand pixels
// in total, need the arithmetic.
func RoundRect(img *image.RGBA, r image.Rectangle, radius float64, c color.RGBA) {
	roundRect(img, r, radius, c, false)
}

// RoundRectOnClear is RoundRect for a destination whose pixels under the
// shape are known to be fully transparent, which is the case for the
// dock's panel and the popup's plate: both are the first thing drawn after
// Clear.
//
// Compositing a colour over a transparent pixel yields that colour
// premultiplied by its own alpha and nothing else, so the read, the
// multiply-add per channel and the write-back of a blend are arithmetic
// whose answer is known before the loop starts. Writing the answer through
// instead is a fill, and the plate is a hundred and thirty thousand pixels
// of it on every frame -- 0.6 ms of an 8 ms budget spent blending a colour
// onto nothing.
func RoundRectOnClear(img *image.RGBA, r image.Rectangle, radius float64, c color.RGBA) {
	roundRect(img, r, radius, c, true)
}

func roundRect(img *image.RGBA, r image.Rectangle, radius float64, c color.RGBA, onClear bool) {
	if r.Empty() || c.A == 0 {
		return
	}
	fill := fillBlend
	if onClear {
		// draw.Src through a premultiplied colour: see RoundRectOnClear.
		fill = func(dst *image.RGBA, b image.Rectangle, col color.RGBA) {
			Fill(dst, b, premultiply(col, 1))
		}
	}
	w, h := float64(r.Dx()), float64(r.Dy())
	radius = math.Min(radius, math.Min(w, h)/2)
	if radius < 0 {
		radius = 0
	}
	// The corner squares must not reach past the middle of the shape, or
	// the two edge bands overlap along one row and the colour is
	// composited there twice -- a seam across the middle of every
	// indicator dot, which is where a fully rounded shape puts it. Rounding
	// the corner size down cannot lose an anti-aliased pixel: the column it
	// gives back to the flat fill has its centre at or beyond the point
	// where the arc meets the straight edge, so it was fully covered
	// anyway.
	ri := int(math.Ceil(radius))
	if half := min(r.Dx(), r.Dy()) / 2; ri > half {
		ri = half
	}

	// The interior and the two straight edge bands: fully covered, so they
	// need a blend but no geometry.
	fill(img, image.Rect(r.Min.X, r.Min.Y+ri, r.Max.X, r.Max.Y-ri), c)
	fill(img, image.Rect(r.Min.X+ri, r.Min.Y, r.Max.X-ri, r.Min.Y+ri), c)
	fill(img, image.Rect(r.Min.X+ri, r.Max.Y-ri, r.Max.X-ri, r.Max.Y), c)

	if ri == 0 {
		return
	}

	halfW, halfH := w/2, h/2
	cx, cy := float64(r.Min.X)+halfW, float64(r.Min.Y)+halfH

	// One pixel of bleed outward so the anti-aliased edge is not clipped.
	corners := [4]image.Rectangle{
		image.Rect(r.Min.X-1, r.Min.Y-1, r.Min.X+ri, r.Min.Y+ri),
		image.Rect(r.Max.X-ri, r.Min.Y-1, r.Max.X+1, r.Min.Y+ri),
		image.Rect(r.Min.X-1, r.Max.Y-ri, r.Min.X+ri, r.Max.Y+1),
		image.Rect(r.Max.X-ri, r.Max.Y-ri, r.Max.X+1, r.Max.Y+1),
	}
	for _, corner := range corners {
		b := corner.Intersect(img.Bounds())
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				// Pixel centre, not corner: sampling the corner shifts every
				// shape half a pixel up and left.
				d := sdRoundBox(float64(x)+0.5-cx, float64(y)+0.5-cy, halfW, halfH, radius)
				// The distance field is in pixels, so coverage is the
				// fraction of the pixel inside the shape, approximated
				// linearly across the one-pixel band around the boundary.
				cov := 0.5 - d
				if cov <= 0 {
					continue
				}
				if cov > 1 {
					cov = 1
				}
				if onClear {
					set(img, x, y, premultiply(c, cov))
					continue
				}
				blend(img, x, y, c, cov)
			}
		}
	}
}

// FillBlend composites a translucent colour over a rectangle.
//
// This is what a translucent overlay needs, and Fill is not: Fill writes
// the colour through with draw.Src, and an RGBA image is premultiplied, so
// a hairline of white at 11% alpha stored as {255,255,255,28} is not a
// faint line -- it is an invalid pixel, which a compositor renders far
// brighter than asked for.
func FillBlend(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	fillBlend(img, r, c)
}

// fillBlend composites a fully opaque-coverage colour over a rectangle.
//
// This is the flat case of blend, hoisted out of the per-pixel path: the
// source contribution is constant, so it is premultiplied once and the
// loop is a multiply-add per channel.
func fillBlend(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	b := r.Intersect(img.Bounds())
	if b.Empty() {
		return
	}
	// Fully opaque is the common case for the popup and tooltip bodies and
	// skips the read-modify-write entirely.
	if c.A == 0xff {
		draw.Draw(img, b, image.NewUniform(c), image.Point{}, draw.Src)
		return
	}

	// Integer arithmetic, not float. This loop runs over every pixel of
	// the dock's panel on every frame, and in float64 it cost 0.9 ms of an
	// 8 ms budget; the rounding is identical to within one least
	// significant bit, which no eye has ever resolved.
	a := uint32(c.A)
	inv := 255 - a
	sr := uint32(c.R)*a + 127
	sg := uint32(c.G)*a + 127
	sb := uint32(c.B)*a + 127
	sa := 255*a + 127

	// The result provably fits in a byte, so the conversions below need no
	// clamp. The two weights sum to 255, so for any channel
	//   src*a + dst*(255-a) <= 255*max(src,dst) <= 65025,
	// and with the +127 rounding term 65152/255 = 255. gosec cannot see
	// that the weights are complementary and flags each conversion.
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		for o := 0; o+3 < len(row); o += 4 {
			//nolint:gosec // bounded by 255: see the proof above
			row[o+0] = uint8((sr + uint32(row[o+0])*inv) / 255)
			//nolint:gosec // bounded by 255: see the proof above
			row[o+1] = uint8((sg + uint32(row[o+1])*inv) / 255)
			//nolint:gosec // bounded by 255: see the proof above
			row[o+2] = uint8((sb + uint32(row[o+2])*inv) / 255)
			//nolint:gosec // bounded by 255: see the proof above
			row[o+3] = uint8((sa + uint32(row[o+3])*inv) / 255)
		}
	}
}

// sdRoundBox is the signed distance from a point to a rounded box centred
// at the origin: negative inside, positive outside.
func sdRoundBox(px, py, halfW, halfH, radius float64) float64 {
	qx := math.Abs(px) - halfW + radius
	qy := math.Abs(py) - halfH + radius
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	inside := math.Min(math.Max(qx, qy), 0)
	return outside + inside - radius
}

// Circle composites an anti-aliased filled circle.
func Circle(img *image.RGBA, cx, cy int, radius float64, c color.RGBA) {
	r := image.Rect(cx-int(radius)-1, cy-int(radius)-1, cx+int(radius)+1, cy+int(radius)+1)
	RoundRect(img, r, radius, c)
}

// premultiply returns c with every channel scaled by its alpha and by an
// extra coverage, which is the pixel a blend onto full transparency would
// have produced.
func premultiply(c color.RGBA, cov float64) color.RGBA {
	a := float64(c.A) / 255 * cov
	return color.RGBA{
		R: uint8(float64(c.R)*a + 0.5),
		G: uint8(float64(c.G)*a + 0.5),
		B: uint8(float64(c.B)*a + 0.5),
		A: uint8(255*a + 0.5),
	}
}

// set writes one already-premultiplied pixel, ignoring what was there.
func set(img *image.RGBA, x, y int, c color.RGBA) {
	if !(image.Point{X: x, Y: y}).In(img.Bounds()) {
		return
	}
	o := img.PixOffset(x, y)
	img.Pix[o+0], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c.R, c.G, c.B, c.A
}

// blend composites a colour onto one pixel with the given coverage,
// treating the source colour as non-premultiplied and the destination as
// premultiplied, which is what image/draw and the X server both expect.
func blend(img *image.RGBA, x, y int, c color.RGBA, cov float64) {
	if !(image.Point{X: x, Y: y}).In(img.Bounds()) {
		return
	}
	a := float64(c.A) / 255 * cov
	if a <= 0 {
		return
	}
	o := img.PixOffset(x, y)
	inv := 1 - a
	img.Pix[o+0] = uint8(float64(c.R)*a + float64(img.Pix[o+0])*inv)
	img.Pix[o+1] = uint8(float64(c.G)*a + float64(img.Pix[o+1])*inv)
	img.Pix[o+2] = uint8(float64(c.B)*a + float64(img.Pix[o+2])*inv)
	img.Pix[o+3] = uint8(255*a + float64(img.Pix[o+3])*inv)
}

// Over composites src onto dst with its top-left corner at (x, y),
// respecting src's alpha.
//
// The whole of src is anchored at (x, y) and whatever falls outside dst is
// dropped, which is not what handing a clipped rectangle to image/draw
// does: there the source point is aligned with the clipped corner, so an
// image hanging off the top or left edge is drawn shifted rather than
// cropped.
func Over(dst *image.RGBA, src image.Image, x, y int) {
	b := src.Bounds()
	r := image.Rect(x, y, x+b.Dx(), y+b.Dy()).Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	if rgba, ok := src.(*image.RGBA); ok {
		overRGBA(dst, rgba, r, x, y)
		return
	}
	draw.Draw(dst, r, src, b.Min.Add(r.Min.Sub(image.Pt(x, y))), draw.Over)
}

// overRGBA is Over for the case every icon takes: one premultiplied RGBA
// image onto another.
//
// image/draw handles this in 16-bit arithmetic, which is the right choice
// for a general library and costs the dock five times what it needs to:
// widening every channel, dividing by 0xffff and narrowing again, for
// seven icons on every frame. Byte arithmetic is a least significant bit
// away and skips the two cases that make up most of an icon's pixels --
// the opaque middle, which is a copy, and the transparent surround, which
// is nothing at all.
func overRGBA(dst, src *image.RGBA, r image.Rectangle, x, y int) {
	width := r.Dx() * 4
	for dy := r.Min.Y; dy < r.Max.Y; dy++ {
		so := src.PixOffset(r.Min.X-x+src.Bounds().Min.X, dy-y+src.Bounds().Min.Y)
		do := dst.PixOffset(r.Min.X, dy)
		srow := src.Pix[so : so+width : so+width]
		drow := dst.Pix[do : do+width : do+width]
		for o := 0; o+3 < len(srow); o += 4 {
			a := uint32(srow[o+3])
			switch a {
			case 0:
				continue
			case 0xff:
				drow[o+0], drow[o+1], drow[o+2], drow[o+3] = srow[o+0], srow[o+1], srow[o+2], 0xff
				continue
			}
			// Premultiplied on both sides, so the source contribution is
			// already scaled: dst = src + dst*(1-srcAlpha). The sum cannot
			// exceed 255 for premultiplied inputs, which is what makes the
			// narrowing conversions below safe.
			inv := 0xff - a
			//nolint:gosec // premultiplied: src + dst*(255-a)/255 <= 255
			drow[o+0] = uint8(uint32(srow[o+0]) + (uint32(drow[o+0])*inv+127)/255)
			//nolint:gosec // as above
			drow[o+1] = uint8(uint32(srow[o+1]) + (uint32(drow[o+1])*inv+127)/255)
			//nolint:gosec // as above
			drow[o+2] = uint8(uint32(srow[o+2]) + (uint32(drow[o+2])*inv+127)/255)
			//nolint:gosec // as above
			drow[o+3] = uint8(a + (uint32(drow[o+3])*inv+127)/255)
		}
	}
}

// OverAlpha is Over with a uniform extra opacity applied to src, used to
// fade things in and out.
func OverAlpha(dst *image.RGBA, src image.Image, x, y int, alpha float64) {
	switch {
	case alpha <= 0:
		return
	case alpha >= 1:
		Over(dst, src, x, y)
		return
	}
	b := src.Bounds()
	mask := image.NewUniform(color.Alpha{A: uint8(alpha*255 + 0.5)})
	draw.DrawMask(dst, image.Rect(x, y, x+b.Dx(), y+b.Dy()).Intersect(dst.Bounds()),
		src, b.Min, mask, image.Point{}, draw.Over)
}
