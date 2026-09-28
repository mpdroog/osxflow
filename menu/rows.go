// Package menu is the dropdown the osxflow tray tools open from the panel:
// a translucent, rounded, macOS-style menu of rows -- switches, sliders,
// lists, media buttons -- in an X11 window.
//
// A tool describes the menu as a list of Rows, each carrying what clicking
// or dragging it does, and hands the list to a Host. When the state behind
// the menu changes, it builds the list again and calls Update. The Host
// owns the window, the pointer grab, hovering, dragging and drawing.
//
// This file is the part a user notices without a display in sight --
// where each row sits, what a click lands on, what a drag sets a slider
// to -- and is tested as such.
package menu

import (
	"image"
	"image/color"
	"math"

	"github.com/mpdroog/osxflow/internal/glyph"
)

// Kind is what a row is.
type Kind int

// The kinds of row.
const (
	Toggle    Kind = iota // bold label with a switch
	Header                // bold label with a dim value at the right
	Section               // small dim heading
	Note                  // dim line of text
	Item                  // badge, label, detail, and a lock or switch
	Slider                // symbol and a draggable track
	Transport             // a centred row of buttons
	Separator             // hairline
	Action                // plain label, such as "Settings…"
	kindCount
)

// Trailing is what sits at the right end of an Item.
type Trailing int

// What an Item can end with.
const (
	TrailingNone Trailing = iota
	TrailingSwitch
	TrailingLock
)

// Glyph draws a symbol in the square at x0, y0 with side s.
type Glyph func(dst *image.RGBA, x0, y0, s float64, col color.RGBA)

// Symbol makes a Glyph from one of internal/glyph's single-colour shapes.
func Symbol(shape func(x0, y0, s float64) glyph.SDF) Glyph {
	return func(dst *image.RGBA, x0, y0, s float64, col color.RGBA) {
		glyph.Fill(dst, glyph.Area(x0, y0, s, s), col, shape(x0, y0, s))
	}
}

// Button is one button of a Transport row. A nil Click draws it dimmed and
// inert, the way a player that cannot go back shows its back button.
type Button struct {
	Glyph Glyph
	Click func()
}

// Row is one line of a menu.
type Row struct {
	Kind   Kind
	Label  string
	Detail string

	// On is a switch's position -- a Toggle's, or an Item's with
	// TrailingSwitch -- and whether an Item's badge is lit.
	On bool

	// Glyph is drawn in an Item's badge and at a Slider's left end.
	Glyph Glyph

	Trailing Trailing

	// Value is a Slider's position, 0 to 1.
	Value float64

	// Click is what clicking the row does; nil makes the row inert. The
	// menu closes first, unless KeepOpen is set -- as it should be for a
	// switch, which is expected to be seen flipping.
	Click    func()
	KeepOpen bool

	// Slide receives a Slider's value while it is dragged, with final set
	// once it is released, and for each notch of the wheel over it.
	Slide func(value float64, final bool)

	// Buttons are a Transport row's. Transport buttons never close the
	// menu.
	Buttons []Button
}

// sliderStep is how far one notch of the wheel moves a slider.
const sliderStep = 0.05

func (r *Row) interactive() bool {
	switch r.Kind {
	case Slider:
		return r.Slide != nil
	case Transport:
		for i := range r.Buttons {
			if r.Buttons[i].Click != nil {
				return true
			}
		}
		return false
	case Section, Note, Separator:
		return false
	case Toggle, Header, Item, Action, kindCount:
	}
	return r.Click != nil
}

// hovers reports whether the whole row lights up under the pointer. A
// slider does not, and a Transport row lights only the button.
func (r *Row) hovers() bool {
	return r.Kind != Slider && r.Kind != Transport && r.interactive()
}

// layout places rows top to bottom and returns each row's top edge and the
// menu's height.
func layout(rows []Row, t *Theme) (tops []float64, total float64) {
	tops = make([]float64, len(rows))
	y := t.padY
	for i := range rows {
		tops[i] = y
		y += t.height(rows[i].Kind)
	}
	return tops, y + t.padY
}

// hit finds what a point in menu coordinates is over: the row, or -1 when
// that is no row or one that does nothing, and for a Transport row the
// button, or -1.
func hit(rows []Row, tops []float64, t *Theme, x, y float64) (row, button int) {
	for i := range rows {
		r := &rows[i]
		if y < tops[i] || y >= tops[i]+t.height(r.Kind) {
			continue
		}
		if !r.interactive() {
			return -1, -1
		}
		if r.Kind != Transport {
			return i, -1
		}
		for b, cx := range buttonCentres(t, len(r.Buttons)) {
			if math.Abs(x-cx) <= t.button/2 && r.Buttons[b].Click != nil {
				return i, b
			}
		}
		return -1, -1
	}
	return -1, -1
}

// buttonCentres spaces n Transport buttons evenly about the menu's middle.
func buttonCentres(t *Theme, n int) []float64 {
	mid := float64(t.Width) / 2
	step := t.button * 1.5
	out := make([]float64, n)
	for i := range n {
		out[i] = mid + (float64(i)-float64(n-1)/2)*step
	}
	return out
}

// sliderTrack is where a slider's knob centre can travel, from value 0 at
// lo to 1 at hi. The knob stays wholly on the track at both ends.
func sliderTrack(t *Theme) (lo, hi float64) {
	start := t.padX + t.sliderGlyph + t.padX*0.6
	end := float64(t.Width) - t.padX
	r := t.knob / 2
	return start + r, end - r
}

// sliderValue is the value a slider takes with the pointer at x.
func sliderValue(t *Theme, x float64) float64 {
	lo, hi := sliderTrack(t)
	if hi <= lo {
		return 0
	}
	return clamp01((x - lo) / (hi - lo))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
