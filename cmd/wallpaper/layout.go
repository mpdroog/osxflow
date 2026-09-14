package main

// Where the picture goes: which part of it is used and where on the screen
// it lands, for each way of fitting it. Pure, so the arithmetic is tested
// without a display.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

type style int

// The ways of fitting a picture to the screen, named as xfdesktop's
// settings name them.
const (
	styleZoom    style = iota // fill the screen, cropping what overflows
	styleFit                  // show all of it, with bars where it does not reach
	styleStretch              // fill the screen, ignoring the aspect ratio
	styleCenter               // actual size, in the middle
)

func parseStyle(s string) (style, error) {
	switch s {
	case "zoom", "zoomed":
		return styleZoom, nil
	case "fit", "scaled":
		return styleFit, nil
	case "stretch", "stretched":
		return styleStretch, nil
	case "center", "centre", "centered":
		return styleCenter, nil
	}
	return 0, fmt.Errorf("unknown style %q: want zoom, fit, stretch or center", s)
}

// placement returns the part of a picture of size src to use, and where on
// a width by height screen it goes.
func placement(src image.Rectangle, width, height int, st style) (from, to image.Rectangle) {
	screen := image.Rect(0, 0, width, height)
	sw, sh := float64(src.Dx()), float64(src.Dy())
	if sw <= 0 || sh <= 0 || width <= 0 || height <= 0 {
		return image.Rectangle{}, image.Rectangle{}
	}
	switch st {
	case styleZoom:
		// Scale until both sides cover the screen, then take the middle.
		scale := math.Max(float64(width)/sw, float64(height)/sh)
		cw := min(src.Dx(), int(math.Round(float64(width)/scale)))
		ch := min(src.Dy(), int(math.Round(float64(height)/scale)))
		return centred(src, cw, ch), screen
	case styleFit:
		scale := math.Min(float64(width)/sw, float64(height)/sh)
		dw := min(width, int(math.Round(sw*scale)))
		dh := min(height, int(math.Round(sh*scale)))
		return src, centred(screen, dw, dh)
	case styleCenter:
		w, h := min(src.Dx(), width), min(src.Dy(), height)
		return centred(src, w, h), centred(screen, w, h)
	case styleStretch:
	}
	return src, screen
}

// centred is a w by h rectangle in the middle of r.
func centred(r image.Rectangle, w, h int) image.Rectangle {
	x := r.Min.X + (r.Dx()-w)/2
	y := r.Min.Y + (r.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}

var errNoPicture = errors.New("the picture has no pixels, or the screen has no size")

// render draws the picture onto a black screen-sized image.
func render(pic image.Image, width, height int, st style) (*image.RGBA, error) {
	from, to := placement(pic.Bounds(), width, height, st)
	if from.Empty() || to.Empty() {
		return nil, errNoPicture
	}
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(frame, frame.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	if from.Dx() == to.Dx() && from.Dy() == to.Dy() {
		draw.Copy(frame, to.Min, pic, from, draw.Src, nil)
		return frame, nil
	}
	// Catmull-Rom: a wallpaper is drawn once and looked at all day, so the
	// slowest of x/image's scalers is the right one.
	draw.CatmullRom.Scale(frame, to, pic, from, draw.Src, nil)
	return frame, nil
}
