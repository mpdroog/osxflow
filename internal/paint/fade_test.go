package paint

import (
	"image"
	"math"
	"slices"
	"testing"
)

func fadeSource() *image.RGBA {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	copy(src.Pix, []byte{200, 100, 50, 200, 255, 255, 255, 255})
	return src
}

func TestFadeScalesEveryChannel(t *testing.T) {
	src := fadeSource()
	dst := image.NewRGBA(src.Bounds())
	Fade(dst, src, 0.5)
	// alpha 0.5 is 128/255, so each channel c becomes (c*128+127)/255.
	want := []byte{100, 50, 25, 100, 128, 128, 128, 128}
	if !slices.Equal(dst.Pix, want) {
		t.Fatalf("Fade(0.5) = %v, want %v", dst.Pix, want)
	}
}

func TestFadeStaysPremultiplied(t *testing.T) {
	src := fadeSource()
	dst := image.NewRGBA(src.Bounds())
	for _, alpha := range []float64{0.1, 0.33, 0.77, 0.99} {
		Fade(dst, src, alpha)
		for i := 0; i < len(dst.Pix); i += 4 {
			a := dst.Pix[i+3]
			if dst.Pix[i] > a || dst.Pix[i+1] > a || dst.Pix[i+2] > a {
				t.Fatalf("Fade(%g) pixel %v has a channel above its alpha", alpha, dst.Pix[i:i+4])
			}
		}
	}
}

func TestFadeEnds(t *testing.T) {
	src := fadeSource()
	dst := image.NewRGBA(src.Bounds())

	Fade(dst, src, 1)
	if !slices.Equal(dst.Pix, src.Pix) {
		t.Errorf("Fade(1) = %v, want a copy %v", dst.Pix, src.Pix)
	}
	for _, alpha := range []float64{0, -3, math.NaN()} {
		Fade(dst, src, alpha)
		if slices.ContainsFunc(dst.Pix, func(b byte) bool { return b != 0 }) {
			t.Errorf("Fade(%g) = %v, want fully transparent", alpha, dst.Pix)
		}
	}
	Fade(dst, src, 7)
	if !slices.Equal(dst.Pix, src.Pix) {
		t.Errorf("Fade(7) = %v, want it clamped to a copy", dst.Pix)
	}
}

func TestFadeIgnoresMismatchedBounds(t *testing.T) {
	src := fadeSource()
	dst := image.NewRGBA(image.Rect(0, 0, 1, 2))
	dst.Pix[0] = 9
	Fade(dst, src, 1)
	if dst.Pix[0] != 9 {
		t.Fatal("Fade drew into an image of a different shape")
	}
}
