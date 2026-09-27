package main

import "testing"

func TestEntryAnswer(t *testing.T) {
	m := NewModel(ModeEntry, nil, "", false, false)
	if _, ok := m.Answer(); ok {
		t.Error("an empty entry should have nothing to accept")
	}
	m.Insert("hello")
	if got, ok := m.Answer(); !ok || got != "hello" {
		t.Errorf("Answer() = %q,%v want hello,true", got, ok)
	}
	m.Backspace()
	if got, _ := m.Answer(); got != "hell" {
		t.Errorf("after Backspace: %q", got)
	}
	m.Clear()
	if _, ok := m.Answer(); ok {
		t.Error("after Clear there should be nothing to accept")
	}
}

// Whitespace only is a dismissal, not a project called " ".
func TestEntryAllowEmpty(t *testing.T) {
	m := NewModel(ModeEntry, nil, "", false, true)
	if got, ok := m.Answer(); !ok || got != "" {
		t.Errorf("Answer() = %q,%v want an empty answer to be acceptable", got, ok)
	}
}

func TestEntryRejectsBlank(t *testing.T) {
	m := NewModel(ModeEntry, nil, "   ", false, false)
	if got, ok := m.Answer(); ok {
		t.Errorf("Answer() = %q,%v want blank rejected", got, ok)
	}
}

func TestEntryDefault(t *testing.T) {
	m := NewModel(ModeEntry, nil, "xsnews", false, false)
	if got, ok := m.Answer(); !ok || got != "xsnews" {
		t.Errorf("Answer() = %q,%v want xsnews,true", got, ok)
	}
}

func TestListPicksHighlightedRow(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews", "hitnews", "portal"}, "", true, false)
	if got, ok := m.Answer(); !ok || got != "xsnews" {
		t.Errorf("a fresh list should offer its first row, got %q,%v", got, ok)
	}
	m.Down()
	if got, _ := m.Answer(); got != "hitnews" {
		t.Errorf("after Down: %q", got)
	}
	m.Up()
	if got, _ := m.Answer(); got != "xsnews" {
		t.Errorf("after Up: %q", got)
	}
}

func TestListDownStopsAtTheEnd(t *testing.T) {
	m := NewModel(ModeList, []string{"a", "b"}, "", true, false)
	for range 5 {
		m.Down()
	}
	if got, _ := m.Answer(); got != "b" {
		t.Errorf("Down past the end should stay on the last row, got %q", got)
	}
}

func TestListFiltersAsYouType(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews", "hitnews", "portal"}, "", true, false)
	m.Insert("news")
	if got := m.Shown(); len(got) != 2 {
		t.Fatalf("Shown() = %v want the two *news rows", got)
	}
	// Matching is case-insensitive.
	m.Clear()
	m.Insert("PORT")
	if got := m.Shown(); len(got) != 1 || got[0] != "portal" {
		t.Errorf("Shown() = %v want [portal]", got)
	}
}

// The reason -editable exists: a project that is not in the list yet.
func TestEditableListAcceptsANewName(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews", "portal"}, "", true, false)
	m.Insert("brandnew")
	if got := m.Shown(); len(got) != 0 {
		t.Fatalf("Shown() = %v want nothing matching", got)
	}
	rows := m.Rows()
	if len(rows) != 1 || !rows[0].Add {
		t.Fatalf("Rows() = %+v want a single add row", rows)
	}
	if got, ok := m.Answer(); !ok || got != "brandnew" {
		t.Errorf("Answer() = %q,%v want brandnew,true", got, ok)
	}
}

// The case that matters most, because it is nearly every use: typing a
// few letters to narrow to a project that already exists. Enter must take
// the match, not offer to create a project called "xsn".
func TestTypingNarrowsToTheExistingProject(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews", "hitnews", "portal"}, "", true, false)
	m.Insert("xsn")
	rows := m.Rows()
	if len(rows) != 2 || rows[0].Value != "xsnews" || !rows[1].Add {
		t.Fatalf("Rows() = %+v want the match first and the add row last", rows)
	}
	if got, ok := m.Answer(); !ok || got != "xsnews" {
		t.Errorf("Answer() = %q,%v want xsnews,true", got, ok)
	}
	// Creating "xsn" anyway is still reachable, one row down.
	m.Down()
	if got, _ := m.Answer(); got != "xsn" {
		t.Errorf("after Down: %q want xsn", got)
	}
}

// Typing a name that no existing project contains leaves only the offer to
// create it, so it is highlighted without a keystroke.
func TestAddRowIsTheOnlyRowWhenNothingMatches(t *testing.T) {
	m := NewModel(ModeList, []string{"portal"}, "", true, false)
	m.Insert("portal-v2")
	rows := m.Rows()
	if len(rows) != 1 || !rows[0].Add {
		t.Fatalf("Rows() = %+v want just the add row", rows)
	}
	if got, ok := m.Answer(); !ok || got != "portal-v2" {
		t.Errorf("Answer() = %q,%v want portal-v2,true", got, ok)
	}
}

// No add row for something already on the list, however it was typed.
func TestNoAddRowForAnExistingProject(t *testing.T) {
	m := NewModel(ModeList, []string{"Portal"}, "", true, false)
	m.Insert("portal")
	for _, r := range m.Rows() {
		if r.Add {
			t.Fatalf("Rows() = %+v want no add row for an existing name", m.Rows())
		}
	}
	if got, ok := m.Answer(); !ok || got != "Portal" {
		t.Errorf("Answer() = %q,%v want the existing Portal", got, ok)
	}
}

// A non-editable list never offers to create anything.
func TestNoAddRowWhenNotEditable(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews"}, "", false, false)
	m.Insert("brandnew")
	if rows := m.Rows(); len(rows) != 0 {
		t.Errorf("Rows() = %+v want nothing offered", rows)
	}
}

// A non-editable list must never invent an answer from the typed filter.
func TestNonEditableListRejectsTypedText(t *testing.T) {
	m := NewModel(ModeList, []string{"xsnews"}, "", false, false)
	m.Insert("nothinglikeit")
	if got, ok := m.Answer(); ok {
		t.Errorf("Answer() = %q,%v want nothing acceptable", got, ok)
	}
}

// Up stops at the first row rather than falling off into a hidden state.
func TestUpStopsAtTheTop(t *testing.T) {
	m := NewModel(ModeList, []string{"a", "b"}, "", true, false)
	m.Up()
	m.Up()
	if m.Selected() != 0 {
		t.Errorf("Selected() = %d want to stay on the first row", m.Selected())
	}
}

func TestMessageIsAlwaysAcceptable(t *testing.T) {
	m := NewModel(ModeMessage, nil, "", false, false)
	if got, ok := m.Answer(); !ok || got != "" {
		t.Errorf("Answer() = %q,%v want \"\",true", got, ok)
	}
	m.Insert("x")
	m.Backspace()
	if got, _ := m.Answer(); got != "" {
		t.Errorf("a message dialog should ignore typing, got %q", got)
	}
}
