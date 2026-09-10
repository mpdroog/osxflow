package search

import (
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
)

func apps(names ...string) []desktop.App {
	out := make([]desktop.App, 0, len(names))
	for _, n := range names {
		out = append(out, desktop.App{Name: n, Argv: []string{strings.ToLower(n)}})
	}
	return out
}

func names(rs []Result) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.App.Name)
	}
	return out
}

func TestFilterEmptyQueryKeepsEverythingInOrder(t *testing.T) {
	in := apps("Zebra", "Apple", "Monkey")
	got := names(Filter(in, "", nil))
	want := []string{"Zebra", "Apple", "Monkey"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Filter(_, \"\") = %v, want %v unchanged", got, want)
	}
	if len(Filter(in, "   ", nil)) != 3 {
		t.Error("a whitespace-only query should behave like an empty one")
	}
}

func TestFilterTierOrdering(t *testing.T) {
	// One app per tier, deliberately listed worst-first so that a scorer
	// that ignored tiers would fail.
	in := []desktop.App{
		{Name: "Fallback Zone", Argv: []string{"fz"}},      // subsequence: f..z
		{Name: "Zebra Zoo", Argv: []string{"zz"}},          // no match at all
		{Name: "Configure Firewall", Argv: []string{"cf"}}, // word prefix "fi"
		{Name: "Notifications", Argv: []string{"nt"}},      // substring "fi"
		{Name: "Files", Argv: []string{"files"}},           // prefix "fi"
		{Name: "Fi", Argv: []string{"fi"}},                 // exact
	}
	got := names(Filter(in, "fi", nil))
	if len(got) < 4 {
		t.Fatalf("Filter matched only %v", got)
	}
	want := []string{"Fi", "Files", "Configure Firewall", "Notifications"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("Filter(_, \"fi\") = %v, want it to start %v", got, want)
		}
	}
}

// The motivating case from the package comment: a scattered subsequence
// match must never outrank a real prefix match.
func TestFilterPrefixBeatsSubsequence(t *testing.T) {
	in := apps("LibreOffice Calc", "Files")
	got := names(Filter(in, "fi", nil))
	if got[0] != "Files" {
		t.Errorf("Filter = %v, want Files first", got)
	}
}

func TestFilterShorterNameWinsWithinTier(t *testing.T) {
	in := apps("File Manager Settings", "Files", "File Roller")
	got := names(Filter(in, "file", nil))
	if got[0] != "Files" {
		t.Errorf("Filter = %v, want the shortest prefix match first", got)
	}
}

func TestFilterWordPrefix(t *testing.T) {
	in := apps("GNOME Calculator", "Cheese", "Archive Manager")
	got := names(Filter(in, "calc", nil))
	if len(got) == 0 || got[0] != "GNOME Calculator" {
		t.Errorf("Filter = %v, want GNOME Calculator first", got)
	}
}

func TestFilterWordPrefixSplitsOnPunctuation(t *testing.T) {
	// Names are full of non-space separators; splitting only on spaces
	// would miss the second half of these.
	for _, name := range []string{"Disks/Volumes", "Text-Editor", "Sound&Video"} {
		in := []desktop.App{{Name: name, Argv: []string{"x"}}}
		q := strings.ToLower(strings.FieldsFunc(name, func(r rune) bool {
			return r == '/' || r == '-' || r == '&'
		})[1][:3])
		got := Filter(in, q, nil)
		if len(got) == 0 {
			t.Errorf("Filter(%q, %q) found nothing", name, q)
			continue
		}
		if got[0].Tier != WordPrefix {
			t.Errorf("Filter(%q, %q) tier = %v, want WordPrefix", name, q, got[0].Tier)
		}
	}
}

func TestFilterByExecutableName(t *testing.T) {
	in := []desktop.App{
		{Name: "File Manager", Binary: "/usr/bin/thunar", Argv: []string{"thunar"}},
		{Name: "Something Else", Binary: "/usr/bin/other", Argv: []string{"other"}},
	}
	got := Filter(in, "thunar", nil)
	if len(got) == 0 {
		t.Fatal("Filter found nothing for an executable name")
	}
	if got[0].App.Name != "File Manager" {
		t.Errorf("Filter = %v, want File Manager", names(got))
	}
	if got[0].Tier != ExecName {
		t.Errorf("tier = %v, want ExecName", got[0].Tier)
	}
}

// The visible name must win over another app's executable name, because
// the user is usually typing what they can see.
func TestFilterNameBeatsExecutable(t *testing.T) {
	in := []desktop.App{
		{Name: "Other Thing", Binary: "/usr/bin/thunar", Argv: []string{"thunar"}},
		{Name: "Thunar File Manager", Binary: "/usr/bin/tfm", Argv: []string{"tfm"}},
	}
	got := Filter(in, "thunar", nil)
	if got[0].App.Name != "Thunar File Manager" {
		t.Errorf("Filter = %v, want the app actually named Thunar first", names(got))
	}
}

