package dock

import (
	"math"
	"testing"
)

// testMetrics is the real dock's geometry at scale 1, so the numbers in
// these tests are the ones the dock actually uses.
func testMetrics() *Metrics {
	return &Metrics{
		Scale:      1,
		Icon:       48,
		MaxZoom:    1.55,
		Influence:  130,
		Gap:        8,
		Padding:    14,
		SeparatorW: 15,
		DotRadius:  2.3,
		DotGap:     6,
		Radius:     17,
	}
}

func apps(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Kind: KindApp, Name: "app", Icon: "x"}
	}
	return items
}

// dockRow is the shape the real dock builds: applications, a separator,
// then the two stacks.
func dockRow(nApps int) []Item {
	return append(apps(nApps),
		Item{Kind: KindSeparator},
		Item{Kind: KindStack, Name: "Downloads"},
		Item{Kind: KindStack, Name: "Trash"},
	)
}

func TestLayoutFlatWhenNotMagnified(t *testing.T) {
	m := testMetrics()
	items := dockRow(5)
	// zoom 0 is the resting state: the cursor's position must not matter.
	for _, cursor := range []float64{-1000, 0, 500, 1e6} {
		places := Layout(items, m, 500, cursor, 0)
		for i, p := range places {
			if p.Size != m.Icon {
				t.Errorf("cursor %g item %d: size %g, want the resting %g", cursor, i, p.Size, m.Icon)
			}
		}
	}
}

func TestLayoutCursorFarAwayLeavesEverythingAlone(t *testing.T) {
	m := testMetrics()
	items := dockRow(5)
	near := Layout(items, m, 500, math.Inf(1), 1)
	far := Layout(items, m, 500, 500+10*m.Influence, 1)
	for i := range near {
		if near[i].Size != m.Icon || far[i].Size != m.Icon {
			t.Fatalf("item %d magnified with the cursor outside the influence radius", i)
		}
		if near[i].CentreX != far[i].CentreX {
			t.Errorf("item %d moved (%g vs %g) with no magnification in play",
				i, near[i].CentreX, far[i].CentreX)
		}
	}
}

func TestLayoutHoveredItemIsLargest(t *testing.T) {
	m := testMetrics()
	items := apps(7)
	flat := Layout(items, m, 500, math.Inf(1), 0)

	for target := range items {
		places := Layout(items, m, 500, flat[target].CentreX, 1)
		for i, p := range places {
			if i == target {
				continue
			}
			if p.Size > places[target].Size {
				t.Errorf("cursor on item %d but item %d is bigger (%g > %g)",
					target, i, p.Size, places[target].Size)
			}
		}
		want := m.Icon * m.MaxZoom
		if math.Abs(places[target].Size-want) > 0.5 {
			t.Errorf("item %d under the cursor is %g, want the full %g", target, places[target].Size, want)
		}
	}
}

func TestLayoutSizeFallsOffWithDistance(t *testing.T) {
	m := testMetrics()
	items := apps(9)
	flat := Layout(items, m, 500, math.Inf(1), 0)
	const target = 4
	places := Layout(items, m, 500, flat[target].CentreX, 1)

	for i := target; i > 0; i-- {
		if places[i-1].Size > places[i].Size+1e-9 {
			t.Errorf("size grows away from the cursor: item %d (%g) > item %d (%g)",
				i-1, places[i-1].Size, i, places[i].Size)
		}
	}
	for i := target; i < len(places)-1; i++ {
		if places[i+1].Size > places[i].Size+1e-9 {
			t.Errorf("size grows away from the cursor: item %d (%g) > item %d (%g)",
				i+1, places[i+1].Size, i, places[i].Size)
		}
	}
}

func TestLayoutSeparatorNeverMagnifies(t *testing.T) {
	m := testMetrics()
	items := dockRow(4)
	sep := 4
	if items[sep].Kind != KindSeparator {
		t.Fatalf("test set-up: item %d is not the separator", sep)
	}
	flat := Layout(items, m, 500, math.Inf(1), 0)
	places := Layout(items, m, 500, flat[sep].CentreX, 1)
	if places[sep].Size != m.Icon {
		t.Errorf("separator scaled to %g; it should never magnify", places[sep].Size)
	}
}

