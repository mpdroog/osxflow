package text

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWrapKeepsShortTextOnOneLine(t *testing.T) {
	face := testFace(t)
	lines, cut := wrap(face, "hello world", 1000, 3)
	if cut || !slices.Equal(lines, []string{"hello world"}) {
		t.Fatalf("wrap = %q (cut %t), want one untouched line", lines, cut)
	}
}

func TestWrapBreaksAtSpacesWithinTheWidth(t *testing.T) {
	face := testFace(t)
	s := "the quick brown fox jumps over the lazy dog and keeps on running"
	width := Width(face, "the quick brown")
	lines, cut := wrap(face, s, width, 20)
	if cut {
		t.Fatalf("wrap cut text that had twenty lines to fit in: %q", lines)
	}
	if len(lines) < 3 {
		t.Fatalf("wrap = %q, want several lines at this width", lines)
	}
	for _, line := range lines {
		if Width(face, line) > width {
			t.Errorf("line %q is %dpx, wider than %d", line, Width(face, line), width)
		}
	}
	if got := strings.Join(lines, " "); got != s {
		t.Errorf("rejoined lines = %q, want the input back", got)
	}
}

func TestWrapBreaksAWordTooLongForALine(t *testing.T) {
	face := testFace(t)
	url := "https://example.com/" + strings.Repeat("abcdefgh", 20)
	const width = 100
	lines, cut := wrap(face, url, width, 50)
	if cut {
		t.Fatalf("wrap cut the URL: %q", lines)
	}
	if len(lines) < 2 {
		t.Fatalf("wrap = %q, want the URL broken across lines", lines)
	}
	for _, line := range lines {
		if Width(face, line) > width {
			t.Errorf("line %q is %dpx, wider than %d", line, Width(face, line), width)
		}
	}
	if got := strings.Join(lines, ""); got != url {
		t.Errorf("rejoined pieces = %q, want the URL back", got)
	}
}

func TestWrapMarksTruncation(t *testing.T) {
	face := testFace(t)
	const width = 120
	lines, cut := wrap(face, strings.Repeat("word ", 200), width, 2)
	if !cut || len(lines) != 2 {
		t.Fatalf("wrap = %q (cut %t), want two lines and a cut", lines, cut)
	}
	if !strings.HasSuffix(lines[1], "…") {
		t.Errorf("last line %q does not end in an ellipsis", lines[1])
	}
	if Width(face, lines[1]) > width {
		t.Errorf("last line %q is wider than %d once the ellipsis is added", lines[1], width)
	}
}

func TestWrapTruncatesAtAParagraphBreak(t *testing.T) {
	face := testFace(t)
	lines, cut := wrap(face, "one\ntwo\nthree", 1000, 2)
	if !cut || !slices.Equal(lines, []string{"one", "two…"}) {
		t.Fatalf("wrap = %q (cut %t), want [one two…] and a cut", lines, cut)
	}
}

func TestWrapStartsNewLinesAtNewlinesAndDropsBlankOnes(t *testing.T) {
	face := testFace(t)
	if got, want := Wrap(face, "one\n\n  \ntwo", 1000, 5), []string{"one", "two"}; !slices.Equal(got, want) {
		t.Fatalf("Wrap = %q, want %q", got, want)
	}
}

func TestWrapWithNoRoom(t *testing.T) {
	face := testFace(t)
	if got := Wrap(face, "text", 0, 3); got != nil {
		t.Errorf("Wrap at width 0 = %q, want nothing", got)
	}
	if got := Wrap(face, "text", 100, 0); got != nil {
		t.Errorf("Wrap into 0 lines = %q, want nothing", got)
	}
}

// FuzzWrap checks the promises Wrap makes: never more lines than asked,
// never a line wider than asked unless it is a single glyph that cannot be
// split, and nothing lost unless it says so.
func FuzzWrap(f *testing.F) {
	f.Add("hello world", 80, 3)
	f.Add("a\nb\nc", 10, 2)
	f.Add(strings.Repeat("x", 300), 30, 4)
	f.Add("ααα βββ γγγ", 5, 1)
	f.Add("  leading and trailing  ", 50, 2)

	parsed, _, err := Load(Candidates)
	if err != nil {
		f.Skip("no usable system font")
	}
	face, err := Face(parsed, 12)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() {
		if closeErr := face.Close(); closeErr != nil {
			f.Error(closeErr)
		}
	})

	f.Fuzz(func(t *testing.T, s string, width, limit int) {
		if len(s) > 2048 || width < -10 || width > 2000 || limit < -1 || limit > 20 {
			t.Skip()
		}
		lines, cut := wrap(face, s, width, limit)
		if len(lines) > max(limit, 0) {
			t.Fatalf("%d lines, limit %d", len(lines), limit)
		}
		for _, line := range lines {
			if utf8.RuneCountInString(line) > 1 && Width(face, line) > width {
				t.Fatalf("line %q is %dpx, over %d", line, Width(face, line), width)
			}
		}
		if !cut && width > 0 && limit > 0 && utf8.ValidString(s) {
			if got, want := squash(strings.Join(lines, "")), squash(s); got != want {
				t.Fatalf("text lost without a cut: got %q, want %q", got, want)
			}
		}
	})
}

// squash removes all whitespace, which is the only thing wrapping may
// change about text it does not cut.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }
