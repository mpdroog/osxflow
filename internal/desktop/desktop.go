// Package desktop reads the .desktop files that make up the application
// list on a freedesktop system.
//
// This is a deliberately partial implementation of the Desktop Entry
// Specification, sized to what is actually on disk rather than to what
// the spec permits. On this machine, of 146 entries: four distinct field
// codes appear in Exec (%u %U %f %F, all of them "insert file arguments",
// all of them stripped because we launch with none), exactly one Exec
// uses quoting, and no entry uses %i, %c or %k. Localised Name[xx] keys
// are numerous and entirely unused, because reading them only matters in
// a locale that has translations we would prefer over Name.
//
// The parts that are implemented are implemented properly. The parts that
// are not — DBusActivatable, startup notification — are absent rather than
// half-present, so a future need for them is a gap to fill and not a bug
// to find.
//
// Desktop Actions used to be in that list and are now read, because the
// menubar opens the focused application's name and needs something to put
// under it. Of an action only Name and Exec are read: its Icon would need
// the SVG theme we cannot draw without cgo, and no action on this machine
// sets OnlyShowIn.
package desktop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// App is one launchable application.
type App struct {
	// ID is the desktop file id ("firefox.desktop", or "kde-konsole.desktop"
	// for a file in a subdirectory). It is the key XDG uses for shadowing:
	// an entry in an earlier directory hides the same id in a later one.
	ID string

	Name    string
	Comment string
	Icon    string

	// Argv is Exec split into arguments with field codes removed, ready to
	// hand to exec. It always has at least one element.
	Argv []string

	// Binary is Argv[0] resolved through PATH and then through symlinks,
	// or "" when it could not be found. This is the value compared against
	// /proc/<pid>/exe to decide whether an app is already running, so it
	// matters that both sides are fully resolved: /usr/bin/firefox is
	// often a symlink, and /proc reports the target.
	Binary string

	// StartupWMClass is the entry's declared window class, when it has one.
	// Only 11 of 146 entries here do, which is why it is a hint used to
	// break ties rather than the primary matching key.
	StartupWMClass string

	// Terminal marks entries that must be run inside a terminal emulator.
	Terminal bool

	// Actions are the Desktop Actions the entry declares, in the order its
	// Actions key lists them. Most entries have none: 2 of 146 here do.
	Actions []Action
}

// Action is one Desktop Action: a second way into an application that the
// entry itself names, the way Thunar's offers Home, Computer and Trash.
type Action struct {
	// ID is the action's identifier -- the "open-home" of
	// [Desktop Action open-home]. It is not shown; Name is. It is kept
	// because it, and not the translated Name, is what another entry or a
	// preference would refer to the action by.
	ID string

	Name string

	// Argv is the action's Exec, split as App.Argv is. It always has at
	// least one element: an action without one is dropped.
	Argv []string
}

// entry is the raw key/value content of a [Desktop Entry] group.
type entry map[string]string

// Load reads and interprets one .desktop file. It returns ok=false for
// entries that exist but should not appear in a launcher (hidden ones,
// non-applications, ones whose TryExec binary is missing), which is a
// different thing from an error and is why it is not reported as one.
//
// There are two kinds of failure. err means the file could not be read at
// all. problems are faults inside a file that was read -- a malformed
// line, a binary whose symlinks cannot be followed -- which cost a detail
// of the entry rather than the entry, so the app is still returned with
// them. Both name the file.
func Load(path, id string) (app App, ok bool, problems []error, err error) {
	f, err := os.Open(path) //nolint:gosec // path comes from scanning known XDG directories
	if err != nil {
		return App{}, false, nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing %s: %w", path, cerr))
		}
	}()

	e, actions, lineProblems, err := parse(f)
	if err != nil {
		return App{}, false, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	app, ok, appProblems := e.toApp(id, actions)
	for _, p := range append(lineProblems, appProblems...) {
		problems = append(problems, fmt.Errorf("%s: %w", path, p))
	}
	return app, ok, problems, nil
}

