package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/text"
)

func TestNewMetricsScales(t *testing.T) {
	one := newMetrics(1)
	two := newMetrics(2)

	if two.windowWidth != 2*one.windowWidth {
		t.Errorf("width %d at 2x vs %d at 1x, want double", two.windowWidth, one.windowWidth)
	}
	if two.rowHeight != 2*one.rowHeight {
		t.Errorf("row height %d at 2x vs %d at 1x, want double", two.rowHeight, one.rowHeight)
	}
	if two.queryFontSize != 2*one.queryFontSize {
		t.Errorf("font %g at 2x vs %g at 1x, want double", two.queryFontSize, one.queryFontSize)
	}
	// A hairline must never round away to nothing.
	if one.separatorH < 1 || newMetrics(0.4).separatorH < 1 {
		t.Error("separator rounded to zero at a small scale")
	}
}

func TestNewMetricsRejectsNonPositiveScale(t *testing.T) {
	for _, s := range []float64{0, -1} {
		if got := newMetrics(s).scale; got != 1 {
			t.Errorf("newMetrics(%g).scale = %g, want 1", s, got)
		}
	}
}

// The bug this pins: windowHeight left out the top padding, so the last
// row was clipped by exactly one padding and looked like a drawing fault.
func TestMetricsHeightIncludesEveryPart(t *testing.T) {
	for _, scale := range []float64{1, 1.5, 2} {
		m := newMetrics(scale)
		lastRowBottom := m.rowTop(visibleRows-1) + m.rowHeight
		if m.windowHeight < lastRowBottom {
			t.Errorf("scale %g: window is %dpx but the last row ends at %dpx",
				scale, m.windowHeight, lastRowBottom)
		}
	}
}

func TestMetricsHeightForRowCount(t *testing.T) {
	m := newMetrics(1)

	// No results: just the query box, and still a real window.
	empty := m.heightFor(0)
	if empty <= 0 || empty >= m.heightFor(1) {
		t.Errorf("heightFor(0) = %d, want a positive height below heightFor(1) = %d", empty, m.heightFor(1))
	}
	// Each extra row adds exactly one row of height.
	if got, want := m.heightFor(3)-m.heightFor(2), m.rowHeight; got != want {
		t.Errorf("a row added %d px, want %d", got, want)
	}
	// And it never exceeds the full window.
	if got := m.heightFor(visibleRows + 50); got != m.windowHeight {
		t.Errorf("heightFor(many) = %d, want the full %d", got, m.windowHeight)
	}
	if m.heightFor(-1) != empty {
		t.Error("a negative row count should behave like zero")
	}
}

func TestRowTopIsMonotonic(t *testing.T) {
	m := newMetrics(2)
	for i := 1; i < visibleRows; i++ {
		if m.rowTop(i) <= m.rowTop(i-1) {
			t.Fatalf("row %d starts at or above row %d", i, i-1)
		}
	}
	if m.rowTop(0) < m.separatorY() {
		t.Error("the first row overlaps the separator")
	}
}

func TestScaleFromXfconfFile(t *testing.T) {
	dir := t.TempDir()

	// The real shape of XFCE's xsettings.xml, trimmed.
	good := filepath.Join(dir, "good.xml")
	writeFile(t, good, `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Gdk" type="empty">
    <property name="WindowScalingFactor" type="int" value="2"/>
  </property>
</channel>`)
	if got, err := scaleFromXfconfFile(good); err != nil || got != 2 {
		t.Errorf("scale = %g err=%v, want 2 nil", got, err)
	}

	// The same file as it looks with scaling off.
	off := filepath.Join(dir, "off.xml")
	writeFile(t, off, `<channel name="xsettings"><property name="Gdk" type="empty">
	<property name="WindowScalingFactor" type="int" value="1"/></property></channel>`)
	if got, err := scaleFromXfconfFile(off); err != nil || got != 1 {
		t.Errorf("scale = %g err=%v, want 1 nil", got, err)
	}

	// Absent files and absent keys decline quietly; malformed and
	// wrong-typed ones decline with an error. None of them guess.
	for name, tc := range map[string]struct {
		content string
		unset   bool
	}{
		"missing.xml": {"", true},
		"empty.xml":   {`<channel name="xsettings"></channel>`, true},
		"broken.xml":  {"<channel><not closed", false},
		"wrongtype":   {`<channel><property name="Gdk"><property name="WindowScalingFactor" type="string" value="2"/></property></channel>`, false},
		"zero.xml":    {`<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="0"/></property></channel>`, false},
	} {
		path := filepath.Join(dir, name)
		if tc.content != "" {
			writeFile(t, path, tc.content)
		}
		got, err := scaleFromXfconfFile(path)
		if err == nil {
			t.Errorf("%s: reported a scale of %g, want none", name, got)
			continue
		}
		if errors.Is(err, scale.ErrUnset) != tc.unset {
			t.Errorf("%s: error = %v, want unset=%v", name, err, tc.unset)
		}
	}
}

