package icons

import (
	"testing"

	"github.com/mpdroog/osxflow/internal/text"
)

// TestFallbackLetterIsLight guards against the letter coming out dark: it
// was once drawn in an invalid premultiplied colour, which blended into a
// near-black glyph on every placeholder tile.
func TestFallbackLetterIsLight(t *testing.T) {
	parsed, _, err := text.Load(text.Candidates)
	if err != nil {
		t.Skipf("no usable system font: %v", err)
	}
	face, err := text.Face(parsed, 58)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := face.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	img := Fallback("Hover", MasterSize, face)
	tint := tintFor("Hover")
	lighter, darker := 0, 0
	// The middle of the tile only: its anti-aliased edge fades toward
	// transparent, which reads as dark and is not the letter.
	inset := MasterSize / 5
	for y := inset; y < MasterSize-inset; y++ {
		for x := inset; x < MasterSize-inset; x++ {
			c := img.RGBAAt(x, y)
			switch {
			case c.R > tint.R+60 && c.G > tint.G+60 && c.B > tint.B+60:
				lighter++
			case int(c.R)+int(c.G)+int(c.B) < int(tint.R)+int(tint.G)+int(tint.B)-120:
				darker++
			}
		}
	}
	if lighter == 0 {
		t.Error("no pixel of the letter is lighter than the tile")
	}
	if darker > 0 {
		t.Errorf("%d pixels are much darker than the tile: the letter is not white", darker)
	}
}
