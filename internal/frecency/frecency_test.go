package frecency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func TestRecordThenScore(t *testing.T) {
	s := New("")
	if got := s.Score("firefox.desktop", t0); got != 0 {
		t.Errorf("an unrecorded app scored %v, want 0", got)
	}
	s.Record("fi", "firefox.desktop", t0)
	if got := s.Score("firefox.desktop", t0); got != 1 {
		t.Errorf("score after one use = %v, want 1", got)
	}
	s.Record("fi", "firefox.desktop", t0)
	if got := s.Score("firefox.desktop", t0); got != 2 {
		t.Errorf("score after two uses = %v, want 2", got)
	}
}

// The property the whole design rests on: one half-life halves the score.
func TestScoreDecaysByHalfEachHalfLife(t *testing.T) {
	s := New("")
	s.Record("q", "app", t0)

	tests := []struct {
		after time.Duration
		want  float64
	}{
		{0, 1},
		{HalfLife, 0.5},
		{2 * HalfLife, 0.25},
		{4 * HalfLife, 0.0625},
	}
	for _, tc := range tests {
		got := s.Score("app", t0.Add(tc.after))
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("score after %v = %v, want %v", tc.after, got, tc.want)
		}
	}
}

// Frequency and recency in one number: something used constantly a year
// ago must lose to something used twice this week.
func TestRecentBeatsHistoricallyFrequent(t *testing.T) {
	s := New("")
	old := t0.Add(-365 * 24 * time.Hour)
	for range 100 {
		s.Record("q", "old-favourite", old)
	}
	s.Record("q", "current", t0.Add(-24*time.Hour))
	s.Record("q", "current", t0)

	oldScore := s.Score("old-favourite", t0)
	newScore := s.Score("current", t0)
	if newScore <= oldScore {
		t.Errorf("current scored %v and the old favourite %v; recent use must win", newScore, oldScore)
	}
}

// ...but frequency still counts when recency is equal.
func TestMoreUsesWinAtEqualRecency(t *testing.T) {
	s := New("")
	s.Record("q", "once", t0)
	for range 5 {
		s.Record("q", "often", t0)
	}
	if s.Score("often", t0) <= s.Score("once", t0) {
		t.Error("the more-used application did not score higher")
	}
}

func TestScoreHandlesAClockGoingBackwards(t *testing.T) {
	s := New("")
	s.Record("q", "app", t0)
	// An NTP correction or a suspend/resume can produce this. It must not
	// inflate the score or produce a NaN.
	got := s.Score("app", t0.Add(-time.Hour))
	if got != 1 {
		t.Errorf("score with time running backwards = %v, want it unchanged at 1", got)
	}
}

func TestPinnedIsExactAndNormalised(t *testing.T) {
	s := New("")
	s.Record("ff", "firefox.desktop", t0)

	for _, q := range []string{"ff", "FF", "  ff  ", "Ff"} {
		if got := s.Pinned(q); got != "firefox.desktop" {
			t.Errorf("Pinned(%q) = %q, want firefox.desktop", q, got)
		}
	}
	// Prefix matching would mean teaching "ff" also rearranges "f", which
	// is the action-at-a-distance this deliberately avoids.
	for _, q := range []string{"f", "fff", "fi", ""} {
		if got := s.Pinned(q); got != "" {
			t.Errorf("Pinned(%q) = %q, want nothing", q, got)
		}
	}
}

// Choosing from the unfiltered list says which app you wanted, not which
// letters mean it. Recording "" would pin the first thing ever launched to
// the top of the empty query for good.
func TestEmptyQueryIsNotPinned(t *testing.T) {
	s := New("")
	s.Record("", "firefox.desktop", t0)
	s.Record("   ", "firefox.desktop", t0)

	if got := s.Pinned(""); got != "" {
		t.Errorf("Pinned(\"\") = %q, want nothing", got)
	}
	if len(s.Queries) != 0 {
		t.Errorf("Queries = %v, want empty", s.Queries)
	}
	// The usage score is still recorded -- it is only the pairing that is
	// meaningless.
	if s.Score("firefox.desktop", t0) == 0 {
		t.Error("the usage score was not recorded for an empty query")
	}
}

func TestRecordIgnoresAnEmptyAppID(t *testing.T) {
	s := New("")
	s.Record("q", "", t0)
	if len(s.Apps) != 0 || len(s.Queries) != 0 {
		t.Errorf("recorded an empty app id: apps=%v queries=%v", s.Apps, s.Queries)
	}
}

func TestForget(t *testing.T) {
	s := New("")
	s.Record("ff", "firefox.desktop", t0)
	s.Record("fi", "firefox.desktop", t0)
	s.Record("ca", "calc.desktop", t0)

	s.Forget("firefox.desktop")
	if s.Score("firefox.desktop", t0) != 0 {
		t.Error("the score survived Forget")
	}
	// Every query taught for it must go too, or they would point at
	// something with no score.
	for _, q := range []string{"ff", "fi"} {
		if got := s.Pinned(q); got != "" {
			t.Errorf("Pinned(%q) = %q after Forget, want nothing", q, got)
		}
	}
	if s.Pinned("ca") != "calc.desktop" {
		t.Error("Forget removed an unrelated entry")
	}
}

