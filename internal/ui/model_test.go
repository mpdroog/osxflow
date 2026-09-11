package ui

import (
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
)

func testApps(names ...string) []desktop.App {
	out := make([]desktop.App, 0, len(names))
	for _, n := range names {
		out = append(out, desktop.App{Name: n, Argv: []string{strings.ToLower(n)}})
	}
	return out
}

func rowNames(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Primary)
	}
	return out
}

func TestModelStartsShowingEverything(t *testing.T) {
	m := NewModel(testApps("Alpha", "Beta", "Gamma"), 7, nil)
	if m.Query() != "" {
		t.Errorf("Query = %q, want empty", m.Query())
	}
	if m.TotalRows() != 3 {
		t.Errorf("TotalRows = %d, want 3", m.TotalRows())
	}
	rows, sel := m.Rows()
	if len(rows) != 3 || sel != 0 {
		t.Errorf("Rows = %v selected %d, want 3 rows with the first selected", rowNames(rows), sel)
	}
}

func TestModelInsertAndBackspace(t *testing.T) {
	m := NewModel(testApps("Firefox", "Files"), 7, nil)
	m.Insert("fi")
	if m.Query() != "fi" {
		t.Fatalf("Query = %q, want fi", m.Query())
	}
	m.Backspace()
	if m.Query() != "f" {
		t.Errorf("Query = %q, want f", m.Query())
	}
	m.Backspace()
	m.Backspace() // on an empty query must not panic
	if m.Query() != "" {
		t.Errorf("Query = %q, want empty", m.Query())
	}
}

// Control characters arrive from the keymap as real runes; inserting them
// would put invisible junk in the query.
func TestModelInsertIgnoresControlCharacters(t *testing.T) {
	m := NewModel(testApps("Thing"), 7, nil)
	m.Insert("\x1b")
	m.Insert("\r")
	m.Insert("\x00")
	if m.Query() != "" {
		t.Errorf("Query = %q, want control characters ignored", m.Query())
	}
	m.Insert("a\x1bb")
	if m.Query() != "ab" {
		t.Errorf("Query = %q, want ab", m.Query())
	}
}

func TestModelBackspaceIsRuneWise(t *testing.T) {
	m := NewModel(testApps("Thing"), 7, nil)
	m.Insert("aé")
	m.Backspace()
	if m.Query() != "a" {
		t.Errorf("Query = %q, want a (a multi-byte rune must go in one press)", m.Query())
	}
}

