package text

import (
	"strings"

	"golang.org/x/image/font"
)

// Wrap breaks s into lines no wider than maxWidth pixels, and at most
// maxLines of them. When the text needs more lines than that, the last one
// shown ends in an ellipsis.
//
// Lines break at whitespace. A word wider than a whole line -- a URL, most
// often -- is broken between runes instead, because the alternative is a
// line that runs off the edge of whatever it is drawn in. A newline in s
// starts a new line; blank lines are dropped, since in a box a few lines
// tall an empty one is space the text needed.
func Wrap(face font.Face, s string, maxWidth, maxLines int) []string {
	lines, _ := wrap(face, s, maxWidth, maxLines)
	return lines
}

// wrap is Wrap, also reporting whether anything was left out.
func wrap(face font.Face, s string, maxWidth, maxLines int) (lines []string, truncated bool) {
	if maxWidth <= 0 || maxLines <= 0 {
		return nil, strings.TrimSpace(s) != ""
	}
	w := &wrapper{face: face, width: maxWidth, limit: maxLines}
	for _, para := range strings.Split(s, "\n") {
		for _, word := range strings.Fields(para) {
			w.word(word)
			if w.full() {
				break
			}
		}
		w.endLine()
		if w.full() {
			break
		}
	}
	if !w.full() {
		return w.lines, false
	}
	lines = w.lines[:maxLines]
	lines[maxLines-1] = withEllipsis(face, lines[maxLines-1], maxWidth)
	return lines, true
}

// wrapper accumulates lines greedily.
//
// It keeps going until it has one line more than can be shown, which is
// the cheapest way to know for certain that the text overflowed: the extra
// line is thrown away, and its existence is what earns the ellipsis.
type wrapper struct {
	face  font.Face
	width int
	limit int
	lines []string
	cur   string
}

func (w *wrapper) full() bool { return len(w.lines) > w.limit }

// word adds one word to the line being built, starting a new line when it
// does not fit.
//
// The candidate line is measured whole rather than by summing word widths:
// kerning across the space makes the sum slightly wrong, and a line that
// measures a pixel over is a line whose last glyph gets clipped.
func (w *wrapper) word(word string) {
	if w.cur != "" {
		if candidate := w.cur + " " + word; Width(w.face, candidate) <= w.width {
			w.cur = candidate
			return
		}
		w.endLine()
	}
	for Width(w.face, word) > w.width && !w.full() {
		head, tail := splitFit(w.face, word, w.width)
		w.lines = append(w.lines, head)
		word = tail
	}
	w.cur = word
}

func (w *wrapper) endLine() {
	if w.cur != "" {
		w.lines = append(w.lines, w.cur)
		w.cur = ""
	}
}

// splitFit returns the longest prefix of word that fits in width, and the
// rest. The prefix always has at least one rune, even when that rune alone
// is too wide, so that a caller breaking a word in a loop always makes
// progress.
func splitFit(face font.Face, word string, width int) (head, tail string) {
	rs := []rune(word)
	// Binary search on the prefix length. Width grows with every rune
	// added -- kerning can shave a pixel, never a whole glyph -- so the
	// predicate is monotonic enough for this to find the right answer.
	lo, hi := 1, len(rs)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if Width(face, string(rs[:mid])) <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(rs[:lo]), string(rs[lo:])
}

// withEllipsis marks a line as cut short, shortening it as far as needed
// to make room.
//
// It is not Truncate: Truncate adds an ellipsis only to text that does not
// fit, and this line fits by construction. What did not fit is the text
// after it.
func withEllipsis(face font.Face, line string, width int) string {
	line = strings.TrimRight(line, " ")
	if Width(face, line+ellipsis) <= width {
		return line + ellipsis
	}
	rs := []rune(line)
	for n := len(rs) - 1; n > 0; n-- {
		head := strings.TrimRight(string(rs[:n]), " ")
		if Width(face, head+ellipsis) <= width {
			return head + ellipsis
		}
	}
	return ellipsis
}