// parse reads the [Desktop Entry] group and every [Desktop Action] group,
// each into a map of its own. Any other group is skipped.
//
// A map per group is the point: an action has a Name and an Exec of its
// own, and reading every group into one map would let an action's Name
// overwrite the application's. That is why these groups used to be skipped
// entirely rather than read into the entry.
//
// Lines that are not key=value are problems rather than an error: see the
// comment where they are skipped.
func parse(r io.Reader) (e entry, actions map[string]entry, problems []error, err error) {
	e, actions = entry{}, map[string]entry{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// group is where the lines being read now go, and nil before the first
	// group header and inside a group we do not read.
	var group entry
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			group = groupFor(line, e, actions)
			continue
		}
		if group == nil {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			// Malformed lines are skipped, not fatal: one bad line should
			// not cost the user the whole application. It is still
			// reported, because it may be the line that held the Exec.
			problems = append(problems, fmt.Errorf("line %d: no '=' in %q, skipped", lineNo, line))
			continue
		}
		key = strings.TrimSpace(key)
		// Localised keys are dropped rather than resolved. See the package
		// comment: in this locale the untranslated Name is the right answer
		// and reading 6455 unused strings per scan is not free. Action
		// groups are where most of them are -- thunar.desktop is 400 lines
		// of translated action names around four lines that matter.
		if strings.ContainsRune(key, '[') {
			continue
		}
		if _, dup := group[key]; dup {
			continue // first occurrence wins, per spec
		}
		group[key] = unescape(strings.TrimSpace(value))
	}
	if err := sc.Err(); err != nil {
		return nil, nil, nil, err
	}
	return e, actions, problems, nil
}

// groupFor picks the map a group header's lines belong in: the
// application's own entry for [Desktop Entry], a fresh map for each
// [Desktop Action <id>], and nil for anything else.
//
// The header is compared without lowering it first, so that a group name
// whose case folds to a different length cannot shift the index the id is
// cut at.
func groupFor(header string, e entry, actions map[string]entry) entry {
	const prefix = "[Desktop Action "
	if strings.EqualFold(header, "[Desktop Entry]") {
		return e
	}
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) ||
		!strings.HasSuffix(header, "]") {
		return nil
	}
	id := strings.TrimSpace(header[len(prefix) : len(header)-1])
	if id == "" {
		return nil
	}
	if _, dup := actions[id]; dup {
		return nil // first occurrence wins, as it does for a key
	}
	group := entry{}
	actions[id] = group
	return group
}

// unescape expands the string escapes the spec defines for values.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			// Unknown escape: keep both bytes. Dropping the backslash
			// would silently corrupt paths like C:\temp in a Wine entry.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (e entry) bool(key string) bool { return strings.EqualFold(e[key], "true") }

// toApp applies the visibility rules and resolves the executable.
//
// problems are the faults that cost a detail rather than the entry; see
// Load. A binary that is simply not installed is not one of them.
func (e entry) toApp(id string, actions map[string]entry) (app App, ok bool, problems []error) {
	if e["Type"] != "Application" {
		return App{}, false, nil
	}
	if e.bool("NoDisplay") || e.bool("Hidden") {
		return App{}, false, nil
	}
	name := e["Name"]
	if name == "" {
		return App{}, false, nil
	}
	// TryExec names a binary whose absence means the entry should not be
	// shown — the standard way a package ships an entry for something
	// installed separately. When the check itself fails the entry stays
	// hidden, as it always has, but now says why.
	if try := e["TryExec"]; try != "" {
		path, err := findBinary(try)
		if err != nil {
			return App{}, false, []error{fmt.Errorf("checking TryExec %q, hiding the entry: %w", try, err)}
		}
		if path == "" {
			return App{}, false, nil
		}
	}
	argv := parseExec(e["Exec"])
	if len(argv) == 0 {
		return App{}, false, nil
	}

	// An unresolved Binary costs the most reliable way of finding the
	// app's running window, not the app: it can still be started, and
	// matched by window class.
	binary, err := findBinary(argv[0])
	if err != nil {
		problems = append(problems, fmt.Errorf("finding %q, cannot match its windows by executable: %w", argv[0], err))
	}
	if binary != "" {
		resolved, err := filepath.EvalSymlinks(binary)
		if err != nil {
			// It exists but a link on the way cannot be followed. The
			// unresolved path still helps: it matches /proc whenever the
			// binary was not a symlink in the first place.
			problems = append(problems, fmt.Errorf("resolving symlinks of %s, matching by the unresolved path: %w", binary, err))
		} else {
			binary = resolved
		}
	}
	acts, actProblems := e.actionList(actions)
	problems = append(problems, actProblems...)

	return App{
		ID:             id,
		Name:           name,
		Comment:        e["Comment"],
		Icon:           e["Icon"],
		Argv:           argv,
		Binary:         binary,
		StartupWMClass: e["StartupWMClass"],
		Terminal:       e.bool("Terminal"),
		Actions:        acts,
	}, true, problems
}

