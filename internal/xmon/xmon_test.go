package xmon

import "testing"

// The two-monitor layout of the machine this package was written for: an
// ultrawide at the origin and a 4K to the right of it.
var twoHeads = []Rect{
	{X: 0, Y: 0, W: 3440, H: 1440, Name: "DP-8"},
	{X: 3440, Y: 0, W: 3840, H: 2160, Name: "DP-12"},
}

func TestNamed(t *testing.T) {
	tests := []struct {
		name string
		want int // index into twoHeads, or -1 for no match
	}{
		{"DP-8", 0},
		{"DP-12", 1},
		{"dp-8", -1},   // connector names are exact; a fuzzy match here
		{"DP-1", -1},   // would open the launcher on the wrong monitor
		{"", -1},       // the caller does not know where the user is
		{"HDMI-1", -1}, // unplugged since the caller asked
	}
	for _, tc := range tests {
		got, ok := Named(twoHeads, tc.name)
		if tc.want < 0 {
			if ok {
				t.Errorf("Named(%q) = %+v, want no match", tc.name, got)
			}
			continue
		}
		if !ok {
			t.Errorf("Named(%q) found nothing, want %+v", tc.name, twoHeads[tc.want])
			continue
		}
		if got != twoHeads[tc.want] {
			t.Errorf("Named(%q) = %+v, want %+v", tc.name, got, twoHeads[tc.want])
		}
	}
	if _, ok := Named(nil, "DP-8"); ok {
		t.Error("Named matched against no monitors at all")
	}
}

func TestContaining(t *testing.T) {
	tests := []struct {
		x, y int
		want string
	}{
		{0, 0, "DP-8"},
		{3439, 1439, "DP-8"},
		{3440, 0, "DP-12"}, // the seam belongs to the monitor it starts
		{7279, 2159, "DP-12"},
		{7280, 0, ""}, // past the right edge of the union
		{0, 1500, ""}, // below the shorter monitor, beside the taller one
		{-1, 0, ""},
	}
	for _, tc := range tests {
		got, ok := Containing(twoHeads, tc.x, tc.y)
		if got.Name != tc.want || ok != (tc.want != "") {
			t.Errorf("Containing(%d,%d) = %q,%v, want %q", tc.x, tc.y, got.Name, ok, tc.want)
		}
	}
}

func TestBottom(t *testing.T) {
	// The dock hides itself at Bottom, which on the shorter monitor is not
	// the bottom of the X screen. Getting this wrong made the dock
	// unsummonable there, which is why it is a named method and not a sum
	// at each call site.
	if got := twoHeads[0].Bottom(); got != 1440 {
		t.Errorf("Bottom() = %d, want 1440", got)
	}
}
