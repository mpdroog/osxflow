package ui

// Sizes and colours. Everything the eye can see is in this file, so that
// changing the look does not mean reading the drawing code.

import "image/color"

// Base sizes, in logical pixels at scale 1. Every one of them is
// multiplied by the display scale before use -- see metrics.
const (
	baseWindowWidth = 620

	basePadding    = 18
	baseQueryRowH  = 58
	baseRowHeight  = 44
	baseSeparatorH = 1
	baseCursorW    = 2

	// Vertical placement inside a row: the name sits above its detail
	// line, both optically centred.
	baseRowNameBaseline   = 19
	baseRowDetailBaseline = 34
	baseQueryBaseline     = 38

	baseQueryFontSize  = 21
	baseNameFontSize   = 13.5
	baseDetailFontSize = 10.5
)

// visibleRows is how many results are shown. It does not scale: a
// HiDPI screen should show the same list at a larger size, not a longer
// list at the same size.
const visibleRows = 7

// fontDPI is fixed at the X11 default. Display scaling is applied to the
// point sizes instead, so that one number controls everything.
const fontDPI = 96

// metrics is the layout at a particular display scale.
type metrics struct {
	scale float64

	windowWidth  int
	windowHeight int

	padding    int
	queryRowH  int
	rowHeight  int
	separatorH int
	cursorW    int

	rowNameBaseline   int
	rowDetailBaseline int
	queryBaseline     int

	queryFontSize  float64
	nameFontSize   float64
	detailFontSize float64
}

func newMetrics(scale float64) *metrics {
	if scale <= 0 {
		scale = 1
	}
	px := func(v int) int { return int(float64(v)*scale + 0.5) }

	m := metrics{
		scale:             scale,
		windowWidth:       px(baseWindowWidth),
		padding:           px(basePadding),
		queryRowH:         px(baseQueryRowH),
		rowHeight:         px(baseRowHeight),
		separatorH:        max(px(baseSeparatorH), 1),
		cursorW:           max(px(baseCursorW), 1),
		rowNameBaseline:   px(baseRowNameBaseline),
		rowDetailBaseline: px(baseRowDetailBaseline),
		queryBaseline:     px(baseQueryBaseline),
		queryFontSize:     baseQueryFontSize * scale,
		nameFontSize:      baseNameFontSize * scale,
		detailFontSize:    baseDetailFontSize * scale,
	}
	// The top padding is part of the height. Leaving it out clipped the
	// last row by exactly one padding, which looked like a rendering bug
	// rather than an arithmetic one.
	// Top padding, the query, the hairline, the rows, and a little air at
	// the bottom so the last detail line is not flush against the edge.
	m.windowHeight = m.padding + m.queryRowH + m.separatorH + visibleRows*m.rowHeight + m.padding/2
	return &m
}

// heightFor is the window height needed to show n result rows.
//
// The window shrinks to fit rather than reserving space for a full list.
// Rows are anchored under the query, so growing the list never moves a row
// that is already on screen -- the reason a fixed height would otherwise
// be worth having.
func (m *metrics) heightFor(n int) int {
	n = min(max(n, 0), visibleRows)
	h := m.padding + m.queryRowH
	if n > 0 {
		h += m.separatorH + n*m.rowHeight + m.padding/2
	}
	return h
}

// rowTop is the y coordinate of the i'th visible result row.
func (m *metrics) rowTop(i int) int {
	return m.padding + m.queryRowH + m.separatorH + i*m.rowHeight
}

// rowAt maps a y coordinate to a visible row index, or -1 when the point
// is not on a row -- the query area, the separator, or the padding below
// the last row.
func (m *metrics) rowAt(y int) int {
	first := m.rowTop(0)
	if y < first {
		return -1
	}
	i := (y - first) / m.rowHeight
	if i < 0 || i >= visibleRows {
		return -1
	}
	return i
}

// contains reports whether a point is inside a window of the given height.
//
// The height is a parameter rather than m.windowHeight because the window
// shrinks to fit the result list: dismissing on a click has to test
// against the size the window actually is, not the largest it could be,
// or the dead space below a short list would swallow clicks.
func (m *metrics) contains(x, y, height int) bool {
	return x >= 0 && x < m.windowWidth && y >= 0 && y < height
}

// separatorY is the y coordinate of the hairline under the query.
func (m *metrics) separatorY() int { return m.padding + m.queryRowH }

// The palette is dark because a launcher appears over whatever you were
// doing, and a bright panel at that moment is a flashbulb.
var (
	colBackground = color.RGBA{R: 0x1c, G: 0x1c, B: 0x22, A: 0xff}
	colSeparator  = color.RGBA{R: 0x2e, G: 0x2e, B: 0x38, A: 0xff}
	colSelected   = color.RGBA{R: 0x2f, G: 0x54, B: 0x8f, A: 0xff}

	colQueryText = color.RGBA{R: 0xf2, G: 0xf2, B: 0xf5, A: 0xff}
	colName      = color.RGBA{R: 0xe8, G: 0xe8, B: 0xee, A: 0xff}
	colDetail    = color.RGBA{R: 0x8a, G: 0x8a, B: 0x99, A: 0xff}

	// On the selected row the detail line has to lift out of the
	// highlight, so it gets its own lighter grey.
	colNameSel   = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colDetailSel = color.RGBA{R: 0xc3, G: 0xd2, B: 0xea, A: 0xff}

	colPlaceholder = color.RGBA{R: 0x5c, G: 0x5c, B: 0x68, A: 0xff}

	// The calculator result is the one thing that is not an app, and is
	// coloured to say so.
	colCalc = color.RGBA{R: 0x8d, G: 0xd6, B: 0xa0, A: 0xff}
)

// placeholder is shown before anything is typed.
const placeholder = "Search applications, or type a sum"