func TestFilterSubsequence(t *testing.T) {
	in := apps("Firefox Web Browser", "Nothing Relevant")
	got := Filter(in, "fwb", nil)
	if len(got) == 0 {
		t.Fatal("subsequence match found nothing")
	}
	if got[0].App.Name != "Firefox Web Browser" {
		t.Errorf("Filter = %v, want Firefox Web Browser", names(got))
	}
	if got[0].Tier != Subsequence {
		t.Errorf("tier = %v, want Subsequence", got[0].Tier)
	}
}

func TestFilterSubsequencePrefersFewerGaps(t *testing.T) {
	in := apps("Fallback Font Fixer Utility", "Fofo")
	got := names(Filter(in, "fof", nil))
	if got[0] != "Fofo" {
		t.Errorf("Filter = %v, want the tighter match first", got)
	}
}

func TestFilterNoMatch(t *testing.T) {
	in := apps("Files", "Firefox")
	if got := Filter(in, "zzzz", nil); len(got) != 0 {
		t.Errorf("Filter = %v, want nothing", names(got))
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	in := apps("Firefox Web Browser")
	for _, q := range []string{"FIREFOX", "firefox", "FireFox", "  firefox  "} {
		if got := Filter(in, q, nil); len(got) != 1 {
			t.Errorf("Filter(_, %q) = %v, want one match", q, names(got))
		}
	}
}

func TestFilterEmptyList(t *testing.T) {
	if got := Filter(nil, "anything", nil); len(got) != 0 {
		t.Errorf("Filter(nil, _, nil) = %v, want nothing", names(got))
	}
	if got := Filter(nil, "", nil); len(got) != 0 {
		t.Errorf("Filter(nil, \"\") = %v, want nothing", names(got))
	}
}

// Results must point at the caller's slice, not at copies: the UI uses
// the pointer to launch the app.
func TestFilterResultsPointAtInput(t *testing.T) {
	in := apps("Files", "Firefox")
	got := Filter(in, "fi", nil)
	for _, r := range got {
		if r.App != &in[0] && r.App != &in[1] {
			t.Error("Result.App does not point into the input slice")
		}
	}
}

func TestSubsequence(t *testing.T) {
	tests := []struct {
		name, q  string
		wantOK   bool
		wantGaps int
	}{
		{"firefox", "ffx", true, 4},
		{"firefox", "fir", true, 0},
		{"firefox", "xof", false, 0},
		{"firefox", "firefoxx", false, 0},
		{"abc", "", false, 0},
		{"", "a", false, 0},
		{"aaa", "aa", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+tc.q, func(t *testing.T) {
			gaps, ok := subsequence(tc.name, tc.q)
			if ok != tc.wantOK {
				t.Fatalf("subsequence(%q, %q) ok = %v, want %v", tc.name, tc.q, ok, tc.wantOK)
			}
			if ok && gaps != tc.wantGaps {
				t.Errorf("gaps = %d, want %d", gaps, tc.wantGaps)
			}
		})
	}
}

func TestTierStrings(t *testing.T) {
	for _, tier := range []Tier{NoMatch, Subsequence, Substring, ExecName, WordPrefix, Prefix, Exact} {
		if tier.String() == "unknown" {
			t.Errorf("Tier(%d).String() = unknown", tier)
		}
	}
}

func TestTierOrdering(t *testing.T) {
	ordered := []Tier{NoMatch, Subsequence, Substring, ExecName, WordPrefix, Prefix, Exact}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1] >= ordered[i] {
			t.Fatalf("Tier %v is not below %v", ordered[i-1], ordered[i])
		}
	}
}

// FuzzFilter checks that ranking never panics and never invents results,
// since the query comes straight from a live text field.
func FuzzFilter(f *testing.F) {
	for _, s := range []string{"", "fi", "firefox", "ZZZ", "  ", "é", "\x00", strings.Repeat("a", 100)} {
		f.Add(s)
	}
	in := apps("Files", "Firefox Web Browser", "GNOME Calculator", "Thunar File Manager")
	f.Fuzz(func(t *testing.T, q string) {
		if len(q) > 128 {
			t.Skip()
		}
		got := Filter(in, q, nil)
		if len(got) > len(in) {
			t.Fatalf("Filter returned %d results for %d apps", len(got), len(in))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].Tier < got[i].Tier {
				t.Fatalf("results are not sorted by tier: %v", got)
			}
		}
	})
}

// fakeRanker is a Ranker with fixed answers, so ordering can be tested
// without a clock or a file.
type fakeRanker struct {
	scores map[string]float64
	pins   map[string]string
}

func (f fakeRanker) Score(appID string) float64 { return f.scores[appID] }
func (f fakeRanker) Pinned(query string) string { return f.pins[query] }

