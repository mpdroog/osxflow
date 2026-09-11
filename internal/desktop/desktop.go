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
// are not — Desktop Actions, DBusActivatable, startup notification — are
// absent rather than half-present, so a future need for them is a gap to
// fill and not a bug to find.
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

	e, lineProblems, err := parse(f)
	if err != nil {
		return App{}, false, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	app, ok, appProblems := e.toApp(id)
	for _, p := range append(lineProblems, appProblems...) {
		problems = append(problems, fmt.Errorf("%s: %w", path, p))
	}
	return app, ok, problems, nil
}

// parse reads the [Desktop Entry] group and stops at the next group
// header. Later groups are Desktop Actions and similar, which we do not
// implement; reading them into the same map would let an action's Name
// overwrite the application's.
//
// Lines that are not key=value are problems rather than an error: see the
// comment where they are skipped.
func parse(r io.Reader) (e entry, problems []error, err error) {
	e = entry{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	inGroup := false
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if inGroup {
				break // past [Desktop Entry]; the rest is not ours
			}
			inGroup = strings.EqualFold(line, "[Desktop Entry]")
			continue
		}
		if !inGroup {
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
		// and reading 6455 unused strings per scan is not free.
		if strings.ContainsRune(key, '[') {
			continue
		}
		if _, dup := e[key]; dup {
			continue // first occurrence wins, per spec
		}
		e[key] = unescape(strings.TrimSpace(value))
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	return e, problems, nil
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
func (e entry) toApp(id string) (app App, ok bool, problems []error) {
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
	return App{
		ID:             id,
		Name:           name,
		Comment:        e["Comment"],
		Icon:           e["Icon"],
		Argv:           argv,
		Binary:         binary,
		StartupWMClass: e["StartupWMClass"],
		Terminal:       e.bool("Terminal"),
	}, true, problems
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
