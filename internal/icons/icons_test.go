package icons

import (
	"image"
	"testing"
)

// TestBuildProducedTheDocksIcons checks the generated set rather than the
// code: if `make icons` has not been run, or the icon theme stopped
// providing one of these, the dock would draw a placeholder where the
// trash should be. That is a build fault, and this is where it surfaces.
func TestBuildProducedTheDocksIcons(t *testing.T) {
	for _, name := range []string{Downloads, Trash, TrashFull} {
		if !Has(name) {
			t.Errorf("no embedded icon %q; run `make icons`", name)
		}
	}
	if err := Verify([]string{Downloads, Trash, TrashFull}); err != nil {
		t.Error(err)
	}
	if err := Verify([]string{"definitely-not-an-icon"}); err == nil {
		t.Error("Verify accepted an icon that is not embedded")
	}
}

func TestNamesAreSortedAndPresent(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no icons embedded at all; run `make icons`")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names is not sorted: %q before %q", names[i-1], names[i])
		}
	}
	for _, n := range names {
		if !Has(n) {
			t.Errorf("Names listed %q but Has says no", n)
		}
	}
}

func TestMasterIsTheDeclaredSize(t *testing.T) {
	s := New()
	img := s.Master(Downloads)
	if img == nil {
		t.Fatal("no master for the Downloads icon")
	}
	if got := img.Bounds().Dx(); got != MasterSize {
		t.Errorf("master is %dpx, but MasterSize says %d", got, MasterSize)
	}
	if img.Bounds().Dx() != img.Bounds().Dy() {
		t.Errorf("master is not square: %v", img.Bounds())
	}
}

func TestMasterIsCachedAndMissesAreRemembered(t *testing.T) {
	s := New()
	first := s.Master(Trash)
	if first == nil {
		t.Fatal("no master for the trash icon")
	}
	if second := s.Master(Trash); second != first {
		t.Error("Master decoded the same icon twice")
	}
	if got := s.Master("definitely-not-an-icon"); got != nil {
		t.Errorf("Master invented an icon: %v", got.Bounds())
	}
	// The second miss must come from the negative cache, not another
	// attempt to read the embedded filesystem.
	if got := s.Master("definitely-not-an-icon"); got != nil {
		t.Error("a second miss returned something")
	}
}

func TestAtScalesAndCaches(t *testing.T) {
	s := New()
	const size = 64
	img := s.At(Downloads, size)
	if img == nil {
		t.Fatal("At returned nothing")
	}
	if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
		t.Errorf("At gave %v, want %dx%d", img.Bounds(), size, size)
	}
	if again := s.At(Downloads, size); again != img {
		t.Error("At rescaled instead of using its cache")
	}
	if got := s.At(Downloads, 0); got != nil {
		t.Error("At accepted a zero size")
	}
	if got := s.At("definitely-not-an-icon", size); got != nil {
		t.Error("At invented an icon")
	}
}

// TestMagnifiedIsCached is the property that keeps magnification cheap:
// the sizes a sweep passes through are produced once and then reused, and
// the caller may hold the result.
func TestMagnifiedIsCached(t *testing.T) {
	s := New()
	first := s.Magnified(Downloads, 100)
	if first == nil || first.Bounds().Dx() != 100 {
		t.Fatalf("Magnified gave %v", first)
	}
	if again := s.Magnified(Downloads, 100); again != first {
		t.Error("Magnified produced the same size twice instead of caching it")
	}
	// A different icon at the same size is a different image; an earlier
	// version handed out one shared scratch buffer and this is what that
	// would look like.
	other := s.Magnified(Trash, 100)
	if other == nil {
		t.Fatal("Magnified gave nothing for a second icon")
	}
	if &first.Pix[0] == &other.Pix[0] {
		t.Error("two icons share one buffer")
	}
	// At and Magnified differ only in the filter, so a size fetched through
	// one is a size the other already has.
	if got := s.At(Downloads, 100); got != first {
		t.Error("At and Magnified do not share a cache")
	}
	if got := s.Magnified("definitely-not-an-icon", 40); got != nil {
		t.Error("Magnified invented an icon")
	}
	if got := s.Magnified(Downloads, -1); got != nil {
		t.Error("Magnified accepted a negative size")
	}
}

func TestFallbackIsDrawnAndStable(t *testing.T) {
	const size = 48
	a := Fallback("Some New App", size, nil)
	if a == nil || a.Bounds().Dx() != size {
		t.Fatalf("Fallback gave %v", a)
	}
	if !anyOpaque(a) {
		t.Error("the fallback tile is entirely transparent")
	}
	// The same name must give the same colour between runs, or an app
	// changes shade every time the dock restarts.
	b := Fallback("Some New App", size, nil)
	if centre(a) != centre(b) {
		t.Error("the fallback tint is not stable for a given name")
	}
	if centre(a) == centre(Fallback("Another App", size, nil)) {
		t.Error("two different names produced the same tint")
	}
	// Names with no letter at all must still produce a tile.
	if got := Fallback("", size, nil); got == nil || !anyOpaque(got) {
		t.Error("an empty name produced no tile")
	}
	if got := Fallback("!!!", size, nil); got == nil || !anyOpaque(got) {
		t.Error("a name with no letters produced no tile")
	}
}

func TestEvictionKeepsTheCacheBounded(t *testing.T) {
	s := New()
	// Ask for more pixel data than the cache holds; it must not grow
	// without bound and must keep answering correctly.
	for i := 1; i <= 400; i++ {
		size := 1 + i%400
		if got := s.At(Downloads, size); got == nil {
			t.Fatalf("At failed at iteration %d", i)
		}
		if s.bytes > maxScaledBytes {
			t.Fatalf("cache holds %d bytes, over the %d limit", s.bytes, maxScaledBytes)
		}
	}
	// The accounting must match what is actually held, or the bound is
	// measuring something other than the memory in use.
	total := 0
	for _, img := range s.scaled {
		total += len(img.Pix)
	}
	if total != s.bytes {
		t.Errorf("cache accounts for %d bytes but holds %d", s.bytes, total)
	}
}

func anyOpaque(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			return true
		}
	}
	return false
}

// centre samples the middle of a tile, which is where the tint shows
// without any letter drawn over it.
func centre(img *image.RGBA) [4]uint8 {
	b := img.Bounds()
	o := img.PixOffset(b.Dx()/2, b.Dy()/2)
	return [4]uint8{img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]}
}

// BenchmarkScaling is why the cache exists: producing a size costs
// hundreds of microseconds and the dock asks for five of them per frame,
// so the difference between the miss and the hit is the difference between
// a frame that fits in its budget and one that does not.
func BenchmarkScaling(b *testing.B) {
	const size = 120
	b.Run("miss", func(b *testing.B) {
		s := New()
		if s.Master(Downloads) == nil {
			b.Skip("no embedded icon to scale")
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// Forget what was scaled but keep the decoded master, so this
			// measures the scaling and not the PNG.
			s.scaled, s.bytes = make(map[key]*image.RGBA), 0
			if s.Magnified(Downloads, size) == nil {
				b.Fatal("no icon")
			}
		}
	})
	b.Run("hit", func(b *testing.B) {
		s := New()
		if s.Magnified(Downloads, size) == nil {
			b.Fatal("no icon")
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			s.Magnified(Downloads, size)
		}
	})
}
