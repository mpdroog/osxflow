package desktop

// Scanning the XDG application directories.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Scan reads every application entry visible to this user, in XDG
// precedence order, and returns them sorted by name.
//
// Errors reading individual files are collected rather than returned:
// one unreadable entry out of 150 should cost the user that entry, not
// the launcher. The caller gets them so they can be logged once at
// startup instead of silently swallowed. Not knowing the home directory
// is one of them: it costs the user's own entries, not the system's.
func Scan() (apps []App, problems []error) {
	dirs, err := DataDirs()
	apps, problems = ScanDirs(dirs)
	if err != nil {
		problems = append(problems, err)
	}
	return apps, problems
}

// ScanDirs is Scan against an explicit directory list, which is what makes
// the whole package testable without touching the real system.
func ScanDirs(dirs []string) (apps []App, problems []error) {
	seen := make(map[string]bool)
	var found []scanned
	for i, dir := range dirs {
		got, errs := scanOne(dir, seen)
		for j := range got {
			got[j].dir = i
		}
		found = append(found, got...)
		problems = append(problems, errs...)
	}
	apps = dedupe(found)
	sortApps(apps)
	return apps, problems
}

// scanned is an app plus where it was found, which dedupe needs and
// callers do not.
type scanned struct {
	app App

	// dir is the index of the XDG directory it came from, and depth is how
	// many subdirectories below that it sat. Together they order two
	// entries that describe the same application.
	dir   int
	depth int
}

// dedupe removes entries that are the same application twice.
//
// XDG shadowing is by desktop file id, and that is not enough on its own:
// /usr/share/applications holds both webapp-manager.desktop and
// kde4/webapp-manager.desktop, whose ids are "webapp-manager.desktop" and
// "kde4-webapp-manager.desktop". Different ids, so neither shadows the
// other, and the launcher shows "Web Apps" twice.
//
// Two entries are the same application when they have the same name and
// run the same command. Anything less strict would merge genuinely
// different entries that happen to share a name -- several "Settings"
// exist, running different programs -- and anything stricter would not
// catch this case.
//
// Of a duplicated pair the shallower one wins, from the
// higher-precedence directory: the top-level entry is the canonical one
// and the nested copy is compatibility packaging.
func dedupe(found []scanned) []App {
	best := make(map[string]int, len(found))
	order := make([]int, 0, len(found))

	for i := range found {
		key := strings.ToLower(found[i].app.Name) + "\x00" + strings.Join(found[i].app.Argv, "\x00")
		prev, ok := best[key]
		if !ok {
			best[key] = i
			order = append(order, i)
			continue
		}
		if less(&found[i], &found[prev]) {
			best[key] = i
		}
	}

	apps := make([]App, 0, len(order))
	for _, i := range order {
		key := strings.ToLower(found[i].app.Name) + "\x00" + strings.Join(found[i].app.Argv, "\x00")
		apps = append(apps, found[best[key]].app)
	}
	return apps
}

func less(a, b *scanned) bool {
	if a.dir != b.dir {
		return a.dir < b.dir
	}
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	return a.app.ID < b.app.ID
}

func scanOne(dir string, seen map[string]bool) (apps []scanned, problems []error) {
	root := filepath.Clean(dir)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A missing applications directory is the normal case, not a
			// problem worth reporting: most systems have only two of the
			// four. Only the directory itself gets that pass. Something
			// missing further down was there a moment ago when its parent
			// was listed -- it vanished mid-walk -- and that is reported.
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			problems = append(problems, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".desktop") {
			return nil
		}

		id, depth, err := desktopID(root, path)
		if err != nil {
			problems = append(problems, err)
			return nil
		}
		// XDG shadowing: an id already claimed by an earlier directory
		// hides this one entirely, even if this file would parse and that
		// one did not.
		if seen[id] {
			return nil
		}
		seen[id] = true

		app, ok, fileProblems, err := Load(path, id)
		if err != nil {
			problems = append(problems, err)
		}
		problems = append(problems, fileProblems...)
		if ok {
			apps = append(apps, scanned{app: app, depth: depth})
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}
	return apps, problems
}

// desktopID builds the id XDG uses for shadowing: the path below the
// applications directory with separators turned into dashes, so
// kde/konsole.desktop becomes kde-konsole.desktop.
//
// It also reports how many directories deep the file sat, which dedupe
// uses to prefer a top-level entry over a nested copy. That count cannot
// be recovered from the id afterwards, because filenames contain dashes
// of their own: "webapp-manager.desktop" is at depth 0 despite its dash.
//
// The walk only hands it paths under root, so Rel cannot fail in practice;
// if it ever does, the file's id is unknowable and so is what it shadows,
// and guessing one could hide a different application.
func desktopID(root, path string) (id string, depth int, err error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", 0, fmt.Errorf("naming %s relative to %s: %w", path, root, err)
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "-"),
		strings.Count(rel, string(filepath.Separator)), nil
}

// DataDirs returns the applications directories in precedence order,
// applying the defaults the XDG basedir spec mandates when the variables
// are unset — which they usually are.
//
// Without XDG_DATA_HOME the user's own directory is found through $HOME.
// If that fails the system directories are still returned, along with the
// error: they are usable, but the list is missing the user's entries and
// the caller should say so.
func DataDirs() ([]string, error) {
	var (
		dirs    []string
		homeErr error
	)
	if home := os.Getenv("XDG_DATA_HOME"); home != "" {
		dirs = append(dirs, filepath.Join(home, "applications"))
	} else {
		h, err := os.UserHomeDir()
		if err != nil {
			homeErr = fmt.Errorf("leaving out the user's own applications: %w", err)
		} else {
			dirs = append(dirs, filepath.Join(h, ".local", "share", "applications"))
		}
	}

	data := os.Getenv("XDG_DATA_DIRS")
	if data == "" {
		data = "/usr/local/share:/usr/share"
	}
	for _, d := range strings.Split(data, ":") {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "applications"))
		}
	}
	return dirs, homeErr
}
