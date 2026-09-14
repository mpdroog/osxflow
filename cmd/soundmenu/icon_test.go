package main

import (
	"image"
	"math"
	"testing"

	"github.com/mpdroog/osxflow/internal/audio"
)

func TestIconFor(t *testing.T) {
	muted := machine()
	muted.audio.Outputs[0].Muted = true
	loud := machine()
	loud.audio.Outputs[0].Volume = 0.9
	noOutput := machine()
	noOutput.audio.Outputs = nil

	for _, tc := range []struct {
		name string
		v    view
		want iconState
	}{
		{"disconnected", view{}, iconState{}},
		{"no output", noOutput, iconState{}},
		{"quiet", machine(), iconState{present: true, waves: 1}},
		{"loud", loud, iconState{present: true, waves: 3}},
		{"muted", muted, iconState{present: true, muted: true}},
	} {
		if got := iconFor(&tc.v); got != tc.want {
			t.Errorf("%s: iconFor = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTooltipFor(t *testing.T) {
	v := machine()
	for _, tc := range []struct {
		name string
		edit func(*view)
		want string
	}{
		{"playing", func(*view) {}, "25% · Built-in Audio Analog Stereo"},
		{"muted", func(v *view) { v.audio.Outputs[0].Muted = true }, "Muted · Built-in Audio Analog Stereo"},
		{"no output", func(v *view) { v.audio = audio.State{} }, "No sound output"},
		{"disconnected", func(v *view) { v.connected = false }, "Sound server not running"},
	} {
		tc.edit(&v)
		if tip := tooltipFor(&v); tip.Title != "Sound" || tip.Text != tc.want {
			t.Errorf("%s: tooltip %q / %q, want Sound / %q", tc.name, tip.Title, tip.Text, tc.want)
		}
	}
}

func opaque(img *image.RGBA) int {
	n := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0xe0 {
			n++
		}
	}
	return n
}

func TestRenderIcon(t *testing.T) {
	for _, size := range iconSizes {
		img := renderIcon(size, iconState{present: true, waves: 2})
		if img.Bounds().Dx() != size || opaque(img) == 0 {
			t.Errorf("size %d: bounds %v, %d opaque pixels", size, img.Bounds(), opaque(img))
		}
		// Nothing to control: faint all over, nothing near opaque.
		if n := opaque(renderIcon(size, iconState{})); n != 0 {
			t.Errorf("size %d: an icon with no output has %d opaque pixels", size, n)
		}
	}
}

func TestNextVolume(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		up   bool
		want float64
	}{
		{0.25, true, 0.30},
		{0.25, false, 0.20},
		{0.27, true, 0.30}, // off the grid snaps back onto it
		{0.98, true, 1},
		{1, true, 1},
		{0.02, false, 0},
		{0, false, 0},
		{1.4, false, 1}, // over-amplified by something else: brought back in range
		{math.NaN(), true, 0.05},
	} {
		if got := nextVolume(tc.v, tc.up); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("nextVolume(%v, %t) = %v, want %v", tc.v, tc.up, got, tc.want)
		}
	}
}

func TestSegmentsLit(t *testing.T) {
	for _, tc := range []struct {
		level float64
		muted bool
		want  int
	}{{0, false, 0}, {0.25, false, 4}, {0.5, false, 8}, {1, false, 16}, {3, false, 16}, {-1, false, 0}, {0.8, true, 0}, {math.NaN(), false, 0}} {
		if got := segmentsLit(tc.level, tc.muted); got != tc.want {
			t.Errorf("segmentsLit(%v, %t) = %d, want %d", tc.level, tc.muted, got, tc.want)
		}
	}
}

// The overlay's bar lights as many segments as the level says: more light
// at a higher level, and a muted overlay lights none but still draws.
func TestRenderOSD(t *testing.T) {
	const size = 200
	render := func(v osdView) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		renderOSD(img, 1, v)
		return img
	}
	barLight := func(img *image.RGBA) int {
		// Through the middle of the bar, as renderOSD places it. A variable,
		// not a constant expression: Go will not truncate a constant 155.5.
		mid := float64(size)*0.76 + float64(size)*0.035/2
		y := int(mid)
		n := 0
		for x := range size {
			if img.RGBAAt(x, y).R > 0xc0 {
				n++
			}
		}
		return n
	}
	quiet, loud := render(osdView{level: 0.25}), render(osdView{level: 0.75})
	if barLight(quiet) == 0 || barLight(loud) <= barLight(quiet) {
		t.Errorf("bar light at 25%%: %d, at 75%%: %d; want more at 75%%", barLight(quiet), barLight(loud))
	}
	if n := barLight(render(osdView{level: 0.75, muted: true})); n != 0 {
		t.Errorf("a muted overlay lights %d bar pixels", n)
	}
	for _, v := range []osdView{{kind: osdMic, level: 1}, {kind: osdMic, level: 1, muted: true}} {
		if opaque(render(v)) == 0 {
			t.Errorf("microphone overlay %+v draws nothing", v)
		}
	}
	// The corners are outside the rounded panel.
	if a := quiet.RGBAAt(0, 0).A; a != 0 {
		t.Errorf("corner alpha %d, want 0", a)
	}
}
