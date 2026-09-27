package main

import (
	"strings"
)

// Mode is which of the three dialogs this is.
type Mode int

const (
	// ModeEntry asks for one line of text.
	ModeEntry Mode = iota
	// ModeList offers a list to pick from, and with Editable also accepts
	// something typed that is not on it.
	ModeList
	// ModeMessage shows text and waits to be dismissed.
	ModeMessage
)

// Model is everything the dialog knows that is not pixels: what was typed,
// what is offered, and what is picked. It is deliberately free of X so the
// awkward part -- which of the typed text and the selected row is the
// answer -- can be tested without a display.
type Model struct {
	Mode     Mode
	Editable bool

	// AllowEmpty lets an entry be accepted with nothing in it. It is off
	// by default because for most questions a blank answer and a dismissed
	// dialog are the same thing, and the caller can only tell them apart
	// by the exit status. A description nobody wrote is the exception: it
	// is genuinely optional, and refusing it would force a word out of
	// someone who has none.
	AllowEmpty bool

	items []string
	text  []rune

	// sel indexes Shown. -1 means the typed text is what is selected,
	// which is the state an editable list starts in and the only state a
	// plain entry has.
	sel int
}

// NewModel prepares a dialog. def is the text the entry starts with, which
// the user can accept with one keystroke.
func NewModel(mode Mode, items []string, def string, editable, allowEmpty bool) *Model {
	m := &Model{Mode: mode, Editable: editable, AllowEmpty: allowEmpty, items: items, text: []rune(def)}
	m.resetSelection()
	return m
}

// Text is what has been typed so far.
func (m *Model) Text() string { return string(m.text) }

// Shown is the items still matching what has been typed. Matching is a
// case-insensitive substring: these lists are a handful of project names,
// not the whole application menu, so ranking them would be machinery
// nobody could perceive the effect of.
func (m *Model) Shown() []string {
	if m.Mode != ModeList {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(string(m.text)))
	if q == "" {
		return m.items
	}
	var out []string
	for _, it := range m.items {
		if strings.Contains(strings.ToLower(it), q) {
			out = append(out, it)
		}
	}
	return out
}

// Row is one line the dialog offers. Add marks the row that commits what
// was typed rather than something already on the list.
type Row struct {
	Label string
	Value string
	Add   bool
}

// Rows is what the list actually shows: the matching items, and below
// them an offer to create what has been typed whenever that is not
// already one of them.
//
// The add row is last, and it is not what Enter takes while anything
// matches. Both of those are deliberate. Typing into this box is almost
// always narrowing towards a project that exists -- "xsn" for "xsnews" --
// and an add row on top turned that into: type, watch the highlight land
// on "Add xsn", press Down, press Enter. Creating a project is the rare
// case, and it is the case where nothing matches, which is exactly when
// the add row ends up highlighted anyway because it is the only row.
func (m *Model) Rows() []Row {
	if m.Mode != ModeList {
		return nil
	}
	var rows []Row
	for _, it := range m.Shown() {
		rows = append(rows, Row{Label: it, Value: it})
	}
	if m.Editable {
		if typed := strings.TrimSpace(string(m.text)); typed != "" && !m.exact(typed) {
			rows = append(rows, Row{Label: "Add \u201c" + typed + "\u201d", Value: typed, Add: true})
		}
	}
	return rows
}

// exact reports whether s is already one of the items, case-insensitively:
// there is no point offering to add a project that exists.
func (m *Model) exact(s string) bool {
	for _, it := range m.items {
		if strings.EqualFold(it, s) {
			return true
		}
	}
	return false
}

// Selected is the index into Rows that is highlighted, or -1 when there is
// nothing to highlight.
func (m *Model) Selected() int { return m.sel }

// resetSelection puts the highlight back on the first row after the text
// changed -- the best match, or the offer to create something when there
// is nothing to match.
func (m *Model) resetSelection() {
	if m.Mode != ModeList || len(m.Rows()) == 0 {
		m.sel = -1
		return
	}
	m.sel = 0
}

// Insert adds typed text.
func (m *Model) Insert(s string) {
	if m.Mode == ModeMessage {
		return
	}
	m.text = append(m.text, []rune(s)...)
	m.resetSelection()
}

// Backspace removes the last character typed.
func (m *Model) Backspace() {
	if m.Mode == ModeMessage || len(m.text) == 0 {
		return
	}
	m.text = m.text[:len(m.text)-1]
	m.resetSelection()
}

// Clear empties the text, as Ctrl+U does in a shell.
func (m *Model) Clear() {
	if m.Mode == ModeMessage {
		return
	}
	m.text = nil
	m.resetSelection()
}

// Up and Down move the highlight and stop at the ends.
func (m *Model) Up() {
	if m.Mode == ModeList && m.sel > 0 {
		m.sel--
	}
}

func (m *Model) Down() {
	if m.Mode == ModeList && m.sel < len(m.Rows())-1 {
		m.sel++
	}
}

// Answer is what accepting now would return, and whether there is anything
// to accept. An empty answer is not acceptable: it is how the caller is
// told the dialog was dismissed, so returning it as a result would make a
// blank project indistinguishable from Cancel.
func (m *Model) Answer() (string, bool) {
	if m.Mode == ModeMessage {
		return "", true
	}
	if rows := m.Rows(); m.sel >= 0 && m.sel < len(rows) {
		return rows[m.sel].Value, true
	}
	if m.Mode == ModeList {
		// A list with nothing highlighted has nothing to give: a
		// non-editable one never offers the typed text, and an editable
		// one would have shown an add row for it.
		return "", false
	}
	s := strings.TrimSpace(string(m.text))
	return s, s != "" || m.AllowEmpty
}