func TestLayoutIsCentred(t *testing.T) {
	m := testMetrics()
	items := dockRow(5)
	const centre float64 = 640
	for _, zoom := range []float64{0, 0.5, 1} {
		places := Layout(items, m, centre, centre, zoom)
		width := Width(items, places, m)
		left := places[0].CentreX - places[0].Size/2 - m.Padding
		got := left + width/2
		if math.Abs(got-centre) > 0.5 {
			t.Errorf("zoom %g: row centred at %g, want %g", zoom, got, centre)
		}
	}
}

// TestLayoutNeverOverlaps is the invariant that matters most: two icons
// sharing pixels is the failure mode a magnifying layout has, and it is
// not visible in a screenshot until it is badly wrong.
func TestLayoutNeverOverlaps(t *testing.T) {
	m := testMetrics()
	items := dockRow(6)
	for cursor := -200.0; cursor <= 1200; cursor += 7 {
		for _, zoom := range []float64{0, 0.25, 0.5, 0.75, 1} {
			places := Layout(items, m, 500, cursor, zoom)
			for i := 1; i < len(places); i++ {
				prevRight := places[i-1].CentreX + slotWidth(&items[i-1], m, places[i-1].Size)/2
				thisLeft := places[i].CentreX - slotWidth(&items[i], m, places[i].Size)/2
				if thisLeft < prevRight-1e-9 {
					t.Fatalf("cursor %g zoom %g: item %d starts at %g, inside item %d ending at %g",
						cursor, zoom, i, thisLeft, i-1, prevRight)
				}
			}
		}
	}
}

func TestHitFindsTheIconUnderThePointer(t *testing.T) {
	m := testMetrics()
	items := dockRow(4)
	const baseline = 100
	places := Layout(items, m, 500, math.Inf(1), 0)

	for i := range items {
		if items[i].Kind == KindSeparator {
			continue
		}
		x := places[i].CentreX
		y := baseline - places[i].Size/2
		if got := Hit(items, places, baseline, x, y); got != i {
			t.Errorf("point on item %d hit %d", i, got)
		}
	}
}

func TestHitMissesGapsAndSeparators(t *testing.T) {
	m := testMetrics()
	items := dockRow(4)
	const baseline = 100
	places := Layout(items, m, 500, math.Inf(1), 0)

	// The gap between two icons belongs to neither of them: a click that
	// lands there should do nothing rather than launch a neighbour.
	between := (places[0].CentreX + places[0].Size/2 + places[1].CentreX - places[1].Size/2) / 2
	if got := Hit(items, places, baseline, between, baseline-1); got != -1 {
		t.Errorf("a click in the gap hit item %d", got)
	}

	sep := 4
	if got := Hit(items, places, baseline, places[sep].CentreX, baseline-1); got == sep {
		t.Error("the separator was hit; it is not clickable")
	}

	// Above the tallest icon and below the baseline are both outside.
	if got := Hit(items, places, baseline, places[0].CentreX, baseline-places[0].Size-1); got != -1 {
		t.Errorf("a point above the icon hit item %d", got)
	}
	if got := Hit(items, places, baseline, places[0].CentreX, baseline+1); got != -1 {
		t.Errorf("a point below the baseline hit item %d", got)
	}
}

func TestMaxWidthCoversEveryCursorPosition(t *testing.T) {
	m := testMetrics()
	items := dockRow(6)
	maxW := MaxWidth(items, m)

	for cursor := -400.0; cursor <= 1400; cursor += 3 {
		places := Layout(items, m, 500, cursor, 1)
		if w := Width(items, places, m); w > maxW+1 {
			t.Fatalf("cursor %g gives width %g, more than MaxWidth %g -- the window would clip",
				cursor, w, maxW)
		}
	}
}

func TestLayoutEmpty(t *testing.T) {
	m := testMetrics()
	if got := Layout(nil, m, 0, 0, 1); got != nil {
		t.Errorf("Layout(nil) = %v, want nil", got)
	}
	if got := MaxWidth(nil, m); got != 0 {
		t.Errorf("MaxWidth(nil) = %g, want 0", got)
	}
}

