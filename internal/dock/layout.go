// Package dock is the dock's model: what is in it, how big each thing is
// drawn, and where the panel currently sits as it slides in and out.
//
// None of it touches X. The magnification curve and the slide are the two
// parts most likely to feel wrong rather than to fail outright, and they
// are exactly the parts worth being able to test without a display.
package dock

import "math"

// Kind distinguishes the three things a slot can hold.
type Kind int

const (
	// KindApp is a launcher: pinned, running, or both.
	KindApp Kind = iota

	// KindStack is a folder that opens a list instead of an application --
	// Downloads and Trash.
	KindStack

	// KindSeparator is the vertical hairline between the applications and
	// the stacks. It occupies a slot so that the layout arithmetic does
	// not need a special case, but it never magnifies and cannot be
	// clicked.
	KindSeparator
)

// Item is one slot in the dock.
type Item struct {
	// Icon is the key into the embedded icon set.
	Icon string

	// Name is what the tooltip shows.
	Name string

	Kind Kind

	// DesktopID identifies the application, for matching windows to it and
	// for launching. Empty for stacks and separators.
	DesktopID string

	// Pinned items are always present and always in the same order.
	// Unpinned ones are running applications that come and go.
	Pinned bool

	// Windows is how many top-level windows the application has open. Zero
	// means not running, and the indicator dot is drawn only above zero.
	Windows int
}

// Metrics is every size the dock draws at, in real (already scaled)
// pixels.
type Metrics struct {
	// Scale is the display scale factor, kept so that callers can size
	// things this struct does not cover.
	Scale float64

	// Icon is the size of an unmagnified icon.
	Icon float64

	// MaxZoom is how much larger the icon under the cursor becomes. 1
	// disables magnification entirely.
	MaxZoom float64

	// Influence is how far from the cursor, in pixels, magnification
	// reaches. Wider means a gentler, longer swell.
	Influence float64

	// Gap is the space between two adjacent unmagnified icons.
	Gap float64

	// Padding is the space between the outermost icon and the panel edge.
	Padding float64

	// SeparatorW is the width of a separator slot.
	SeparatorW float64

	// BottomGap is how far the panel floats above the bottom of the
	// screen.
	BottomGap float64

	// DotRadius and DotGap size the running indicator below an icon.
	DotRadius float64
	DotGap    float64

	// Radius is the panel's corner rounding.
	Radius float64
}

// Placement is where and how large one item is drawn.
type Placement struct {
	// CentreX is the horizontal centre of the icon.
	CentreX float64

	// Size is the icon's side length in pixels.
	Size float64
}

// Layout computes the size and position of every item.
//
// The two-pass shape is what makes magnification behave. Sizes are decided
// from where the icons would sit if nothing were magnified, so that the
// swell follows the cursor rather than chasing its own displacement; only
// then are the icons packed left to right at their new sizes. Deciding
// size from the magnified positions instead produces a layout that shifts
// under the cursor and oscillates.
//
// zoom scales the whole effect from 0 (flat) to 1 (full), which is what
// the dock eases when the pointer arrives and leaves. centreX is where the
// row is centred, and cursorX is in the same coordinate space.
func Layout(items []Item, m *Metrics, centreX, cursorX, zoom float64) []Placement {
	if len(items) == 0 {
		return nil
	}
	out := make([]Placement, len(items))

	// Pass one: natural centres, with nothing magnified.
	natural := make([]float64, len(items))
	flatWidth := m.Padding * 2
	for i := range items {
		if i > 0 {
			flatWidth += m.Gap
		}
		flatWidth += slotWidth(&items[i], m, m.Icon)
	}
	x := centreX - flatWidth/2 + m.Padding
	for i := range items {
		w := slotWidth(&items[i], m, m.Icon)
		natural[i] = x + w/2
		x += w + m.Gap
	}

	// Pass two: sizes from the cursor's distance to those natural centres.
	sizes := make([]float64, len(items))
	total := m.Padding * 2
	for i := range items {
		if i > 0 {
			total += m.Gap
		}
		sizes[i] = m.Icon
		if items[i].Kind != KindSeparator {
			sizes[i] = m.Icon * magnification(cursorX-natural[i], m, zoom)
		}
		total += slotWidth(&items[i], m, sizes[i])
	}

	// Pass three: pack them at their new sizes, still centred.
	x = centreX - total/2 + m.Padding
	for i := range items {
		w := slotWidth(&items[i], m, sizes[i])
		out[i] = Placement{CentreX: x + w/2, Size: sizes[i]}
		x += w + m.Gap
	}
	return out
}

