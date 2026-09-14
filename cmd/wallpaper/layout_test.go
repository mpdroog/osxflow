package main

import (
	"image"
	"image/color"
	"testing"
)

// This machine: a 3840x2160 picture on a 2560x1600 screen.
var (
	picture = image.Rect(0, 0, 3840, 2160)
	screenW = 2560
	screenH = 1600
)

func TestPlacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		st       style
		from, to image.Rectangle
	}{
		// 1600/2160 is the larger scale; at it the screen covers 3456 of
		// the picture's 3840 columns, 192 cut from each side.
		{"zoom", styleZoom, image.Rect(192, 0, 3648, 2160), image.Rect(0, 0, 2560, 1600)},
		// 2560/3840 is the smaller scale: 2560x1440, 80 rows of bars.
		{"fit", styleFit, picture, image.Rect(0, 80, 2560, 1520)},
		{"stretch", styleStretch, picture, image.Rect(0, 0, 2560, 1600)},
		// Actual size: the middle 2560x1600 of the picture.
		{"center", styleCenter, image.Rect(640, 280, 3200, 1880), image.Rect(0, 0, 2560, 1600)},
	} {
		from, to := placement(picture, screenW, screenH, tc.st)
		if from != tc.from || to != tc.to {
			t.Errorf("%s: placement = %v -> %v, want %v -> %v", tc.name, from, to, tc.from, tc.to)
		}
	}
}

// A picture smaller than the screen, centred, keeps its size and sits in
// the middle; zoomed, it still fills the screen.
func TestPlacementSmallPicture(t *testing.T) {
	small := image.Rect(0, 0, 800, 600)
	from, to := placement(small, screenW, screenH, styleCenter)
	if from != small || to != image.Rect(880, 500, 1680, 1100) {
		t.Errorf("center: %v -> %v", from, to)
	}
	from, to = placement(small, screenW, screenH, styleZoom)
	if to != image.Rect(0, 0, screenW, screenH) || !from.In(small) || from.Empty() {
		t.Errorf("zoom: %v -> %v", from, to)
	}
}

func TestPlacementEmpty(t *testing.T) {
	for _, tc := range []struct {
		src  image.Rectangle
		w, h int
	}{{image.Rectangle{}, 2560, 1600}, {picture, 0, 1600}, {picture, 2560, -1}} {
		for st := styleZoom; st <= styleCenter; st++ {
			if from, to := placement(tc.src, tc.w, tc.h, st); !from.Empty() || !to.Empty() {
				t.Errorf("placement(%v, %d, %d, %d) = %v -> %v, want nothing", tc.src, tc.w, tc.h, st, from, to)
			}
		}
	}
}

func TestParseStyle(t *testing.T) {
	for in, want := range map[string]style{
		"zoom": styleZoom, "zoomed": styleZoom, "fit": styleFit, "scaled": styleFit,
		"stretch": styleStretch, "center": styleCenter, "centre": styleCenter,
	} {
		if got, err := parseStyle(in); err != nil || got != want {
			t.Errorf("parseStyle(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseStyle("tile"); err == nil {
		t.Error("parseStyle(tile) succeeded")
	}
}

func TestRender(t *testing.T) {
	// A picture with a red left half and a blue right half, fitted to a
	// wider screen: black bars at the sides, red and blue inside.
	pic := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := range 40 {
		for x := range 40 {
			c := color.RGBA{R: 255, A: 255}
			if x >= 20 {
				c = color.RGBA{B: 255, A: 255}
			}
			pic.SetRGBA(x, y, c)
		}
	}
	frame, err := render(pic, 100, 50, styleFit)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Bounds() != image.Rect(0, 0, 100, 50) {
		t.Fatalf("frame is %v", frame.Bounds())
	}
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{
		{5, 25, color.RGBA{A: 255}},          // left bar
		{95, 25, color.RGBA{A: 255}},         // right bar
		{35, 25, color.RGBA{R: 255, A: 255}}, // red half
		{65, 25, color.RGBA{B: 255, A: 255}}, // blue half
	} {
		if got := frame.RGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("pixel %d,%d = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}

	// Same size in and out: copied, not scaled.
	same, err := render(pic, 40, 40, styleStretch)
	if err != nil || same.RGBAAt(0, 0) != pic.RGBAAt(0, 0) || same.RGBAAt(39, 39) != pic.RGBAAt(39, 39) {
		t.Errorf("unscaled render: %v", err)
	}

	if _, err := render(image.NewRGBA(image.Rectangle{}), 100, 50, styleZoom); err == nil {
		t.Error("an empty picture rendered")
	}
}
