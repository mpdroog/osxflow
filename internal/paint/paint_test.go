package paint

import (
	"image"
	"image/color"
	"image/draw"
	"math/rand"
	"testing"
)

func newCleared(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	Clear(img)
	return img
}

func at(img *image.RGBA, x, y int) color.RGBA {
	o := img.PixOffset(x, y)
	return color.RGBA{R: img.Pix[o], G: img.Pix[o+1], B: img.Pix[o+2], A: img.Pix[o+3]}
}

func TestClear(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	Fill(img, img.Bounds(), color.RGBA{R: 1, G: 2, B: 3, A: 4})
	Clear(img)
	for i, v := range img.Pix {
		if v != 0 {
			t.Fatalf("byte %d is %d after Clear", i, v)
		}
	}
}

func TestRoundRectFillsTheMiddle(t *testing.T) {
	img := newCleared(60, 40)
	c := color.RGBA{R: 0x20, G: 0x30, B: 0x40, A: 0xff}
	RoundRect(img, image.Rect(5, 5, 55, 35), 10, c)

	if got := at(img, 30, 20); got != c {
		t.Errorf("centre = %+v, want %+v", got, c)
	}
	if got := at(img, 1, 1); got.A != 0 {
		t.Errorf("outside the rectangle = %+v, want transparent", got)
	}
}

// TestRoundRectRoundsItsCorners is what separates this from Fill: the
// exact corner pixel must be less covered than the middle of the edge.
func TestRoundRectRoundsItsCorners(t *testing.T) {
	img := newCleared(60, 40)
	RoundRect(img, image.Rect(5, 5, 55, 35), 12, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	corner := at(img, 5, 5)
	edge := at(img, 30, 5)
	if corner.A >= edge.A {
		t.Errorf("corner alpha %d is not less than edge alpha %d; the corners are not rounded",
			corner.A, edge.A)
	}
	if corner.A > 40 {
		t.Errorf("corner alpha %d is too high for a 12px radius", corner.A)
	}
}

// TestRoundRectPremultiplies is the bug that made translucent hairlines
// glow: a translucent colour written straight through produces a pixel
// whose colour channels exceed its alpha, which is not a valid
// premultiplied pixel and which a compositor renders far too bright.
func TestRoundRectPremultiplies(t *testing.T) {
	img := newCleared(40, 40)
	RoundRect(img, image.Rect(5, 5, 35, 35), 4, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x40})

	got := at(img, 20, 20)
	if got.R > got.A || got.G > got.A || got.B > got.A {
		t.Errorf("pixel %+v has colour above its alpha; it is not premultiplied", got)
	}
	if got.A != 0x40 {
		t.Errorf("alpha = %d, want 0x40", got.A)
	}
}

func TestFillBlendComposites(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	Fill(img, img.Bounds(), color.RGBA{R: 0, G: 0, B: 0, A: 0xff})
	FillBlend(img, img.Bounds(), color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x80})

	got := at(img, 4, 4)
	if got.A != 0xff {
		t.Errorf("alpha = %d, want an opaque result over an opaque base", got.A)
	}
	// Half of white over black is a mid grey, give or take rounding.
	if got.R < 0x76 || got.R > 0x8a {
		t.Errorf("red = %d, want about 0x80", got.R)
	}
}

func TestFillBlendOpaqueIsExact(t *testing.T) {
	img := newCleared(8, 8)
	c := color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xff}
	FillBlend(img, img.Bounds(), c)
	if got := at(img, 4, 4); got != c {
		t.Errorf("got %+v, want %+v", got, c)
	}
}

func TestRoundRectClipsToImage(t *testing.T) {
	img := newCleared(20, 20)
	// Entirely outside, straddling, and degenerate: none may panic.
	RoundRect(img, image.Rect(-100, -100, -50, -50), 5, color.RGBA{A: 0xff})
	RoundRect(img, image.Rect(-10, -10, 10, 10), 5, color.RGBA{A: 0xff})
	RoundRect(img, image.Rect(5, 5, 5, 5), 5, color.RGBA{A: 0xff})
	RoundRect(img, image.Rect(0, 0, 20, 20), 1000, color.RGBA{A: 0xff})
}

