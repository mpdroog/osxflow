package main

// Where windows go, kept free of Wayland so it is tested without one.
//
// labwc cannot be told "put that window at x,y". What it can do is snap
// the focused window into a named region, given in percent of the
// monitor's usable area -- below the menubar's strip -- and bound to a
// key. So the layouts are fixed in advance: for n windows on a monitor
// there are n regions, and every region has a chord of its own. tile
// focuses each window in turn and presses its region's chord.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/mpdroog/osxflow/wl"
)

// maxTiled is the most windows tiled on one monitor. Past nine the cells
// are too small to work in; a tenth window is left where it is.
const maxTiled = 9

// cell is a region in percent of the monitor's usable area.
type cell struct{ x, y, w, h int }

// grid lays out n windows, oldest first, without overlap or gaps.
//
// Up to three side by side, then two rows, then three. When the windows do
// not fill the grid, the leftmost columns get one window fewer -- and so
// taller windows -- which hands the extra room to the oldest ones.
func grid(n int) []cell {
	if n <= 0 {
		return nil
	}
	rows := 1
	switch {
	case n > 8:
		rows = 3
	case n > 3:
		rows = 2
	}
	cols := (n + rows - 1) / rows
	short := cols*rows - n // columns with one window fewer

	cells := make([]cell, 0, n)
	for c := range cols {
		x0, x1 := split(c, cols), split(c+1, cols)
		inCol := rows
		if c < short {
			inCol--
		}
		for r := range inCol {
			y0, y1 := split(r, inCol), split(r+1, inCol)
			cells = append(cells, cell{x0, y0, x1 - x0, y1 - y0})
		}
	}
	return cells
}

// split is the i-th of n boundaries across 100%. Neighbouring cells share
// a boundary, so whole percents still meet exactly.
func split(i, n int) int { return (i*200 + n) / (2 * n) }

// binding is the chord that snaps the focused window into the i-th of n
// cells.
type binding struct {
	region string
	mods   uint32
	key    int // F13 is 13
}

// Chords. No real keyboard has every one of these modifiers held with an
// F-key above F12 -- the Mac's tops out at F19 -- so a binding fires only
// when tile presses it.
const (
	firstKey = 13
	keyCount = 23 // F13 to F35, every F-key xkb has a name for past F12
)

var modSets = []struct {
	mask uint32
	rc   string
}{
	{wl.ModSuper | wl.ModCtrl | wl.ModAlt | wl.ModShift, "W-C-A-S-"},
	{wl.ModSuper | wl.ModCtrl | wl.ModShift, "W-C-S-"},
}

// bindings is every region tile can snap to, for 1 to maxTiled windows.
func bindings() [][]binding {
	all := make([][]binding, maxTiled+1)
	j := 0
	for n := 1; n <= maxTiled; n++ {
		for i := range n {
			all[n] = append(all[n], binding{
				region: fmt.Sprintf("osxflow-tile-%d-%d", n, i+1),
				mods:   modSets[j/keyCount].mask,
				key:    firstKey + j%keyCount,
			})
			j++
		}
	}
	return all
}

// keycode is the xkb keycode the virtual keyboard's own keymap gives Fk.
// Any free range would do: the keymap is tile's and nobody else's.
func keycode(k int) int { return 200 + k - firstKey }

// keymap is the virtual keyboard's: F13 to F35 and nothing else.
// Modifiers need no keys -- the keyboard sets them directly -- and the
// standard types and compat give them their usual names.
func keymap() string {
	var b strings.Builder
	b.WriteString("xkb_keymap {\nxkb_keycodes \"osxflow-tile\" {\n\tminimum = 8;\n\tmaximum = 255;\n")
	for k := firstKey; k < firstKey+keyCount; k++ {
		fmt.Fprintf(&b, "\t<F%d> = %d;\n", k, keycode(k))
	}
	b.WriteString("};\nxkb_types \"osxflow-tile\" { include \"complete\" };\n")
	b.WriteString("xkb_compat \"osxflow-tile\" { include \"complete\" };\n")
	b.WriteString("xkb_symbols \"osxflow-tile\" {\n")
	for k := firstKey; k < firstKey+keyCount; k++ {
		fmt.Fprintf(&b, "\tkey <F%d> { [ F%d ] };\n", k, k)
	}
	b.WriteString("};\n};\n")
	return b.String()
}

// plan is which windows go on each monitor, by wl_output id, oldest
// window first.
//
// Left out: minimised windows, which are out of the way by choice;
// fullscreen ones, a video or a game; dialogs, which belong over their
// window; and windows on no monitor yet, which have not been mapped.
// Oldest is by handle id, which the compositor hands out in the order the
// windows opened -- not by focus, which would reshuffle the screen on
// every click.
func plan(ts []wl.Toplevel) map[uint32][]uint32 {
	ts = slices.Clone(ts)
	slices.SortFunc(ts, func(a, b wl.Toplevel) int { return cmp.Compare(a.ID, b.ID) })
	out := map[uint32][]uint32{}
	for i := range ts {
		t := &ts[i]
		if t.Minimized || t.Fullscreen || t.Parent != 0 || len(t.Outputs) == 0 {
			continue
		}
		out[t.Outputs[0]] = append(out[t.Outputs[0]], t.ID)
	}
	return out
}

// signature says whether a plan differs from the last one tiled. A window
// retitling itself or being focused leaves it alone; one opening, closing
// or moving to another monitor does not.
func signature(p map[uint32][]uint32) string {
	keys := make([]uint32, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%d:%v;", k, p[k])
	}
	return b.String()
}
