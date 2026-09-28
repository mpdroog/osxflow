package sni

// The tray's side of a pixmap: turning what an item sent back into
// something to draw.

import (
	"errors"
	"fmt"
	"image"
	"math"
)

// maxPixmapSide bounds what ToImage accepts. Icons are tens of pixels; an
// item claiming a side of a million is broken or hostile, and would have
// the tray allocate for it.
const maxPixmapSide = 1024

// ToImage decodes a pixmap into a premultiplied image, the inverse of
// FromImage. It checks the size against the data rather than trusting
// either: both come from another process.
func ToImage(p *Pixmap) (*image.RGBA, error) {
	w, h := int(p.Width), int(p.Height)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("pixmap is %dx%d", w, h)
	}
	if w > maxPixmapSide || h > maxPixmapSide {
		return nil, fmt.Errorf("pixmap is %dx%d, more than %d a side", w, h, maxPixmapSide)
	}
	if len(p.Pix) != 4*w*h {
		return nil, fmt.Errorf("pixmap is %dx%d but has %d bytes, want %d", w, h, len(p.Pix), 4*w*h)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(p.Pix); i += 4 {
		a := p.Pix[i]
		img.Pix[i] = premultiply(p.Pix[i+1], a)
		img.Pix[i+1] = premultiply(p.Pix[i+2], a)
		img.Pix[i+2] = premultiply(p.Pix[i+3], a)
		img.Pix[i+3] = a
	}
	return img, nil
}

func premultiply(c, a uint8) uint8 {
	v := (uint32(c)*uint32(a) + 127) / 255
	if v > math.MaxUint8 {
		// Cannot happen -- 255*255+127 over 255 is 255 -- but the check
		// is what lets the conversion below be seen to be safe.
		return math.MaxUint8
	}
	return uint8(v)
}

// ErrNoPixmap means an item sent no usable pixmap at all.
var ErrNoPixmap = errors.New("no usable pixmap")

// Best picks the pixmap to draw at size pixels: the smallest that is at
// least that big, since scaling down looks better than scaling up, or else
// the biggest there is. Pixmaps that do not decode are skipped; what was
// wrong with them is returned alongside the one chosen, or joined into the
// error when none was usable.
func Best(pixmaps []Pixmap, size int) (*image.RGBA, []error) {
	var best *image.RGBA
	var problems []error
	for i := range pixmaps {
		img, err := ToImage(&pixmaps[i])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if best == nil || better(img.Bounds().Dx(), best.Bounds().Dx(), size) {
			best = img
		}
	}
	if best == nil {
		problems = append(problems, ErrNoPixmap)
	}
	return best, problems
}

// better reports whether a pixmap side is a better fit for size than cur.
func better(side, cur, size int) bool {
	switch {
	case side >= size && cur >= size:
		return side < cur
	case side >= size:
		return true
	case cur >= size:
		return false
	}
	return side > cur
}