func TestScaleFromEnv(t *testing.T) {
	t.Setenv("GDK_SCALE", "2")
	if got, err := scaleFromEnv(); err != nil || got != 2 {
		t.Errorf("scaleFromEnv = %g err=%v, want 2 nil", got, err)
	}
	t.Setenv("GDK_SCALE", "")
	if _, err := scaleFromEnv(); !errors.Is(err, scale.ErrUnset) {
		t.Errorf("GDK_SCALE=\"\": error = %v, want ErrUnset", err)
	}
	for _, bad := range []string{"0", "-1", "two", "NaN", "Inf"} {
		t.Setenv("GDK_SCALE", bad)
		if got, err := scaleFromEnv(); err == nil || errors.Is(err, scale.ErrUnset) {
			t.Errorf("GDK_SCALE=%q: got %g, %v; want it rejected with an error", bad, got, err)
		}
	}
}

// An explicit override must win over everything, which is the point of
// the -scale flag.
func TestDetectScaleOverrideWins(t *testing.T) {
	t.Setenv("GDK_SCALE", "3")
	if got, err := DetectScale(nil, 1.25); err != nil || got != 1.25 {
		t.Errorf("DetectScale = %g, %v; want the 1.25 override", got, err)
	}
}

func TestDetectScaleFallsBackToOne(t *testing.T) {
	t.Setenv("GDK_SCALE", "")
	t.Setenv("HOME", t.TempDir()) // no xfconf file there
	got, err := DetectScale(nil, 0)
	if got != 1 {
		t.Errorf("DetectScale = %g, want 1 with nothing to go on", got)
	}
	// An unscaled desktop is not a fault, so nothing to log.
	if err != nil {
		t.Errorf("DetectScale error = %v, want none when nothing is configured", err)
	}
}

