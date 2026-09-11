// Package text loads a font and draws strings, without fontconfig.
//
// Asking fontconfig which font to use would mean cgo, so the candidates
// below are hardcoded paths. That is less fragile than it looks: the list
// only has to contain one font that exists, these paths are stable across
// Debian and Arch derivatives, and these tools need exactly one typeface.
// Ubuntu is first because it is what XFCE's own Gtk/FontName names on this
// machine, so the tools match the desktop around them.
package text

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io/fs"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Candidates is the font search path, best first.
var Candidates = []string{
	"/usr/share/fonts/truetype/ubuntu/Ubuntu-R.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
	"/usr/share/fonts/noto/NotoSans-Regular.ttf",
}

// BoldCandidates is the search path for the heavier weight used to set a
// title apart from the text under it. Ubuntu Medium comes first: at
// notification sizes Bold is heavier than the distinction needs.
var BoldCandidates = []string{
	"/usr/share/fonts/truetype/ubuntu/Ubuntu-M.ttf",
	"/usr/share/fonts/truetype/ubuntu/Ubuntu-B.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
	"/usr/share/fonts/TTF/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSansBold.ttf",
	"/usr/share/fonts/noto/NotoSans-Bold.ttf",
}

// DPI is fixed at the X11 default. Display scaling is applied to point
// sizes instead, so that one number controls everything.
const DPI = 72 * 96 / 72

// ErrNoFont is wrapped by Load's error when no candidate could be used.
var ErrNoFont = errors.New("no usable font found")

// Load returns the first candidate that exists and parses, naming every
// path it tried when none does.
//
// A candidate that does not exist is the expected case -- the list covers
// several distributions and any one machine has one or two of them -- so
// it is skipped without comment. Anything else (a font that cannot be read
// or does not parse) is a real fault on this machine: once a later
// candidate loads it is not in the way and is not reported, but when none
// does, every such failure is in the error, because any of them could be
// the one the user meant to have.
func Load(candidates []string) (*sfnt.Font, string, error) {
	var errs []error
	for _, path := range candidates {
		data, err := os.ReadFile(path) //nolint:gosec // a fixed list of system font paths
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // not installed on this distribution
			}
			errs = append(errs, fmt.Errorf("reading %s: %w", path, err))
			continue
		}
		parsed, err := opentype.Parse(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("parsing %s: %w", path, err))
			continue
		}
		return parsed, path, nil
	}
	return nil, "", errors.Join(append([]error{fmt.Errorf("%w; tried %v", ErrNoFont, candidates)}, errs...)...)
}

// Face opens one size of a parsed font, hinted for sharpness at small
// sizes.
func Face(f *sfnt.Font, sizePt float64) (font.Face, error) {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    sizePt,
		DPI:     DPI,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("opening a %gpt face: %w", sizePt, err)
	}
	return face, nil
}

// Width measures a string in pixels.
func Width(face font.Face, s string) int {
	return font.MeasureString(face, s).Round()
}

// Draw paints a string at a baseline and returns where it ended. A
// maxWidth of zero or less means no truncation.
func Draw(img *image.RGBA, face font.Face, c color.Color, x, baseline int, s string, maxWidth int) int {
	if maxWidth > 0 {
		s = Truncate(face, s, maxWidth)
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	d.DrawString(s)
	return d.Dot.X.Round()
}

// DrawCentred paints a string centred on x.
func DrawCentred(img *image.RGBA, face font.Face, c color.Color, centreX, baseline int, s string, maxWidth int) {
	if maxWidth > 0 {
		s = Truncate(face, s, maxWidth)
	}
	Draw(img, face, c, centreX-Width(face, s)/2, baseline, s, 0)
}

const ellipsis = "…"

// Truncate shortens s until it fits, cutting runes rather than bytes.
//
// It is a linear scan from the end rather than a binary search because
// these are application and file names: the loop runs a handful of times
// at most, and the obvious version is easier to be sure of.
func Truncate(face font.Face, s string, maxWidth int) string {
	if maxWidth <= 0 || Width(face, s) <= maxWidth {
		return s
	}
	rs := []rune(s)
	ellipsisW := Width(face, ellipsis)
	for n := len(rs) - 1; n > 0; n-- {
		if Width(face, string(rs[:n]))+ellipsisW <= maxWidth {
			return string(rs[:n]) + ellipsis
		}
	}
	return ellipsis
}
