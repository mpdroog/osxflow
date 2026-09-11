package main

// Drawing a banner. Each one is redrawn whole whenever anything about it
// changes -- it is a few hundred thousand pixels and that happens a handful
// of times in its life, so there is nothing to gain from tracking damage.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
)

type faces struct {
	title    font.Face
	body     font.Face
	button   font.Face
	glyph    font.Face
	fallback font.Face

	// closeMark is what the close button shows: see closeMark.
	closeMark string
}

func loadFaces(th *theme) (*faces, error) {
	regular, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, fmt.Errorf("loading regular font: %w", err)
	}
	// A missing bold weight costs the title its emphasis, not the daemon
	// its ability to start. It is worth a line all the same: the fix is a
	// font package, and nothing else would say so.
	bold, _, boldErr := text.Load(text.BoldCandidates)
	if boldErr != nil {
		log.Printf("no bold font, using regular: %v", boldErr)
		bold = regular
	}
	out := &faces{}
	for _, spec := range []struct {
		name string
		dst  *font.Face
		font *sfnt.Font
		size float64
	}{
		{"title", &out.title, bold, th.titlePt},
		{"body", &out.body, regular, th.bodyPt},
		{"button", &out.button, regular, th.buttonPt},
		{"close button", &out.glyph, bold, th.closePt},
		// For a letter on a placeholder tile, drawn at the icon master
		// size and scaled down, as the dock does.
		{"placeholder", &out.fallback, bold, 58},
	} {
		face, faceErr := text.Face(spec.font, spec.size)
		if faceErr != nil {
			return nil, errors.Join(fmt.Errorf("%s face: %w", spec.name, faceErr), out.close())
		}
		*spec.dst = face
	}
	var ok bool
	if out.closeMark, ok = closeMark(out.glyph); !ok {
		log.Printf("the bold font has no %q; the close button shows %q", preferredCloseMark, out.closeMark)
	}
	return out, nil
}

// preferredCloseMark is the close button's glyph, as on macOS.
const preferredCloseMark = '×'

// closeMark picks the close button's glyph: the multiplication sign, or,
// from a font without one, a plain x. Drawing a glyph the font lacks draws
// nothing, which leaves a button with no hint of what it does. It reports
// whether the preferred mark was available.
func closeMark(face font.Face) (string, bool) {
	if _, ok := face.GlyphAdvance(preferredCloseMark); ok {
		return string(preferredCloseMark), true
	}
	return "x", false
}

// close releases every face. Closing one only frees a cache, so a failure
// costs nothing, but it is reported all the same.
func (f *faces) close() error {
	var errs []error
	for _, face := range []font.Face{f.title, f.body, f.button, f.glyph, f.fallback} {
		if face == nil {
			continue
		}
		if err := face.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing a font face: %w", err))
		}
	}
	return errors.Join(errs...)
}

// render draws a banner in its current state and puts it on screen.
func (d *daemon) render(b *banner) {
	img := b.surf.Image()
	paint.Clear(img)
	th, l := d.th, &b.lay

	drawShadow(img, l.plate, th)
	paint.RoundRect(img, l.plate, th.radius, colPlate)
	// A hairline of light along the top edge, as on the dock: without it
	// the plate reads as a flat shape lying on the wallpaper.
	edge := colEdge
	if b.n.Urgency == notify.Critical {
		edge = colEdgeCritical
	}
	paint.FillBlend(img, image.Rect(l.plate.Min.X+int(th.radius), l.plate.Min.Y,
		l.plate.Max.X-int(th.radius), l.plate.Min.Y+max(int(th.scale), 1)), edge)

	if b.icon != nil {
		ib := b.icon.Bounds()
		paint.Over(img, b.icon, l.icon.Min.X+(l.icon.Dx()-ib.Dx())/2, l.icon.Min.Y+(l.icon.Dy()-ib.Dy())/2)
	}

	if l.title != "" {
		text.Draw(img, d.faces.title, colTitle, l.textX, l.titleBase, l.title, l.textW)
	}
	for i, line := range l.body {
		text.Draw(img, d.faces.body, colBody, l.textX, l.bodyBase[i], line, 0)
	}
	if !l.bar.Empty() {
		radius := float64(l.bar.Dy()) / 2
		paint.RoundRect(img, l.bar, radius, colBarTrack)
		fill := l.bar
		fill.Max.X = fill.Min.X + l.bar.Dx()*b.n.Value/100
		if fill.Dx() > 0 {
			paint.RoundRect(img, fill, radius, colBarFill)
		}
	}

	for i := range l.buttons {
		btn := &l.buttons[i]
		bg := colButton
		if b.hover && b.part.kind == partButton && b.part.index == i {
			bg = colButtonHover
		}
		paint.RoundRect(img, btn.rect, th.buttonRadius, bg)
		text.DrawCentred(img, d.faces.button, colButtonText, btn.rect.Min.X+btn.rect.Dx()/2,
			baseline(d.faces.button, btn.rect.Min.Y, btn.rect.Dy()), btn.label, 0)
	}

	// The close button appears only under the pointer, as on macOS: a
	// stack of banners each wearing one is a stack of clutter.
	if b.hover {
		bg := colClose
		if b.part.kind == partClose {
			bg = colCloseHover
		}
		paint.Circle(img, l.closeX, l.closeY, l.closeR, bg)
		drawGlyphCentred(img, d.faces.glyph, colCloseText, l.closeX, l.closeY, d.faces.closeMark)
	}
	d.flush(b)
}

// drawShadow lays a soft shadow under the plate: a few rounded rectangles,
// each a little larger and all very faint, whose overlap darkens toward the
// middle. xfwm4 draws no shadow for an override-redirect window, and a
// translucent plate without one sinks into a busy wallpaper.
func drawShadow(img *image.RGBA, plate image.Rectangle, th *theme) {
	const layers = 5
	for i := layers; i >= 1; i-- {
		grow := (th.shadow*i + layers/2) / layers
		paint.RoundRect(img, plate.Inset(-grow), th.radius+float64(grow), colShadow)
	}
}

// drawGlyphCentred centres a string on a point by its ink, not its
// metrics. A glyph like "×" sits well above the baseline and well below
// the ascent, so centring the font's box would put it visibly low in a
// button that small.
func drawGlyphCentred(img *image.RGBA, face font.Face, c color.Color, cx, cy int, s string) {
	bounds, _ := font.BoundString(face, s)
	midX := (bounds.Min.X + bounds.Max.X) / 2
	midY := (bounds.Min.Y + bounds.Max.Y) / 2
	x := fixed.I(cx) - midX
	y := fixed.I(cy) - midY
	text.Draw(img, face, c, x.Round(), y.Round(), s, 0)
}
