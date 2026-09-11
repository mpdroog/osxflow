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
	"errors"
	"fmt"
	"io"
	"io/fs"
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
//
// Like scale.Detect, the path is usable even alongside an error, as long
// as it is not empty: an unreadable user-dirs.dirs costs the user's
// configured folder, not the stack, so ~/Downloads comes back with the
// error that says why it may be the wrong one. Only a home directory that
// cannot be found leaves nothing to fall back to.
func DownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the downloads folder: %w", err)
	}
	fallback := filepath.Join(home, "Downloads")
	dir, err := xdgUserDir(home, "XDG_DOWNLOAD_DIR")
	if err != nil {
		return fallback, err
	}
	if dir != "" {
		return dir, nil
	}
	return fallback, nil
}

// xdgUserDir reads one entry out of ~/.config/user-dirs.dirs, which is a
// shell fragment of KEY="$HOME/path" lines.
//
// A file that does not exist is the ordinary case on a desktop that never
// ran xdg-user-dirs-update, and comes back as "" with no error.
func xdgUserDir(home, key string) (dir string, err error) {
	name := filepath.Join(home, ".config", "user-dirs.dirs")
	f, err := os.Open(name) //nolint:gosec // a fixed path under the user's own config
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing %s: %w", name, closeErr))
		}
	}()
	dir, err = parseUserDirs(f, home, key)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	return dir, nil
}

// parseUserDirs is the parsing half of xdgUserDir, split out so it can be
// tested and fuzzed against a file this program does not write.
//
// A key that is not there is "" with no error; a read that stopped short
// is an error, because "the line was not found" and "the file could not be
// read as far as the line" must not look the same.
func parseUserDirs(r io.Reader, home, key string) (string, error) {
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
			return filepath.Join(home, rest), nil
		}
		if filepath.IsAbs(value) {
			return value, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// TrashDir returns the user's trash directory per the freedesktop trash
// specification.
func TrashDir() (string, error) {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "Trash"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the trash: %w", err)
	}
	return filepath.Join(home, ".local", "share", "Trash"), nil
}

// Recent returns the newest entries in a directory, most recent first.
//
// Hidden files are skipped: a dot file in Downloads is nearly always a
// partial download or a browser's bookkeeping, and showing it as the most
// recent item would be actively misleading.
//
// An entry that could not be examined is left out and reported in err,
// alongside every entry that could: one unreadable file is no reason to
// show none of the others.
func Recent(dir string, limit int) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	out := make([]Entry, 0, len(entries))
	var problems []error
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// Vanished between the listing and the stat: the file is simply
			// no longer there to show.
			if !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, fmt.Errorf("examining %s: %w", filepath.Join(dir, e.Name()), err))
			}
			continue
		}
		out = append(out, Entry{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			When:  info.ModTime(),
			IsDir: e.IsDir(),
		})
	}
	return newestFirst(out, limit), errors.Join(problems...)
}

// TrashEntries returns the most recently deleted files, newest first.
//
// As with Recent, the entries it could read come back alongside an error
// describing the ones it could not.
func TrashEntries(trashDir string, limit int) ([]Entry, error) {
	filesDir := filepath.Join(trashDir, "files")
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // an empty trash has never been created
		}
		return nil, fmt.Errorf("reading %s: %w", filesDir, err)
	}
	out := make([]Entry, 0, len(entries))
	var problems []error
	for _, e := range entries {
		entry := Entry{
			Name:  e.Name(),
			Path:  filepath.Join(filesDir, e.Name()),
			IsDir: e.IsDir(),
		}
		when, err := deletionTime(trashDir, e.Name())
		if err == nil {
			entry.When = when
			out = append(out, entry)
			continue
		}
		// No .trashinfo is something other file managers do leave behind,
		// and is not worth a word; one that is there and unreadable is.
		// Either way the file's own mtime stands in for the deletion time:
		// a poor substitute, but it keeps the entry in the list rather
		// than dropping a file the user can see in their file manager.
		if !errors.Is(err, errNoDeletionDate) {
			problems = append(problems, err)
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, fmt.Errorf("examining %s: %w", entry.Path, err))
			}
			// Gone since the listing: emptied or restored while we looked.
			continue
		}
		entry.When = info.ModTime()
		out = append(out, entry)
	}
	return newestFirst(out, limit), errors.Join(problems...)
}

// TrashCount reports how many items are in the trash, which is all the
// dock needs to choose between the empty and full icons. A trash that was
// never created holds nothing and is not an error.
func TrashCount(trashDir string) (int, error) {
	filesDir := filepath.Join(trashDir, "files")
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", filesDir, err)
	}
	return len(entries), nil
}

// errNoDeletionDate reports that a trashed file has no recorded deletion
// time: its .trashinfo does not exist, or does not say. Both are left
// behind by file managers that follow the specification loosely, and
// neither is a fault anybody can act on.
var errNoDeletionDate = errors.New("no DeletionDate")

// deletionTime reads DeletionDate out of a .trashinfo file.
func deletionTime(trashDir, name string) (when time.Time, err error) {
	infoPath := filepath.Join(trashDir, "info", name+".trashinfo")
	f, err := os.Open(infoPath) //nolint:gosec // a name read from the trash directory itself
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, fmt.Errorf("%s: %w", infoPath, errNoDeletionDate)
		}
		return time.Time{}, fmt.Errorf("reading %s: %w", infoPath, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing %s: %w", infoPath, closeErr))
		}
	}()
	when, err = parseTrashInfo(f)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", infoPath, err)
	}
	return when, nil
}

// parseTrashInfo pulls the deletion date out of a .trashinfo file.
//
// This is the one parser here fed by something outside the program: the
// files are written by whichever file manager did the deleting, and a
// truncated or half-written one is entirely possible if the dock reads the
// directory mid-delete. A file without the key is errNoDeletionDate; a key
// whose value is not a date, or a file that could not be read to the end,
// is an error of its own.
func parseTrashInfo(r io.Reader) (time.Time, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		value, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "DeletionDate=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		// The spec's format is local time with no zone, which is what
		// time.Local parsing gives.
		when, err := time.ParseInLocation("2006-01-02T15:04:05", value, time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("DeletionDate %q: %w", value, err)
		}
		return when, nil
	}
	if err := sc.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, errNoDeletionDate
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