func TestRoundRectTransparentIsANoOp(t *testing.T) {
	img := newCleared(20, 20)
	RoundRect(img, img.Bounds(), 4, color.RGBA{R: 0xff, A: 0})
	for i, v := range img.Pix {
		if v != 0 {
			t.Fatalf("byte %d changed to %d drawing a fully transparent colour", i, v)
		}
	}
}

func TestCircle(t *testing.T) {
	img := newCleared(40, 40)
	Circle(img, 20, 20, 10, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	if got := at(img, 20, 20); got.A != 0xff {
		t.Errorf("centre alpha = %d, want opaque", got.A)
	}
	// Just outside the radius, on the diagonal, must be clear.
	if got := at(img, 29, 29); got.A != 0 {
		t.Errorf("outside the circle alpha = %d, want 0", got.A)
	}
	if got := at(img, 20, 11); got.A == 0 {
		t.Error("a point just inside the top of the circle is transparent")
	}
}

func TestOverRespectsAlpha(t *testing.T) {
	dst := newCleared(20, 20)
	Fill(dst, dst.Bounds(), color.RGBA{R: 0, G: 0, B: 0, A: 0xff})

	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	Fill(src, src.Bounds(), color.RGBA{R: 0xff, G: 0, B: 0, A: 0xff})

	Over(dst, src, 5, 5)
	if got := at(dst, 6, 6); got.R != 0xff {
		t.Errorf("covered pixel = %+v, want red", got)
	}
	if got := at(dst, 1, 1); got.R != 0 {
		t.Errorf("uncovered pixel = %+v, want untouched", got)
	}
}

func TestOverAlphaFades(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	Fill(src, src.Bounds(), color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	full := newCleared(10, 10)
	OverAlpha(full, src, 0, 0, 1)
	half := newCleared(10, 10)
	OverAlpha(half, src, 0, 0, 0.5)
	none := newCleared(10, 10)
	OverAlpha(none, src, 0, 0, 0)

	if at(full, 1, 1).A != 0xff {
		t.Error("alpha 1 did not draw opaquely")
	}
	if a := at(half, 1, 1).A; a < 0x70 || a > 0x90 {
		t.Errorf("alpha 0.5 gave %d, want about 0x80", a)
	}
	if at(none, 1, 1).A != 0 {
		t.Error("alpha 0 drew something")
	}
}

// FuzzRoundRect throws arbitrary rectangles and radii at the shape code.
// The sizes come from a display scale and an item count, so they are not
// fixed constants, and drawing must never panic or corrupt a pixel.
func FuzzRoundRect(f *testing.F) {
	f.Add(0, 0, 10, 10, 3.0, uint8(0xff))
	f.Add(-5, -5, 3, 3, 100.0, uint8(0x40))
	f.Fuzz(func(t *testing.T, x0, y0, x1, y1 int, radius float64, alpha uint8) {
		const limit = 1 << 12
		for _, v := range []int{x0, y0, x1, y1} {
			if v < -limit || v > limit {
				t.Skip()
			}
		}
		if radius != radius || radius > limit { // NaN or absurd
			t.Skip()
		}
		img := newCleared(32, 32)
		RoundRect(img, image.Rect(x0, y0, x1, y1), radius,
			color.RGBA{R: 0xff, G: 0x80, B: 0x40, A: alpha})

		// Every pixel must remain a valid premultiplied colour.
		for i := 0; i+3 < len(img.Pix); i += 4 {
			a := img.Pix[i+3]
			if img.Pix[i] > a || img.Pix[i+1] > a || img.Pix[i+2] > a {
				t.Fatalf("pixel %d is not premultiplied: %v over alpha %d",
					i/4, img.Pix[i:i+3], a)
			}
		}
	})
}

// TestOverMatchesImageDraw pins the byte arithmetic in overRGBA to the
// 16-bit arithmetic it replaced. A least significant bit of difference is
// the deal; anything more is a compositing bug that would show as a halo
// around every icon.
func TestOverMatchesImageDraw(t *testing.T) {
	const size = 40
	src := image.NewRGBA(image.Rect(0, 0, size, size))
	r := rand.New(rand.NewSource(7))
	for i := 0; i < len(src.Pix); i += 4 {
		// Premultiplied, which is what every icon and every scaled image
		// in this program is: no channel may exceed alpha.
		a := byte(r.Intn(256))
		src.Pix[i+0] = byte(r.Intn(int(a) + 1))
		src.Pix[i+1] = byte(r.Intn(int(a) + 1))
		src.Pix[i+2] = byte(r.Intn(int(a) + 1))
		src.Pix[i+3] = a
	}

	// Every offset that matters: inside, clipped at each edge, and wholly
	// outside. Each starts from the same destination, because a one-bit
	// rounding difference is the deal and stacking six of them is not.
	for _, p := range []image.Point{{X: 10, Y: 12}, {X: -7, Y: 5}, {X: 5, Y: -7},
		{X: size*2 - 3, Y: 4}, {X: 4, Y: size*2 - 3}, {X: 500, Y: 500}} {
		mine := image.NewRGBA(image.Rect(0, 0, size*2, size*2))
		theirs := image.NewRGBA(mine.Bounds())
		for i := range mine.Pix {
			v := byte(r.Intn(256))
			mine.Pix[i], theirs.Pix[i] = v, v
		}

		Over(mine, src, p.X, p.Y)
		b := src.Bounds()
		dr := image.Rect(p.X, p.Y, p.X+b.Dx(), p.Y+b.Dy()).Intersect(theirs.Bounds())
		// image/draw aligns sp with dr.Min, so a clipped rectangle needs
		// the source point moved by the same amount or it draws a shifted
		// picture rather than a cropped one.
		draw.Draw(theirs, dr, src, b.Min.Add(dr.Min.Sub(image.Pt(p.X, p.Y))), draw.Over)

		for i := range mine.Pix {
			if diff := int(mine.Pix[i]) - int(theirs.Pix[i]); diff < -1 || diff > 1 {
				t.Fatalf("at %v byte %d: got %d, image/draw says %d", p, i, mine.Pix[i], theirs.Pix[i])
			}
		}
	}
}

// TestRoundRectOnClearMatchesRoundRect is the assumption the fast path
// rests on: over a transparent destination, a blend and a premultiplied
// fill are the same picture.
func TestRoundRectOnClearMatchesRoundRect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		radius float64
		c      color.RGBA
	}{
		{"translucent plate", 17, color.RGBA{R: 0x28, G: 0x28, B: 0x2f, A: 0xbe}},
		{"opaque plate", 12, color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}},
		{"square corners", 0, color.RGBA{R: 0x40, G: 0x10, B: 0x90, A: 0x80}},
		{"capsule", 400, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x2b}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := image.Rect(3, 5, 90, 44)
			blended := image.NewRGBA(image.Rect(0, 0, 100, 50))
			filled := image.NewRGBA(blended.Bounds())
			RoundRect(blended, r, tc.radius, tc.c)
			RoundRectOnClear(filled, r, tc.radius, tc.c)
			for i := range blended.Pix {
				if diff := int(blended.Pix[i]) - int(filled.Pix[i]); diff < -1 || diff > 1 {
					t.Fatalf("byte %d: blended %d, filled %d", i, blended.Pix[i], filled.Pix[i])
				}
			}
		})
	}
}

// The dock repaints its whole surface on every frame of every animation
// and on every pointer motion, so these two are the frame budget. A dock
// panel is roughly 950 by 130 pixels and an icon roughly 120 square.
func BenchmarkRoundRect(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 993, 293))
	r := image.Rect(20, 140, 973, 279)
	c := color.RGBA{R: 0x28, G: 0x28, B: 0x2f, A: 0xbe}
	b.Run("blended", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			RoundRect(img, r, 34, c)
		}
	})
	b.Run("on clear", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			RoundRectOnClear(img, r, 34, c)
		}
	})
}

func BenchmarkOver(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 993, 293))
	src := image.NewRGBA(image.Rect(0, 0, 120, 120))
	r := rand.New(rand.NewSource(3))
	for i := 0; i < len(src.Pix); i += 4 {
		a := byte(r.Intn(256))
		src.Pix[i+0] = byte(r.Intn(int(a) + 1))
		src.Pix[i+1] = byte(r.Intn(int(a) + 1))
		src.Pix[i+2] = byte(r.Intn(int(a) + 1))
		src.Pix[i+3] = a
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Over(dst, src, 100, 120)
	}
}
