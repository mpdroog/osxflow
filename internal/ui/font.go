package ui

// Font loading, without fontconfig.
//
// Finding and parsing the typeface is internal/text's job, shared with the
// other tools so there is one candidate list and one set of rules about
// what counts as a font that failed to load. This file only opens the
// sizes the launcher draws at.

import (
	"errors"
	"fmt"

	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/text"
)

// faces holds the sizes the interface draws at, all from one typeface.
type faces struct {
	query  font.Face // the text the user is typing
	name   font.Face // application names
	detail font.Face // the second line of a row
}

// close frees the faces' glyph caches. Nothing depends on it having
// worked, so the caller reports a failure rather than acting on it.
func (f *faces) close() error {
	var errs []error
	for _, face := range []font.Face{f.query, f.name, f.detail} {
		if face == nil {
			continue
		}
		if err := face.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing a font face: %w", err))
		}
	}
	return errors.Join(errs...)
}

// loadFaces finds a usable font and opens the three sizes.
func loadFaces(m *metrics) (*faces, error) {
	parsed, path, err := text.Load(text.Candidates)
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
		// text.Face hints fully, which keeps small text sharp at these
		// sizes: the difference between a list that reads instantly and
		// one that looks blurry.
		face, err := text.Face(parsed, spec.size)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("%s: %w", path, err), out.close())
		}
		*spec.dst = face
	}
	return out, nil
}