// actionList builds the entry's Desktop Actions, in the order its Actions
// key lists them.
//
// That key is authoritative, not a summary: the spec says a group not named
// there must be ignored, which is how an entry retires an action without
// deleting its group. So the key is walked, not the map.
//
// An id named there with no group, or a group with no Name or no Exec, is a
// fault in the file rather than in us: it costs that one action and is
// reported as a problem, the same way an unresolvable binary costs the
// matching and not the entry.
func (e entry) actionList(groups map[string]entry) (out []Action, problems []error) {
	for _, id := range strings.Split(e["Actions"], ";") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue // the key conventionally ends in a semicolon
		}
		group, ok := groups[id]
		if !ok {
			problems = append(problems, fmt.Errorf("action %q is listed in Actions but has no [Desktop Action %s] group, skipped", id, id))
			continue
		}
		name, argv := group["Name"], parseExec(group["Exec"])
		switch {
		case name == "":
			problems = append(problems, fmt.Errorf("action %q has no Name, skipped", id))
		case len(argv) == 0:
			problems = append(problems, fmt.Errorf("action %q has no Exec to run, skipped", id))
		default:
			out = append(out, Action{ID: id, Name: name, Argv: argv})
		}
	}
	return out, problems
}

// parseExec splits an Exec value into argv and drops field codes.
//
// The quoting rules are the spec's, minus the parts nothing uses: a
// double-quoted section is literal except for backslash escapes. Field
// codes are removed wholesale — every one that appears in practice means
// "insert the files the user selected", and a launcher opening an app
// fresh has none. Unknown codes are dropped too, on the same reasoning:
// passing a literal "%i" to a program is never what it wanted.
func parseExec(s string) []string {
	var (
		argv    []string
		cur     strings.Builder
		inQuote bool
		hasCur  bool
	)
	flush := func() {
		if hasCur {
			argv = append(argv, cur.String())
			cur.Reset()
			hasCur = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			hasCur = true // "" is a real, empty argument
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			hasCur = true
		case c == '%' && i+1 < len(s):
			i++
			if s[i] == '%' {
				cur.WriteByte('%')
				hasCur = true
			}
			// any other code: dropped
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
			hasCur = true
		}
	}
	flush()

	// A field code that stood alone ("firefox %U") contributes no argument,
	// because it never sets hasCur -- so "app %U --flag" is two arguments,
	// not three with a gap. Explicit empty quotes ("") still survive, which
	// is the distinction worth keeping.
	return argv
}

// findBinary resolves a command to the path of an executable file through
// PATH. Symlinks are left alone; toApp resolves those, since only Binary
// needs to be comparable with /proc/<pid>/exe.
//
// A command that is not installed is ("", nil): that is an answer, not a
// failure, and it is how TryExec hides an entry. Any other error -- the
// file is there but not executable, say -- is returned, because it means
// something on this system is not the way its package left it.
func findBinary(cmd string) (string, error) {
	if cmd == "" {
		return "", nil
	}
	// LookPath is used even for absolute paths: it checks that the file
	// exists and is executable, which is the whole point of TryExec. An
	// earlier version trusted an absolute path without checking, so
	// TryExec silently hid nothing.
	path, err := exec.LookPath(cmd)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return "", nil // not installed
		}
		return "", err
	}
	return path, nil
}

// sortApps orders by name, case-insensitively, with ID as the tiebreak so
// the result is deterministic across runs.
func sortApps(apps []App) {
	sort.Slice(apps, func(i, j int) bool {
		a, b := strings.ToLower(apps[i].Name), strings.ToLower(apps[j].Name)
		if a != b {
			return a < b
		}
		return apps[i].ID < apps[j].ID
	})
}
