package ui

// The interface's state, kept deliberately free of X11 so that the
// behaviour a user actually notices -- what is selected after typing, what
// happens to the scroll position, when the calculator takes over the first
// row -- can be tested without a display.

import (
	"errors"
	"strings"
	"unicode"

	"github.com/mpdroog/osxflow/internal/calc"
	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/search"
)

// Row is one line of the result list, ready to draw.
type Row struct {
	// App is the application this row launches, or nil for a calculator
	// row.
	App *desktop.App

	Primary   string
	Secondary string
}

// Model is the state behind the window.
type Model struct {
	apps    []desktop.App
	results []search.Result

	// ranker supplies usage memory. Nil is valid and means no memory.
	ranker search.Ranker

	query string

	// calcText is the rendered calculator result, empty when the query is
	// not an expression or does not evaluate.
	calcText string

	// selected indexes rows(), so it counts the calculator row when there
	// is one.
	selected int

	// offset is the first visible row, adjusted only enough to keep the
	// selection on screen.
	offset int

	// visible is how many rows fit in the window.
	visible int
}

// NewModel builds the state for a list of applications. The slice is not
// copied and must outlive the model: rows point into it.
//
// ranker may be nil, which is the first run and most tests.
func NewModel(apps []desktop.App, visibleRows int, ranker search.Ranker) *Model {
	if visibleRows < 1 {
		visibleRows = 1
	}
	m := &Model{apps: apps, visible: visibleRows, ranker: ranker}
	m.recompute()
	return m
}

// Query is the current text.
func (m *Model) Query() string { return m.query }

// SetQuery replaces the text and resets the selection to the top, which is
// what a changed query means: the old selection referred to a list that no
// longer exists.
func (m *Model) SetQuery(q string) {
	m.query = q
	m.selected, m.offset = 0, 0
	m.recompute()
}

// Insert appends typed text, ignoring control characters. Those arrive
// from the keymap as real runes -- Escape is U+001B -- and inserting them
// would put invisible junk in the query.
func (m *Model) Insert(s string) {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return
	}
	m.SetQuery(m.query + b.String())
}

// Backspace removes the last rune, not the last byte.
func (m *Model) Backspace() {
	if m.query == "" {
		return
	}
	r := []rune(m.query)
	m.SetQuery(string(r[:len(r)-1]))
}

// DeleteWord removes the trailing word, for Ctrl+W.
func (m *Model) DeleteWord() {
	trimmed := strings.TrimRightFunc(m.query, unicode.IsSpace)
	if i := strings.LastIndexFunc(trimmed, unicode.IsSpace); i >= 0 {
		m.SetQuery(trimmed[:i+1])
		return
	}
	m.SetQuery("")
}

// Clear empties the query, for Ctrl+U.
func (m *Model) Clear() { m.SetQuery("") }

// Move changes the selection by delta, clamping at both ends.
//
// Clamping rather than wrapping is deliberate: holding Down should come to
// rest at the bottom of the list, not cycle back to the top past the entry
// you were aiming for.
func (m *Model) Move(delta int) {
	n := m.rowCount()
	if n == 0 {
		m.selected, m.offset = 0, 0
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= n {
		m.selected = n - 1
	}
	m.scrollToSelection()
}

// Selected returns the application under the cursor, and false when the
// selection is a calculator result or the list is empty.
func (m *Model) Selected() (*desktop.App, bool) {
	rows := m.allRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return nil, false
	}
	app := rows[m.selected].App
	return app, app != nil
}

// CalcText is the calculator result for the current query, or "".
func (m *Model) CalcText() string { return m.calcText }

// Rows returns the visible slice of the list, and the index within it that
// is selected (-1 when the selection is off screen, which should not
// happen).
func (m *Model) Rows() (rows []Row, selected int) {
	all := m.allRows()
	if len(all) == 0 {
		return nil, -1
	}
	end := min(m.offset+m.visible, len(all))
	return all[m.offset:end], m.selected - m.offset
}

// TotalRows is the number of rows the query produced, visible or not.
func (m *Model) TotalRows() int { return m.rowCount() }

func (m *Model) rowCount() int { return len(m.allRows()) }

// allRows builds the full list: the calculator result first when there is
// one, then the matching applications.
//
// The calculator goes first because when the query is arithmetic that is
// what the user is looking at, and because it means Enter does the obvious
// thing without them having to move the selection.
func (m *Model) allRows() []Row {
	rows := make([]Row, 0, len(m.results)+1)
	if m.calcText != "" {
		rows = append(rows, Row{Primary: m.calcText, Secondary: m.query})
	}
	for i := range m.results {
		app := m.results[i].App
		rows = append(rows, Row{App: app, Primary: app.Name, Secondary: rowDetail(app)})
	}
	return rows
}

// rowDetail is the dimmer second line: the entry's own description when it
// has one, and the command otherwise. The command is genuinely useful
// here, because it is how you tell two identically named entries apart.
func rowDetail(app *desktop.App) string {
	if app.Comment != "" {
		return app.Comment
	}
	return strings.Join(app.Argv, " ")
}

func (m *Model) recompute() {
	m.results = search.Filter(m.apps, m.query, m.ranker)
	m.calcText = evalQuery(m.query)
	m.scrollToSelection()
}

// evalQuery renders the calculator line, or "" when there is nothing worth
// showing.
//
// An incomplete expression produces nothing rather than an error: the
// query is being typed, and "12 *" is a state every multiplication passes
// through. A wrong expression produces nothing either, because the
// alternative is an error message shouting at someone halfway through
// typing an application name that happens to start with a digit.
func evalQuery(query string) string {
	if !calc.LooksLikeExpr(query) {
		return ""
	}
	v, err := calc.Eval(query)
	if err != nil {
		if errors.Is(err, calc.ErrIncomplete) {
			return ""
		}
		return ""
	}
	return "= " + calc.Format(v)
}

// scrollToSelection moves the window over the list by the smallest amount
// that puts the selection back on screen.
func (m *Model) scrollToSelection() {
	n := m.rowCount()
	if n == 0 {
		m.offset = 0
		return
	}
	if m.selected >= n {
		m.selected = n - 1
	}
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+m.visible {
		m.offset = m.selected - m.visible + 1
	}
	if maxOffset := max(n-m.visible, 0); m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}
