package ui

// Font loading, without fontconfig.
//
// Asking fontconfig for a font would mean cgo, so the candidates below are
// hardcoded paths instead. That is less general than it looks: the list
// only has to contain one font that exists, the paths are stable across
// Debian and Arch derivatives, and a launcher needs exactly one typeface.
// Ubuntu is first because it is what XFCE's own Gtk/FontName setting names
// on the machine this was built for, so the launcher matches the desktop
// around it.

import (
	"fmt"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

var fontCandidates = []string{
	"/usr/share/fonts/truetype/ubuntu/Ubuntu-R.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
	"/usr/share/fonts/noto/NotoSans-Regular.ttf",
}

// faces holds the sizes the interface draws at, all from one typeface.
type faces struct {
	query  font.Face // the text the user is typing
	name   font.Face // application names
	detail font.Face // the second line of a row
}

func (f *faces) close() {
	for _, face := range []font.Face{f.query, f.name, f.detail} {
		if face == nil {
			continue
		}
		// Closing a face frees a cache; there is nothing to do about a
		// failure and nothing that depends on it having worked.
		if err := face.Close(); err != nil {
			_ = err
		}
	}
}

// loadFaces finds a usable font and opens the three sizes.
func loadFaces(m *metrics) (*faces, error) {
	parsed, path, err := loadFont(fontCandidates)
	if err != nil {
		return nil, err
	}

	out := &faces{}
	for _, spec := range []struct {
		dst  *font.Face
		size float64
	}{
		{&out.query, m.queryFontSize},
		{&out.name, m.nameFontSize},
		{&out.detail, m.detailFontSize},
	} {
		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
			Size: spec.size,
			DPI:  fontDPI,
			// Full hinting keeps small text sharp at these sizes, which is
			// the difference between a list that reads instantly and one
			// that looks blurry.
			Hinting: font.HintingFull,
		})
		if err != nil {
			out.close()
			return nil, fmt.Errorf("opening %s at %gpt: %w", path, spec.size, err)
		}
		*spec.dst = face
	}
	return out, nil
}

// loadFont returns the first candidate that exists and parses, naming
// every path it tried when none does -- a launcher that will not start
// should say what it was looking for.
func loadFont(candidates []string) (*sfnt.Font, string, error) {
	var lastErr error
	for _, path := range candidates {
		data, err := os.ReadFile(path) //nolint:gosec // paths are a fixed list in this file
		if err != nil {
			continue
		}
		parsed, err := opentype.Parse(data)
		if err != nil {
			lastErr = fmt.Errorf("parsing %s: %w", path, err)
			continue
		}
		return parsed, path, nil
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("no usable font found; tried %v", candidates)
}