func withIDs(names ...string) []desktop.App {
	out := make([]desktop.App, 0, len(names))
	for _, n := range names {
		out = append(out, desktop.App{
			Name: n,
			ID:   strings.ToLower(n) + ".desktop",
			Argv: []string{strings.ToLower(n)},
		})
	}
	return out
}

// The motivating case: fish, Fingerprints and Firefox all match "fi" as
// prefixes, so they sit in one tier and are ordered only by name length.
// Usage replaces that arbitrary tiebreak.
func TestFilterUsageOrdersWithinATier(t *testing.T) {
	in := withIDs("fish", "Fingerprints", "Firefox")

	before := names(Filter(in, "fi", nil))
	if before[0] != "fish" {
		t.Fatalf("fixture: without usage the order is %v, expected fish first", before)
	}

	r := fakeRanker{scores: map[string]float64{"firefox.desktop": 3}}
	after := names(Filter(in, "fi", r))
	if after[0] != "Firefox" {
		t.Errorf("Filter = %v, want Firefox first once it has been used", after)
	}
	// Everything still matches; usage reorders rather than filters.
	if len(after) != 3 {
		t.Errorf("Filter returned %d results, want 3", len(after))
	}
}

// Usage must not cross a tier. An application used hourly should not
// displace an exact-name match for something else, or typing a full name
// would stop working.
func TestFilterUsageCannotBeatABetterTier(t *testing.T) {
	in := withIDs("Files", "Fi")
	r := fakeRanker{scores: map[string]float64{"files.desktop": 1000}}

	got := names(Filter(in, "fi", r))
	if got[0] != "Fi" {
		t.Errorf("Filter = %v, want the exact match first despite heavy use of Files", got)
	}
}

func TestFilterUsageOrdersTheEmptyQuery(t *testing.T) {
	in := withIDs("Alpha", "Beta", "Gamma")
	r := fakeRanker{scores: map[string]float64{"gamma.desktop": 5, "beta.desktop": 2}}

	got := names(Filter(in, "", r))
	want := []string{"Gamma", "Beta", "Alpha"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Filter(_, \"\") = %v, want most-used first: %v", got, want)
		}
	}
}

// A pin is the one thing allowed to beat a better match, because the user
// taught it deliberately by choosing that app for that exact query.
func TestFilterPinnedWinsAcrossTiers(t *testing.T) {
	// "ff": LibreOffice matches as a substring, Firefox only as a
	// scattered subsequence, so scoring alone can never lift Firefox.
	in := withIDs("LibreOffice", "Firefox")

	before := names(Filter(in, "ff", nil))
	if before[0] != "LibreOffice" {
		t.Fatalf("fixture: expected LibreOffice first unpinned, got %v", before)
	}

	r := fakeRanker{pins: map[string]string{"ff": "firefox.desktop"}}
	after := Filter(in, "ff", r)
	if after[0].App.Name != "Firefox" {
		t.Errorf("Filter = %v, want the pinned Firefox first", names(after))
	}
	if !after[0].Pinned {
		t.Error("the winning result is not marked Pinned")
	}
	if after[1].Pinned {
		t.Error("a non-pinned result is marked Pinned")
	}
}

// A pin only applies to the query it was taught for.
func TestFilterPinnedIsPerQuery(t *testing.T) {
	in := withIDs("LibreOffice", "Firefox")
	r := fakeRanker{pins: map[string]string{"ff": "firefox.desktop"}}

	got := names(Filter(in, "libre", r))
	if got[0] != "LibreOffice" {
		t.Errorf("Filter = %v, want the pin not to apply to another query", got)
	}
}

// A pin for an app that does not match the query at all must not conjure
// it into the results.
func TestFilterPinnedAppThatDoesNotMatch(t *testing.T) {
	in := withIDs("LibreOffice", "Firefox")
	r := fakeRanker{pins: map[string]string{"zzz": "firefox.desktop"}}

	got := Filter(in, "zzz", r)
	if len(got) != 0 {
		t.Errorf("Filter = %v, want nothing: the pinned app does not match", names(got))
	}
}

func TestFilterExposesUsageOnResults(t *testing.T) {
	in := withIDs("Firefox")
	r := fakeRanker{scores: map[string]float64{"firefox.desktop": 2.5}}
	got := Filter(in, "fire", r)
	if len(got) != 1 {
		t.Fatalf("Filter returned %d results, want 1", len(got))
	}
	if got[0].Usage != 2.5 {
		t.Errorf("Usage = %v, want 2.5", got[0].Usage)
	}
}

// A nil Ranker must behave exactly as before frecency existed.
func TestFilterNilRankerMatchesOldBehaviour(t *testing.T) {
	in := withIDs("fish", "Fingerprints", "Firefox")
	got := Filter(in, "fi", nil)
	for _, r := range got {
		if r.Usage != 0 || r.Pinned {
			t.Errorf("%s has Usage=%v Pinned=%v with a nil ranker", r.App.Name, r.Usage, r.Pinned)
		}
	}
}