// slotWidth is how much horizontal room an item takes at a given icon
// size. Only the separator differs from its icon.
func slotWidth(it *Item, m *Metrics, size float64) float64 {
	if it.Kind == KindSeparator {
		return m.SeparatorW
	}
	return size
}

// magnification is the swell curve: 1 far from the cursor, MaxZoom under
// it, with a raised cosine between.
//
// A raised cosine rather than a linear ramp or a bare cosine because its
// slope is zero at both ends. That is what stops the icons from visibly
// jerking as the cursor crosses the edge of the influence radius, which is
// the artefact that makes a magnifying dock feel cheap.
func magnification(dx float64, m *Metrics, zoom float64) float64 {
	if m.MaxZoom <= 1 || zoom <= 0 || m.Influence <= 0 {
		return 1
	}
	d := math.Abs(dx) / m.Influence
	if d >= 1 {
		return 1
	}
	bump := 0.5 * (1 + math.Cos(math.Pi*d))
	if zoom > 1 {
		zoom = 1
	}
	return 1 + (m.MaxZoom-1)*bump*zoom
}

// Quantise snaps an icon size onto a ladder of drawable sizes, anchored on
// the resting size and stepping by one display pixel.
//
// Magnification produces a continuous range of sizes, and scaling an icon
// to a size is the most expensive thing a frame does. Rounding to the
// display's own pixel grid -- two device pixels on a 2x screen, one on an
// unscaled one -- means a sweep across the dock revisits sizes it has
// already produced instead of asking for a new one every frame, which is
// what makes caching them possible at all. The step is below what the eye
// resolves on an icon that is in motion.
//
// The ladder is anchored on Icon rather than on zero so that an icon with
// no magnification on it is drawn at exactly its resting size, never a
// pixel beside it.
func (m *Metrics) Quantise(size float64) int {
	step := int(m.Scale + 0.5)
	if step < 1 {
		step = 1
	}
	rungs := int(math.Round((size - m.Icon) / float64(step)))
	q := int(m.Icon+0.5) + rungs*step
	if q < 1 {
		return 1
	}
	return q
}

// Width is the panel's width for a set of placements.
func Width(items []Item, places []Placement, m *Metrics) float64 {
	if len(places) == 0 {
		return 0
	}
	total := m.Padding * 2
	for i := range places {
		if i > 0 {
			total += m.Gap
		}
		total += slotWidth(&items[i], m, places[i].Size)
	}
	return total
}

// Hit returns the index of the item under a point, or -1.
//
// The test is against the icon's own box rather than its slot, so the gaps
// between icons are not clickable. A dock that launches something because
// the click landed a pixel into a neighbour's padding is worse than one
// that ignores the click.
func Hit(items []Item, places []Placement, baseline, x, y float64) int {
	for i := range places {
		if items[i].Kind == KindSeparator {
			continue
		}
		p := places[i]
		half := p.Size / 2
		if x < p.CentreX-half || x >= p.CentreX+half {
			continue
		}
		// Icons are bottom-aligned on the baseline and grow upward.
		if y < baseline-p.Size || y >= baseline {
			continue
		}
		return i
	}
	return -1
}

// MaxWidth is the widest the panel can ever be for a given set of items.
//
// The dock's window is sized to this once per change of contents, rather
// than resized on every frame of magnification. Resizing a window sixty
// times a second is visible under a compositor -- it flickers, and it
// makes the server reallocate a pixmap each time -- so the window is made
// big enough for the worst case and the panel is drawn centred inside it.
//
// The worst case is found by sampling: the width is a smooth function of
// cursor position with a single broad maximum, so stepping the cursor
// across the row at icon-sized intervals finds it to within a pixel or
// two, and the few pixels of slack cost nothing.
func MaxWidth(items []Item, m *Metrics) float64 {
	if len(items) == 0 {
		return 0
	}
	// A generous span: the cursor only affects the layout from one
	// influence radius outside the row.
	flat := Layout(items, m, 0, math.Inf(1), 0)
	span := Width(items, flat, m)/2 + m.Influence

	step := m.Icon / 4
	if step < 1 {
		step = 1
	}
	widest := Width(items, flat, m)
	for x := -span; x <= span; x += step {
		places := Layout(items, m, 0, x, 1)
		if w := Width(items, places, m); w > widest {
			widest = w
		}
	}
	return widest
}