func TestPruneDropsDecayedEntries(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "f.json"))
	s.Record("q", "ancient", t0)
	s.Record("q2", "recent", t0)

	// Long enough for "ancient" to fall under the threshold.
	later := t0.Add(300 * 24 * time.Hour)
	s.Record("q2", "recent", later)
	s.prune(later)

	if _, ok := s.Apps["ancient"]; ok {
		t.Error("a fully decayed entry survived pruning")
	}
	if _, ok := s.Apps["recent"]; !ok {
		t.Error("a live entry was pruned")
	}
	// A query pointing at a pruned app would be a dangling pin.
	if got := s.Pinned("q"); got != "" {
		t.Errorf("Pinned(q) = %q, want nothing after its app was pruned", got)
	}
}

func TestPruneBoundsTheNumberOfEntries(t *testing.T) {
	s := New("")
	// All at the same instant so nothing decays; only the cap can act.
	for i := range maxEntries + 50 {
		s.Record("q", string(rune('a'+i%26))+string(rune(i)), t0)
	}
	s.prune(t0)
	if len(s.Apps) > maxEntries {
		t.Errorf("kept %d entries, want at most %d", len(s.Apps), maxEntries)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "frecency.json")
	s := New(path)
	s.Record("fi", "firefox.desktop", t0)
	s.Record("fi", "firefox.desktop", t0)
	s.Record("ca", "calc.desktop", t0)

	if err := s.Save(t0); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.Score("firefox.desktop", t0), s.Score("firefox.desktop", t0); got != want {
		t.Errorf("score after a round trip = %v, want %v", got, want)
	}
	if got := loaded.Pinned("fi"); got != "firefox.desktop" {
		t.Errorf("Pinned after a round trip = %q, want firefox.desktop", got)
	}
	// Saving again must work: the path has to survive Load.
	if err := loaded.Save(t0); err != nil {
		t.Errorf("Save after Load: %v", err)
	}
}

func TestSaveCreatesTheDirectoryPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "frecency.json")
	s := New(path)
	s.Record("q", "app", t0)
	if err := s.Save(t0); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// What you launch is not something to leave world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

// Save must not leave the temporary file behind; a launcher run on every
// keypress would otherwise litter the state directory.
func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frecency.json")
	s := New(path)
	for i := range 5 {
		s.Record("q", "app", t0.Add(time.Duration(i)*time.Minute))
		if err := s.Save(t0); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only frecency.json", names)
	}
}

// The first run has no file, and that is not a failure.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nothing-here.json"))
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if s == nil {
		t.Fatal("Load returned no store")
	}
	// It must be usable, not just non-nil.
	s.Record("q", "app", t0)
	if s.Score("app", t0) != 1 {
		t.Error("the store from a missing file does not work")
	}
}

// A corrupt file must cost the memory, not the launcher.
func TestLoadCorruptFileReturnsAUsableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frecency.json")
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil {
		t.Error("Load reported no error for a corrupt file")
	}
	if s == nil {
		t.Fatal("Load returned no store for a corrupt file")
	}
	s.Record("q", "app", t0)
	if s.Score("app", t0) != 1 {
		t.Error("the store from a corrupt file does not work")
	}
}

// JSON with the keys missing unmarshals to nil maps, and every writer then
// panics. This is the crash that would have happened on a hand-edited file.
func TestLoadFileWithMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frecency.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Record("q", "app", t0) // must not panic
	if s.Score("app", t0) != 1 {
		t.Error("the store does not work after loading a file with no keys")
	}
}

func TestSaveWithNoPath(t *testing.T) {
	if err := New("").Save(t0); err == nil {
		t.Error("Save with no path succeeded, want an error")
	}
}

func TestRankerBindsOneMoment(t *testing.T) {
	s := New("")
	s.Record("fi", "firefox.desktop", t0)
	r := s.At(t0.Add(HalfLife))

	if got := r.Score("firefox.desktop"); got < 0.49 || got > 0.51 {
		t.Errorf("Ranker score = %v, want about 0.5", got)
	}
	if got := r.Pinned("fi"); got != "firefox.desktop" {
		t.Errorf("Ranker pinned = %q, want firefox.desktop", got)
	}
	if got := r.Score("unknown"); got != 0 {
		t.Errorf("unknown app scored %v, want 0", got)
	}
}

// A nil Ranker is what "no memory" looks like, and it must be safe.
func TestNilRankerIsSafe(t *testing.T) {
	var r *Ranker
	if got := r.Score("anything"); got != 0 {
		t.Errorf("nil Ranker scored %v, want 0", got)
	}
	if got := r.Pinned("anything"); got != "" {
		t.Errorf("nil Ranker pinned %q, want nothing", got)
	}
}

func TestDefaultPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/custom/state/osxflow/frecency.json"; got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	got, err = DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "frecency.json" {
		t.Errorf("DefaultPath = %q, want it to end in frecency.json", got)
	}
}

// The on-disk shape is a compatibility surface: a future version has to be
// able to read what this one wrote.
func TestFileFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frecency.json")
	s := New(path)
	s.Record("fi", "firefox.desktop", t0)
	if err := s.Save(t0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Apps map[string]struct {
			Score float64   `json:"score"`
			Last  time.Time `json:"last"`
		} `json:"apps"`
		Queries map[string]string `json:"queries"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("the saved file does not have the documented shape: %v\n%s", err, data)
	}
	if raw.Apps["firefox.desktop"].Score != 1 {
		t.Errorf("apps = %+v, want firefox.desktop at score 1", raw.Apps)
	}
	if raw.Queries["fi"] != "firefox.desktop" {
		t.Errorf("queries = %v, want fi -> firefox.desktop", raw.Queries)
	}
}