func TestModelDeleteWord(t *testing.T) {
	tests := []struct{ in, want string }{
		{"one two", "one "},
		{"one two   ", "one "},
		{"single", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range tests {
		m := NewModel(testApps("X"), 7, nil)
		m.SetQuery(tc.in)
		m.DeleteWord()
		if m.Query() != tc.want {
			t.Errorf("DeleteWord(%q) = %q, want %q", tc.in, m.Query(), tc.want)
		}
	}
}

func TestModelClear(t *testing.T) {
	m := NewModel(testApps("X"), 7, nil)
	m.SetQuery("something")
	m.Clear()
	if m.Query() != "" {
		t.Errorf("Query = %q, want empty", m.Query())
	}
}

// A changed query must reset the selection: the old index referred to a
// list that no longer exists, and silently launching whatever now sits at
// that position is the worst failure this program can have.
func TestModelQueryResetsSelection(t *testing.T) {
	m := NewModel(testApps("Alpha", "Beta", "Gamma"), 7, nil)
	m.Move(2)
	if _, sel := m.Rows(); sel != 2 {
		t.Fatalf("selected %d, want 2", sel)
	}
	m.Insert("a")
	if _, sel := m.Rows(); sel != 0 {
		t.Errorf("selected %d after typing, want 0", sel)
	}
}

func TestModelMoveClamps(t *testing.T) {
	m := NewModel(testApps("Alpha", "Beta", "Gamma"), 7, nil)
	m.Move(-5)
	if _, sel := m.Rows(); sel != 0 {
		t.Errorf("selected %d, want 0 after moving up past the top", sel)
	}
	m.Move(100)
	rows, sel := m.Rows()
	if sel != len(rows)-1 {
		t.Errorf("selected %d of %d, want the last row", sel, len(rows))
	}
	// Clamping, not wrapping: another Down must stay put.
	m.Move(1)
	if _, sel2 := m.Rows(); sel2 != sel {
		t.Errorf("selection wrapped to %d, want it to stay at %d", sel2, sel)
	}
}

func TestModelMoveOnEmptyList(t *testing.T) {
	m := NewModel(testApps("Alpha"), 7, nil)
	m.SetQuery("zzzzzz")
	if m.TotalRows() != 0 {
		t.Fatalf("TotalRows = %d, want 0", m.TotalRows())
	}
	m.Move(1)
	m.Move(-1)
	if _, ok := m.Selected(); ok {
		t.Error("Selected returned an app from an empty list")
	}
}

func TestModelScrolling(t *testing.T) {
	const visible = 3
	m := NewModel(testApps("A1", "A2", "A3", "A4", "A5", "A6"), visible, nil)

	rows, sel := m.Rows()
	if len(rows) != visible || rows[0].Primary != "A1" || sel != 0 {
		t.Fatalf("initial view = %v selected %d", rowNames(rows), sel)
	}

	// Moving within the window must not scroll.
	m.Move(2)
	rows, sel = m.Rows()
	if rows[0].Primary != "A1" || sel != 2 {
		t.Errorf("view = %v selected %d, want no scroll yet", rowNames(rows), sel)
	}

	// Moving past the bottom scrolls by exactly one.
	m.Move(1)
	rows, sel = m.Rows()
	if rows[0].Primary != "A2" || sel != visible-1 {
		t.Errorf("view = %v selected %d, want it scrolled by one", rowNames(rows), sel)
	}

	// And back up again.
	m.Move(-3)
	rows, sel = m.Rows()
	if rows[0].Primary != "A1" || sel != 0 {
		t.Errorf("view = %v selected %d, want the top again", rowNames(rows), sel)
	}
}

func TestModelScrollingClampsAtEnd(t *testing.T) {
	m := NewModel(testApps("A1", "A2", "A3", "A4", "A5"), 3, nil)
	m.Move(100)
	rows, sel := m.Rows()
	if len(rows) != 3 {
		t.Fatalf("view has %d rows, want 3", len(rows))
	}
	if rows[len(rows)-1].Primary != "A5" || sel != 2 {
		t.Errorf("view = %v selected %d, want the last three with A5 selected", rowNames(rows), sel)
	}
}

func TestModelSelected(t *testing.T) {
	apps := testApps("Alpha", "Beta")
	m := NewModel(apps, 7, nil)
	app, ok := m.Selected()
	if !ok {
		t.Fatal("Selected reported nothing")
	}
	if app != &apps[0] {
		t.Error("Selected did not point into the caller's slice")
	}
	m.Move(1)
	app, _ = m.Selected()
	if app.Name != "Beta" {
		t.Errorf("Selected = %q, want Beta", app.Name)
	}
}

func TestModelCalculatorRow(t *testing.T) {
	m := NewModel(testApps("Alpha"), 7, nil)
	m.SetQuery("2+2*10")

	if got := m.CalcText(); got != "= 22" {
		t.Errorf("CalcText = %q, want \"= 22\"", got)
	}
	rows, _ := m.Rows()
	if len(rows) == 0 || rows[0].Primary != "= 22" {
		t.Fatalf("rows = %v, want the calculator first", rowNames(rows))
	}
	if rows[0].App != nil {
		t.Error("the calculator row has an App attached")
	}
	// Enter on a calculator row must not resolve to an application.
	if _, ok := m.Selected(); ok {
		t.Error("Selected returned an app while the calculator row is selected")
	}
}

// A half-typed sum must show nothing rather than an error: every
// multiplication passes through "12 *" on the way to being written.
func TestModelCalculatorStaysQuietWhileTyping(t *testing.T) {
	m := NewModel(testApps("Alpha"), 7, nil)
	for _, q := range []string{"1", "1+", "1+2*", "(", "2^"} {
		m.SetQuery(q)
		if got := m.CalcText(); got != "" && q != "1" {
			t.Errorf("CalcText(%q) = %q, want nothing while incomplete", q, got)
		}
	}
}

func TestModelCalculatorIgnoresAppNames(t *testing.T) {
	m := NewModel(testApps("Firefox", "gnome-terminal"), 7, nil)
	for _, q := range []string{"firefox", "gnome-terminal", "fire"} {
		m.SetQuery(q)
		if got := m.CalcText(); got != "" {
			t.Errorf("CalcText(%q) = %q, want nothing: that is an app search", q, got)
		}
	}
}

// A sum that cannot be worked out says why, in a dimmed calculator row:
// that is how the user learns the answer is "division by zero" rather than
// that the calculator broke.
func TestModelCalculatorShowsErrors(t *testing.T) {
	m := NewModel(testApps("Alpha"), 7, nil)
	m.SetQuery("1/0")
	if got := m.CalcText(); got != "" {
		t.Errorf("CalcText = %q, want no result for a bad expression", got)
	}
	if err := m.CalcErr(); err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("CalcErr = %v, want division by zero", err)
	}
	rows, sel := m.Rows()
	if len(rows) == 0 || !rows[0].CalcError || !strings.Contains(rows[0].Primary, "division by zero") {
		t.Fatalf("rows = %+v, want the error as the first row", rows)
	}
	if rows[0].App != nil || rows[0].Secondary != "1/0" {
		t.Errorf("error row = %+v, want no app and the query underneath", rows[0])
	}
	if sel != 0 {
		t.Errorf("selected %d, want the error row: nothing else is listed", sel)
	}
	// Enter on it does nothing, as on a result.
	if _, ok := m.Selected(); ok {
		t.Error("Selected returned an app for the error row")
	}

	// Fixing the sum replaces the error with the result.
	m.SetQuery("1/2")
	if m.CalcErr() != nil || m.CalcText() != "= 0.5" {
		t.Errorf("after fixing: CalcText = %q, CalcErr = %v", m.CalcText(), m.CalcErr())
	}
	// Still being typed is not an error.
	m.SetQuery("1/")
	if m.CalcErr() != nil {
		t.Errorf("CalcErr(\"1/\") = %v, want nil while incomplete", m.CalcErr())
	}
}

// An error row must not take the selection from an application: an app
// whose name happens to parse as a broken sum still launches on Enter.
func TestModelCalculatorErrorDoesNotStealTheSelection(t *testing.T) {
	m := NewModel(testApps("1/0 Tool", "Other"), 7, nil)
	m.SetQuery("1/0")
	if m.CalcErr() == nil {
		t.Fatal("CalcErr = nil, want division by zero")
	}
	app, ok := m.Selected()
	if !ok || app.Name != "1/0 Tool" {
		t.Fatalf("Selected = %v, %v; want the app under the error row", app, ok)
	}
	rows, sel := m.Rows()
	if sel != 1 || !rows[0].CalcError {
		t.Errorf("selected %d of %+v, want row 1 under the error", sel, rows)
	}
	// The error row can still be reached, and choosing it still does nothing.
	m.Move(-1)
	if _, ok := m.Selected(); ok {
		t.Error("Selected returned an app with the error row selected")
	}
}

func TestModelRowDetail(t *testing.T) {
	apps := []desktop.App{
		{Name: "With Comment", Comment: "does things", Argv: []string{"wc"}},
		{Name: "No Comment", Argv: []string{"nc", "--flag"}},
	}
	m := NewModel(apps, 7, nil)
	rows, _ := m.Rows()
	if rows[0].Secondary != "does things" {
		t.Errorf("row detail = %q, want the comment", rows[0].Secondary)
	}
	// Without a comment the command is shown, because that is how you tell
	// two identically named entries apart.
	if rows[1].Secondary != "nc --flag" {
		t.Errorf("row detail = %q, want the command", rows[1].Secondary)
	}
}

func TestNewModelRejectsZeroVisibleRows(t *testing.T) {
	m := NewModel(testApps("A", "B"), 0, nil)
	rows, _ := m.Rows()
	if len(rows) != 1 {
		t.Errorf("got %d visible rows, want at least 1", len(rows))
	}
}

func TestModelEmptyAppList(t *testing.T) {
	m := NewModel(nil, 7, nil)
	if m.TotalRows() != 0 {
		t.Errorf("TotalRows = %d, want 0", m.TotalRows())
	}
	rows, sel := m.Rows()
	if len(rows) != 0 || sel != -1 {
		t.Errorf("Rows = %v selected %d, want none", rowNames(rows), sel)
	}
	// The calculator must still work with no applications installed.
	m.SetQuery("2+2")
	if m.CalcText() != "= 4" {
		t.Errorf("CalcText = %q, want \"= 4\"", m.CalcText())
	}
}

func FuzzModelTyping(f *testing.F) {
	for _, s := range []string{"fi", "2+2", "", "\x1b", "((((", strings.Repeat("a", 60)} {
		f.Add(s)
	}
	apps := testApps("Files", "Firefox", "Calculator")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 128 {
			t.Skip()
		}
		m := NewModel(apps, 7, nil)
		for _, r := range input {
			m.Insert(string(r))
		}
		m.Move(1)
		m.Move(-1)
		m.Backspace()
		m.DeleteWord()

		rows, sel := m.Rows()
		if len(rows) > 0 && (sel < 0 || sel >= len(rows)) {
			t.Fatalf("selection %d is outside the %d visible rows", sel, len(rows))
		}
		if _, ok := m.Selected(); ok && len(rows) == 0 {
			t.Fatal("Selected returned an app with no visible rows")
		}
	})
}
