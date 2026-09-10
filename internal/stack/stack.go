// Package stack lists the contents of the two folders the dock opens as
// stacks: Downloads and Trash.
//
// Both answer the same question -- what went in here most recently -- but
// they answer it from different places. A download's time is the file's
// own mtime; a trashed file's mtime is whenever it was last edited, which
// may be years before it was thrown away. The deletion time lives in a
// separate .trashinfo file, and reading it is the difference between a
// trash stack that shows what you just deleted and one that shows
// whatever happens to be oldest.
package stack

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one item in a stack.
type Entry struct {
	// Name is what the popup shows.
	Name string

	// Path is what gets opened. For a trashed file this is the copy inside
	// the trash directory, not the original location it came from.
	Path string

	// When orders the list: modification time for a file, deletion time
	// for a trashed one.
	When time.Time

	IsDir bool
}

// DownloadsDir returns the user's download directory, honouring
// XDG_DOWNLOAD_DIR when user-dirs.dirs sets it.
func DownloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if dir := xdgUserDir(home, "XDG_DOWNLOAD_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home, "Downloads")
}

// xdgUserDir reads one entry out of ~/.config/user-dirs.dirs, which is a
// shell fragment of KEY="$HOME/path" lines.
func xdgUserDir(home, key string) string {
	f, err := os.Open(filepath.Join(home, ".config", "user-dirs.dirs")) //nolint:gosec // a fixed path under the user's own config
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only
	return parseUserDirs(f, home, key)
}

// parseUserDirs is the parsing half of xdgUserDir, split out so it can be
// tested and fuzzed against a file this program does not write.
func parseUserDirs(r io.Reader, home, key string) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		value := strings.Trim(strings.TrimPrefix(line, key+"="), `"`)
		// The file is written with $HOME rather than an absolute path, and
		// expanding exactly that one variable is all that is needed: the
		// format explicitly permits only "$HOME/..." or an absolute path.
		if rest, ok := strings.CutPrefix(value, "$HOME"); ok {
			return filepath.Join(home, rest)
		}
		if filepath.IsAbs(value) {
			return value
		}
	}
	return ""
}

// TrashDir returns the user's trash directory per the freedesktop trash
// specification.
func TrashDir() string {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "Trash")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "Trash")
}

// Recent returns the newest entries in a directory, most recent first.
//
// Hidden files are skipped: a dot file in Downloads is nearly always a
// partial download or a browser's bookkeeping, and showing it as the most
// recent item would be actively misleading.
func Recent(dir string, limit int) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished between the listing and the stat
		}
		out = append(out, Entry{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			When:  info.ModTime(),
			IsDir: e.IsDir(),
		})
	}
	return newestFirst(out, limit), nil
}

// TrashEntries returns the most recently deleted files, newest first.
func TrashEntries(trashDir string, limit int) ([]Entry, error) {
	filesDir := filepath.Join(trashDir, "files")
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // an empty trash has never been created
		}
		return nil, fmt.Errorf("reading %s: %w", filesDir, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		entry := Entry{
			Name:  e.Name(),
			Path:  filepath.Join(filesDir, e.Name()),
			IsDir: e.IsDir(),
		}
		if when, ok := deletionTime(trashDir, e.Name()); ok {
			entry.When = when
		} else if info, err := e.Info(); err == nil {
			// No .trashinfo, or an unreadable one. The file's own mtime is
			// a poor substitute for a deletion time, but it keeps the entry
			// in the list rather than dropping a file the user can see in
			// their file manager.
			entry.When = info.ModTime()
		}
		out = append(out, entry)
	}
	return newestFirst(out, limit), nil
}

// TrashCount reports how many items are in the trash, which is all the
// dock needs to choose between the empty and full icons.
func TrashCount(trashDir string) int {
	entries, err := os.ReadDir(filepath.Join(trashDir, "files"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// deletionTime reads DeletionDate out of a .trashinfo file.
func deletionTime(trashDir, name string) (time.Time, bool) {
	f, err := os.Open(filepath.Join(trashDir, "info", name+".trashinfo")) //nolint:gosec // a name read from the trash directory itself
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close() //nolint:errcheck // read-only
	return parseTrashInfo(f)
}

// parseTrashInfo pulls the deletion date out of a .trashinfo file.
//
// This is the one parser here fed by something outside the program: the
// files are written by whichever file manager did the deleting, and a
// truncated or half-written one is entirely possible if the dock reads the
// directory mid-delete.
func parseTrashInfo(r io.Reader) (time.Time, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		value, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "DeletionDate=")
		if !ok {
			continue
		}
		// The spec's format is local time with no zone, which is what
		// time.Local parsing gives.
		when, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(value), time.Local)
		if err != nil {
			return time.Time{}, false
		}
		return when, true
	}
	return time.Time{}, false
}

// newestFirst sorts by time descending and truncates.
func newestFirst(entries []Entry, limit int) []Entry {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].When.Equal(entries[j].When) {
			// A stable tiebreak so a stack of files written in the same
			// second does not reshuffle itself between openings.
			return entries[i].Name < entries[j].Name
		}
		return entries[i].When.After(entries[j].When)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}
