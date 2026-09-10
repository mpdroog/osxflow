package frecency

// Persistence.
//
// The file is small, written rarely, and read once at startup, so the
// format is plain JSON and the whole thing is loaded at once. What matters
// is not speed but that a launcher which is killed mid-write -- which is
// normal, since it exits the moment it launches something -- cannot leave
// a corrupt file behind. Hence the write-and-rename below.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath is where usage is remembered: $XDG_STATE_HOME/osxflow, or
// ~/.local/state/osxflow when that is unset.
//
// State, not config and not cache: it is written by the program rather
// than by the user, but losing it costs the ranking rather than nothing.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "osxflow", "frecency.json"), nil
}

// Load reads the store at path.
//
// A missing file is not an error -- it is what the first run looks like.
// A corrupt one is reported, but an empty store is still returned, so the
// launcher opens with no ranking memory rather than not opening.
func Load(path string) (*Store, error) {
	s := New(path)

	data, err := os.ReadFile(path) //nolint:gosec // path is ours, from DefaultPath or a flag
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return New(path), fmt.Errorf("parsing %s: %w", path, err)
	}
	// Unmarshalling over the zero value leaves nil maps when the file has
	// no such key; every caller then writes to a nil map and panics.
	if s.Apps == nil {
		s.Apps = map[string]Entry{}
	}
	if s.Queries == nil {
		s.Queries = map[string]string{}
	}
	s.path = path
	return s, nil
}

// Save writes the store back, pruning entries that have decayed away.
//
// The write goes to a temporary file in the same directory and is renamed
// over the target, because rename within a filesystem is atomic: a reader
// sees either the old file or the new one, never a half-written one. The
// same directory matters -- a rename across filesystems is not atomic and
// fails outright.
func (s *Store) Save(now time.Time) error {
	if s.path == "" {
		return errors.New("no path to save to")
	}
	s.prune(now)

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the store: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".frecency-*.json")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Any failure from here on leaves the temporary file behind, so every
	// path removes it. Remove after a successful rename is harmless: the
	// name no longer exists.
	defer os.Remove(tmpName) //nolint:errcheck // best effort cleanup

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // already failing
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}

// Ranker binds a store to a moment in time, so that every comparison in
// one ranking pass uses the same clock. Without it a long sort could
// score two applications microseconds apart and produce an ordering that
// is not internally consistent.
type Ranker struct {
	store *Store
	now   time.Time
}

// At returns a Ranker for this store at now.
func (s *Store) At(now time.Time) *Ranker { return &Ranker{store: s, now: now} }

// Score implements the ranking hook.
func (r *Ranker) Score(appID string) float64 {
	if r == nil || r.store == nil {
		return 0
	}
	return r.store.Score(appID, r.now)
}

// Pinned implements the ranking hook.
func (r *Ranker) Pinned(query string) string {
	if r == nil || r.store == nil {
		return ""
	}
	return r.store.Pinned(query)
}
