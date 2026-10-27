package main

import (
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/wl"
)

// Every layout fills the monitor exactly: no cell overlaps another, none
// is empty, and together they cover every percent of it.
func TestGridTilesWithoutOverlapOrGaps(t *testing.T) {
	for n := 1; n <= maxTiled; n++ {
		cells := grid(n)
		if len(cells) != n {
			t.Fatalf("grid(%d) has %d cells", n, len(cells))
		}
		var cover [100][100]int
		for _, c := range cells {
			if c.w <= 0 || c.h <= 0 || c.x < 0 || c.y < 0 || c.x+c.w > 100 || c.y+c.h > 100 {
				t.Fatalf("grid(%d): cell %+v is empty or off the monitor", n, c)
			}
			for x := c.x; x < c.x+c.w; x++ {
				for y := c.y; y < c.y+c.h; y++ {
					cover[x][y]++
				}
			}
		}
		for x := range cover {
			for y := range cover[x] {
				if cover[x][y] != 1 {
					t.Fatalf("grid(%d): %d%%,%d%% is covered %d times", n, x, y, cover[x][y])
				}
			}
		}
	}
}

func TestGridShapes(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []cell
	}{
		{1, []cell{{0, 0, 100, 100}}},
		{2, []cell{{0, 0, 50, 100}, {50, 0, 50, 100}}},
		{3, []cell{{0, 0, 33, 100}, {33, 0, 34, 100}, {67, 0, 33, 100}}},
		// The oldest window gets the column to itself.
		{5, []cell{{0, 0, 33, 100}, {33, 0, 34, 50}, {33, 50, 34, 50}, {67, 0, 33, 50}, {67, 50, 33, 50}}},
	} {
		got := grid(tc.n)
		if len(got) != len(tc.want) {
			t.Fatalf("grid(%d) = %v", tc.n, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("grid(%d)[%d] = %+v, want %+v", tc.n, i, got[i], tc.want[i])
			}
		}
	}
}

// Each region has a chord of its own, inside the keys the keymap has.
func TestBindingsAreDistinct(t *testing.T) {
	seen := map[[2]uint32]string{}
	regions := map[string]bool{}
	for n, bs := range bindings() {
		if len(bs) != n {
			t.Fatalf("%d bindings for %d windows", len(bs), n)
		}
		for _, b := range bs {
			if b.key < firstKey || b.key >= firstKey+keyCount || modRC(b.mods) == "" {
				t.Errorf("%s: F%d with mods %#x has no key", b.region, b.key, b.mods)
			}
			k := [2]uint32{b.mods, uint32(b.key)}
			if other, dup := seen[k]; dup {
				t.Errorf("%s and %s share a chord", b.region, other)
			}
			seen[k] = b.region
			if regions[b.region] {
				t.Errorf("region %s twice", b.region)
			}
			regions[b.region] = true
		}
	}
	if !strings.Contains(keymap(), "key <F35> { [ F35 ] };") {
		t.Error("the keymap stops short of F35")
	}
}

func TestPlan(t *testing.T) {
	ts := []wl.Toplevel{
		{ID: 5, Outputs: []uint32{1}},
		{ID: 2, Outputs: []uint32{1}},
		{ID: 3, Outputs: []uint32{2}},
		{ID: 4, Outputs: []uint32{1}, Minimized: true},
		{ID: 6, Outputs: []uint32{1}, Fullscreen: true},
		{ID: 7, Outputs: []uint32{1}, Parent: 2},
		{ID: 8},
		{ID: 9, Outputs: []uint32{2, 1}},
	}
	p := plan(ts)
	if got := signature(p); got != "1:[2 5];2:[3 9];" {
		t.Errorf("plan = %s", got)
	}
	// Focus and titles are not in it; the set of windows is.
	ts[0].Activated, ts[0].Title = true, "retitled"
	if signature(plan(ts)) != signature(p) {
		t.Error("focusing a window changed the plan")
	}
	ts[3].Minimized = false
	if signature(plan(ts)) == signature(p) {
		t.Error("restoring a window left the plan as it was")
	}
}

func TestSplice(t *testing.T) {
	rc := "<labwc_config>\n  <core/>\n</labwc_config>\n"
	once, err := splice(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(once, "<labwc_config>\n  <core/>\n  "+blockBegin) || !strings.HasSuffix(once, blockEnd+"\n\n</labwc_config>\n") {
		t.Fatalf("first splice:\n%s", once)
	}
	twice, err := splice(once)
	if err != nil {
		t.Fatal(err)
	}
	if twice != once {
		t.Error("splicing an up-to-date config changed it")
	}
	stale := strings.Replace(once, "osxflow-tile-1-1", "old", 1)
	if fixed, err := splice(stale); err != nil || fixed != once {
		t.Errorf("a stale block was not replaced: %v", err)
	}
	if _, err := splice("<labwc_config>\n  " + blockBegin + "\n</labwc_config>\n"); err == nil {
		t.Error("a block with no end was accepted")
	}
	if _, err := splice("<openbox_config/>"); err == nil {
		t.Error("a config with no </labwc_config> was accepted")
	}
}
