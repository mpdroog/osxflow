package ui

// Drawing. Everything is painted into an off-screen image and pushed to
// the server in one go, so the window never shows a half-drawn frame.

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// paint renders the whole window. It repaints unconditionally rather than
// tracking damaged regions: the surface is 620x400, the frame is well
// under a millisecond, and invalidation bugs are the most tedious class of
// UI bug there is.
func paint(img *image.RGBA, f *faces, mt *metrics, m *Model) {
	fill(img, img.Bounds(), colBackground)

	drawQuery(img, f, mt, m)

	sepY := mt.separatorY()
	fill(img, image.Rect(0, sepY, mt.windowWidth, sepY+mt.separatorH), colSeparator)

	rows, selected := m.Rows()
	for i, row := range rows {
		drawRow(img, f, mt, row, mt.rowTop(i), i == selected)
	}
}

func drawQuery(img *image.RGBA, f *faces, mt *metrics, m *Model) {
	text, col := m.Query(), colQueryText
	if text == "" {
		text, col = placeholder, colPlaceholder
	}
	baseline := mt.padding + mt.queryBaseline
	end := drawString(img, f.query, col, mt.padding, baseline, text, mt.windowWidth-2*mt.padding)

	// The caret sits after the text, and only when there is text: against
	// the placeholder it would read as part of the hint.
	if m.Query() != "" {
		gap := int(3 * mt.scale)
		up := int(20 * mt.scale)
		down := int(6 * mt.scale)
		fill(img, image.Rect(end+gap, baseline-up, end+gap+mt.cursorW, baseline+down), colQueryText)
	}
}

func drawRow(img *image.RGBA, f *faces, mt *metrics, row Row, top int, selected bool) {
	if selected {
		fill(img, image.Rect(0, top, mt.windowWidth, top+mt.rowHeight), colSelected)
	}

	nameCol, detailCol := colName, colDetail
	switch {
	case selected:
		nameCol, detailCol = colNameSel, colDetailSel
	case row.App == nil:
		// The calculator row, unselected.
		nameCol = colCalc
	}

	width := mt.windowWidth - 2*mt.padding
	drawString(img, f.name, nameCol, mt.padding, top+mt.rowNameBaseline, row.Primary, width)
	if row.Secondary != "" {
		drawString(img, f.detail, detailCol, mt.padding, top+mt.rowDetailBaseline, row.Secondary, width)
	}
}

// fill paints a solid rectangle, clipped to the image.
func fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(img, r.Intersect(img.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

// drawString draws text at a baseline, truncating with an ellipsis when it
// will not fit, and returns the x coordinate where the text ended.
func drawString(img *image.RGBA, face font.Face, c color.Color, x, baseline int, s string, maxWidth int) int {
	s = truncate(face, s, maxWidth)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	d.DrawString(s)
	return d.Dot.X.Round()
}

const ellipsis = "…"

// truncate shortens s until it fits, cutting runes rather than bytes.
//
// It is a linear scan from the end rather than a binary search because the
// strings are application names: the loop runs a handful of times at most,
// and the obvious version is easier to be sure of.
func truncate(face font.Face, s string, maxWidth int) string {
	if maxWidth <= 0 || font.MeasureString(face, s).Round() <= maxWidth {
		return s
	}
	rs := []rune(s)
	ellipsisW := font.MeasureString(face, ellipsis).Round()
	for n := len(rs) - 1; n > 0; n-- {
		if font.MeasureString(face, string(rs[:n])).Round()+ellipsisW <= maxWidth {
			return string(rs[:n]) + ellipsis
		}
	}
	return ellipsis
}

// drawStatus replaces the first result row with a failure message, which
// is where the user is already looking.
func drawStatus(img *image.RGBA, f *faces, mt *metrics, msg string) {
	top := mt.rowTop(0)
	fill(img, image.Rect(0, top, mt.windowWidth, top+mt.rowHeight), colBackground)
	drawString(img, f.name, colCalc, mt.padding, top+mt.rowNameBaseline, msg, mt.windowWidth-2*mt.padding)
}