func TestMagnificationIsSmoothAtTheEdge(t *testing.T) {
	m := testMetrics()
	// The raised cosine is chosen for a zero slope at both ends. Stepping
	// across the edge of the influence radius must not jump.
	prev := magnification(m.Influence*1.01, m, 1)
	for d := m.Influence; d >= 0; d -= m.Influence / 200 {
		got := magnification(d, m, 1)
		if got < prev-1e-9 {
			t.Fatalf("magnification decreased approaching the cursor at d=%g", d)
		}
		if got-prev > 0.02 {
			t.Fatalf("magnification jumped by %g at d=%g; the curve should be smooth", got-prev, d)
		}
		prev = got
	}
}

// FuzzLayout checks the invariants against arbitrary item counts, cursor
// positions and zoom levels. Layout is fed a cursor position that comes
// straight from the X server and an item count that depends on what the
// user has open, so neither is really under this program's control.
func FuzzLayout(f *testing.F) {
	f.Add(5, 500.0, 1.0)
	f.Add(0, 0.0, 0.0)
	f.Add(40, -1e6, 0.5)

	f.Fuzz(func(t *testing.T, n int, cursor, zoom float64) {
		if n < 0 || n > 64 || math.IsNaN(cursor) || math.IsNaN(zoom) {
			t.Skip()
		}
		if math.IsInf(cursor, 0) && math.Signbit(cursor) {
			t.Skip()
		}
		m := testMetrics()
		items := dockRow(n)
		places := Layout(items, m, 500, cursor, zoom)
		if len(places) != len(items) {
			t.Fatalf("got %d placements for %d items", len(places), len(items))
		}

		for i, p := range places {
			if math.IsNaN(p.Size) || math.IsNaN(p.CentreX) {
				t.Fatalf("item %d has a NaN placement: %+v", i, p)
			}
			if p.Size < m.Icon-1e-9 || p.Size > m.Icon*m.MaxZoom+1e-9 {
				t.Fatalf("item %d size %g outside [%g, %g]", i, p.Size, m.Icon, m.Icon*m.MaxZoom)
			}
			if i > 0 {
				prevRight := places[i-1].CentreX + slotWidth(&items[i-1], m, places[i-1].Size)/2
				thisLeft := p.CentreX - slotWidth(&items[i], m, p.Size)/2
				if thisLeft < prevRight-1e-6 {
					t.Fatalf("item %d overlaps item %d", i, i-1)
				}
			}
		}
	})
}

// TestQuantise is the property the icon cache depends on: a magnification
// sweep must land on a small, repeating set of sizes, and an unmagnified
// icon must land exactly on its resting size.
func TestQuantise(t *testing.T) {
	for _, scale := range []float64{1, 1.5, 2, 3} {
		m := Metrics{Scale: scale, Icon: 48 * scale, MaxZoom: 1.55}
		icon := int(m.Icon + 0.5)

		if got := m.Quantise(m.Icon); got != icon {
			t.Errorf("scale %v: resting size quantised to %d, want %d", scale, got, icon)
		}

		step := int(scale + 0.5)
		if step < 1 {
			step = 1
		}
		seen := make(map[int]bool)
		prev := 0
		for s := m.Icon; s <= m.Icon*m.MaxZoom; s += 0.05 {
			q := m.Quantise(s)
			if q < icon {
				t.Fatalf("scale %v: size %v quantised below the resting size, to %d", scale, s, q)
			}
			if (q-icon)%step != 0 {
				t.Fatalf("scale %v: size %v quantised to %d, off the %d-pixel ladder", scale, s, q, step)
			}
			if q < prev {
				t.Fatalf("scale %v: the ladder went backwards, %d after %d", scale, q, prev)
			}
			prev = q
			seen[q] = true
		}
		// The whole point: far fewer distinct sizes than the sweep asked
		// for, and never more than the ladder can hold.
		rungs := int((m.Icon*m.MaxZoom-m.Icon)/float64(step)) + 2
		if len(seen) > rungs {
			t.Errorf("scale %v: %d distinct sizes over a ladder of %d", scale, len(seen), rungs)
		}
	}

	// A degenerate scale must still produce a drawable size rather than a
	// zero-sized image.
	m := Metrics{Scale: 0, Icon: 0}
	if got := m.Quantise(-5); got != 1 {
		t.Errorf("a negative size quantised to %d, want 1", got)
	}
}