func TestKeysymRune(t *testing.T) {
	tests := []struct {
		name string
		ks   uint32
		want rune
		ok   bool
	}{
		{"ascii a", 'a', 'a', true},
		{"ascii Z", 'Z', 'Z', true},
		{"digit", '7', '7', true},
		{"space", 0x20, ' ', true},
		{"plus", '+', '+', true},
		{"asterisk", '*', '*', true},
		{"latin1 e-acute", 0xe9, 'é', true},
		{"keypad 0", ksKP0, '0', true},
		{"keypad 9", ksKP9, '9', true},
		{"keypad 5", ksKP0 + 5, '5', true},
		{"unicode range", 0x01000100 + 0x20ac - 0x100, 0, false}, // computed below
		{"escape is not text", ksEscape, 0, false},
		{"return is not text", ksReturn, 0, false},
		{"backspace is not text", ksBackSpace, 0, false},
		{"arrow is not text", ksUp, 0, false},
		{"zero", 0, 0, false},
	}
	for _, tc := range tests {
		if tc.name == "unicode range" {
			continue // covered separately, the table entry is unreadable
		}
		t.Run(tc.name, func(t *testing.T) {
			got, ok := keysymRune(xproto.Keysym(tc.ks))
			if ok != tc.ok {
				t.Fatalf("keysymRune(%#x) ok = %v, want %v", tc.ks, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("keysymRune(%#x) = %q, want %q", tc.ks, got, tc.want)
			}
		})
	}

	// The Unicode keysym range: 0x01000000 + code point.
	if got, ok := keysymRune(xproto.Keysym(0x01000000 + 0x20ac)); !ok || got != '€' {
		t.Errorf("keysymRune(euro) = %q ok=%v, want € true", got, ok)
	}
}

func TestCaseHelpersAreASCIIOnly(t *testing.T) {
	if toUpper('a') != 'A' || toLower('Z') != 'z' {
		t.Error("ASCII case conversion is wrong")
	}
	// Caps Lock never produced these, so they must be left alone.
	for _, r := range []rune{'é', 'ß', '1', '+', 'É'} {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if toUpper(r) != r && (r < 'a' || r > 'z') {
			t.Errorf("toUpper(%q) changed a non-ASCII-letter rune", r)
		}
	}
}

func TestTruncate(t *testing.T) {
	face := testFace(t)

	short := "Files"
	if got := truncate(face, short, 10000); got != short {
		t.Errorf("truncate widened a short string to %q", got)
	}

	long := "A Very Long Application Name That Will Not Fit At All"
	got := truncate(face, long, 100)
	if got == long {
		t.Fatal("truncate returned the string unchanged despite it not fitting")
	}
	if !hasSuffix(got, ellipsis) {
		t.Errorf("truncate = %q, want it to end in an ellipsis", got)
	}
	if font.MeasureString(face, got).Round() > 100 {
		t.Errorf("truncate = %q, which is still wider than 100px", got)
	}

	// Degenerate widths must terminate rather than loop or panic.
	for _, w := range []int{0, -5, 1} {
		_ = truncate(face, long, w)
	}
}

func testFace(t *testing.T) font.Face {
	t.Helper()
	parsed, _, err := text.Load(text.Candidates)
	if err != nil {
		t.Skipf("no system font available: %v", err)
	}
	face, err := text.Face(parsed, 12)
	if err != nil {
		t.Fatalf("opening a face: %v", err)
	}
	t.Cleanup(func() {
		if err := face.Close(); err != nil {
			t.Errorf("closing the face: %v", err)
		}
	})
	return face
}

func TestLoadFacesUsesTheSystemFont(t *testing.T) {
	f, err := loadFaces(newMetrics(1))
	if err != nil {
		t.Skipf("no system font available: %v", err)
	}
	t.Cleanup(func() {
		if err := f.close(); err != nil {
			t.Errorf("closing the faces: %v", err)
		}
	})
	if f.query == nil || f.name == nil || f.detail == nil {
		t.Fatal("loadFaces returned a nil face")
	}
	// The query face must be visibly larger than the detail face, or the
	// hierarchy the layout depends on is gone.
	q := font.MeasureString(f.query, "example").Round()
	d := font.MeasureString(f.detail, "example").Round()
	if q <= d {
		t.Errorf("query text measures %d and detail %d, want the query larger", q, d)
	}
}

func TestRowsPerRequest(t *testing.T) {
	// The real constraint: a request that exceeds the server's maximum
	// drops the connection, so this must never return something too big.
	const width = 620
	got := rowsPerRequest(65535, width)
	if got < 1 {
		t.Fatalf("rowsPerRequest = %d, want at least 1", got)
	}
	if got*width*4 > 65535*4 {
		t.Errorf("rowsPerRequest = %d, which exceeds the request budget", got)
	}
	// A tiny maximum must still make progress rather than returning 0.
	if got := rowsPerRequest(16, width); got != 1 {
		t.Errorf("rowsPerRequest with a tiny limit = %d, want 1", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }

func TestMetricsRowAt(t *testing.T) {
	m := newMetrics(1)

	// The query area and the separator are not rows.
	for _, y := range []int{0, m.padding, m.separatorY(), m.rowTop(0) - 1} {
		if got := m.rowAt(y); got != -1 {
			t.Errorf("rowAt(%d) = %d, want -1 (not a row)", y, got)
		}
	}

	// The top and bottom pixel of each row map to that row.
	for i := range visibleRows {
		top := m.rowTop(i)
		if got := m.rowAt(top); got != i {
			t.Errorf("rowAt(top of row %d) = %d, want %d", i, got, i)
		}
		if got := m.rowAt(top + m.rowHeight - 1); got != i {
			t.Errorf("rowAt(bottom of row %d) = %d, want %d", i, got, i)
		}
	}

	// Below the last row is not a row either.
	if got := m.rowAt(m.rowTop(visibleRows-1) + m.rowHeight); got != -1 {
		t.Errorf("rowAt(below the list) = %d, want -1", got)
	}
	if got := m.rowAt(-5); got != -1 {
		t.Errorf("rowAt(negative) = %d, want -1", got)
	}
}

func TestMetricsContains(t *testing.T) {
	m := newMetrics(1)
	h := m.heightFor(3)

	inside := [][2]int{{0, 0}, {m.windowWidth - 1, h - 1}, {m.windowWidth / 2, h / 2}}
	for _, p := range inside {
		if !m.contains(p[0], p[1], h) {
			t.Errorf("contains(%d, %d, %d) = false, want true", p[0], p[1], h)
		}
	}

	// Clicks elsewhere on the screen arrive with coordinates relative to
	// this window, so they land outside its bounds -- including negative.
	outside := [][2]int{
		{-1, 0}, {0, -1}, {m.windowWidth, 0}, {0, h},
		{-500, -300}, {m.windowWidth + 100, h + 100},
	}
	for _, p := range outside {
		if m.contains(p[0], p[1], h) {
			t.Errorf("contains(%d, %d, %d) = true, want false", p[0], p[1], h)
		}
	}
}

// The dead space below a short list belongs to the desktop, not the
// window: testing against the full window height would swallow those
// clicks instead of dismissing.
func TestMetricsContainsUsesTheCurrentHeight(t *testing.T) {
	m := newMetrics(1)
	short := m.heightFor(1)
	if short >= m.windowHeight {
		t.Fatal("fixture: a one-row window should be shorter than a full one")
	}
	belowShortWindow := short + 10
	if m.contains(10, belowShortWindow, short) {
		t.Error("a click below a shrunk window was treated as inside it")
	}
	if !m.contains(10, belowShortWindow, m.windowHeight) {
		t.Error("the same point should be inside a full-height window")
	}
}
