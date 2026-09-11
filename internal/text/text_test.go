package text

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
)

func testFace(t *testing.T) font.Face {
	t.Helper()
	parsed, _, err := Load(Candidates)
	if err != nil {
		t.Skipf("no usable system font: %v", err)
	}
	face, err := Face(parsed, 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := face.Close(); err != nil {
			t.Error(err)
		}
	})
	return face
}

func TestLoadReportsEveryPathItTried(t *testing.T) {
	_, _, err := Load([]string{"/nonexistent/a.ttf", "/nonexistent/b.ttf"})
	if err == nil {
		t.Fatal("Load succeeded with no candidates on disk")
	}
	for _, want := range []string{"/nonexistent/a.ttf", "/nonexistent/b.ttf"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestLoadWrapsErrNoFont(t *testing.T) {
	_, _, err := Load([]string{"/nonexistent/a.ttf"})
	if !errors.Is(err, ErrNoFont) {
		t.Errorf("error = %v, want it to wrap ErrNoFont", err)
	}
}

// A candidate that is missing is expected and not worth a word; one that
// is there and broken is a fault, and every such fault must be in the
// error -- not just the last one.
func TestLoadReportsEveryBrokenCandidate(t *testing.T) {
	dir := t.TempDir()
	notAFont := filepath.Join(dir, "broken.ttf")
	if err := os.WriteFile(notAFont, []byte("not a font"), 0o600); err != nil {
		t.Fatal(err)
	}
	alsoNotAFont := filepath.Join(dir, "broken2.ttf")
	if err := os.WriteFile(alsoNotAFont, []byte("nor this"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "a-directory.ttf")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.ttf")

	_, _, err := Load([]string{notAFont, unreadable, missing, alsoNotAFont})
	if !errors.Is(err, ErrNoFont) {
		t.Fatalf("error = %v, want it to wrap ErrNoFont", err)
	}
	msg := err.Error()
	for _, want := range []string{"parsing " + notAFont, "parsing " + alsoNotAFont, "reading " + unreadable} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "reading "+missing) {
		t.Errorf("error reports the missing candidate as a failure:\n%s", msg)
	}
}

// A broken candidate ahead of a good one must not stop the good one
// loading, and is not reported once it has.
func TestLoadSkipsABrokenCandidate(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "broken.ttf")
	if err := os.WriteFile(broken, []byte("not a font"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(Candidates); err != nil {
		t.Skipf("no usable system font: %v", err)
	}
	_, path, err := Load(append([]string{broken}, Candidates...))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path == broken {
		t.Errorf("Load returned the broken candidate")
	}
}

func TestWidthGrowsWithText(t *testing.T) {
	face := testFace(t)
	if Width(face, "") != 0 {
		t.Error("the empty string has a width")
	}
	if Width(face, "mm") <= Width(face, "m") {
		t.Error("two characters are not wider than one")
	}
}

func TestTruncateFits(t *testing.T) {
	face := testFace(t)
	const s = "a rather long application name that will not fit"
	full := Width(face, s)

	if got := Truncate(face, s, full+10); got != s {
		t.Errorf("Truncate shortened a string that already fits: %q", got)
	}
	got := Truncate(face, s, full/2)
	if got == s {
		t.Fatal("Truncate returned the string unchanged when it did not fit")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Truncate(%q) = %q, want an ellipsis", s, got)
	}
	if w := Width(face, got); w > full/2 {
		t.Errorf("truncated to %d pixels, over the %d asked for", w, full/2)
	}
	// A width too small for anything still yields the ellipsis rather than
	// an empty label or a panic.
	if got := Truncate(face, s, 1); got != "…" {
		t.Errorf("Truncate at width 1 = %q, want the ellipsis", got)
	}
}

func TestTruncateCutsRunesNotBytes(t *testing.T) {
	face := testFace(t)
	const s = "ααααααααααααααααααααα"
	got := Truncate(face, s, Width(face, s)/2)
	for _, r := range got {
		if r != 'α' && r != '…' {
			t.Fatalf("Truncate produced %q, which is not made of whole runes", got)
		}
	}
}

func TestDrawMarksThePage(t *testing.T) {
	face := testFace(t)
	img := image.NewRGBA(image.Rect(0, 0, 200, 40))
	end := Draw(img, face, white(), 5, 25, "Ghostty", 0)
	if end <= 5 {
		t.Errorf("Draw ended at %d, having started at 5", end)
	}
	if !anyInk(img) {
		t.Error("Draw left the image blank")
	}
}

func TestDrawCentred(t *testing.T) {
	face := testFace(t)
	img := image.NewRGBA(image.Rect(0, 0, 200, 40))
	DrawCentred(img, face, white(), 100, 25, "Files", 0)

	left, right := inkBounds(img)
	if left < 0 {
		t.Fatal("DrawCentred left the image blank")
	}
	centre := (left + right) / 2
	if centre < 90 || centre > 110 {
		t.Errorf("text centred at %d, want about 100", centre)
	}
}

func FuzzTruncate(f *testing.F) {
	f.Add("hello", 40)
	f.Add("", 0)
	f.Add("ααα", 5)
	f.Fuzz(func(t *testing.T, s string, width int) {
		if len(s) > 1024 || width < -1024 || width > 1<<16 {
			t.Skip()
		}
		parsed, _, err := Load(Candidates)
		if err != nil {
			t.Skip()
		}
		face, err := Face(parsed, 12)
		if err != nil {
			t.Skip()
		}
		defer face.Close() //nolint:errcheck // test cleanup

		got := Truncate(face, s, width)
		if width > 0 && got != s && !strings.HasSuffix(got, "…") {
			t.Fatalf("Truncate(%q, %d) = %q: shortened without marking it", s, width, got)
		}
	})
}

func white() color.RGBA { return color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} }

func anyInk(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			return true
		}
	}
	return false
}

func inkBounds(img *image.RGBA) (left, right int) {
	left, right = -1, -1
	b := img.Bounds()
	for x := b.Min.X; x < b.Max.X; x++ {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if img.Pix[img.PixOffset(x, y)+3] > 0 {
				if left < 0 {
					left = x
				}
				right = x
				break
			}
		}
	}
	return left, right
}
