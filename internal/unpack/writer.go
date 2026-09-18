package unpack

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnsafePath is returned for an entry whose name would land outside the
// directory being extracted into: an absolute path, or one climbing out
// with "..". An archive carrying one is refused whole.
var ErrUnsafePath = errors.New("entry escapes the extraction directory")

// writer creates entries inside an os.Root, which is what keeps them
// there: every operation resolves the name inside the root, following
// symbolic links only while they stay inside it. The name checks in
// clean are for a clear error, not the safety.
type writer struct {
	root *os.Root
	// dirTimes are applied last: writing a file into a directory changes
	// the directory's own modification time.
	dirTimes []dirTime
}

type dirTime struct {
	name string
	t    time.Time
}

func newWriter(root *os.Root) *writer { return &writer{root: root} }

// clean turns an entry name as stored into a path relative to the root.
// It returns "" for an entry that should be skipped rather than written:
// the root itself, and the Finder litter macOS puts into archives.
func clean(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%q: %w", name, ErrUnsafePath)
	}
	name = path.Clean(name)
	if name == "." {
		return "", nil
	}
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("%q: %w", name, ErrUnsafePath)
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "__MACOSX" || part == ".DS_Store" {
			return "", nil
		}
	}
	return name, nil
}

// filePerm keeps an entry's permission bits, minus setuid and friends,
// and always lets the owner read and write what they now own.
func filePerm(mode fs.FileMode) fs.FileMode { return mode.Perm()&0o755 | 0o600 }

// dirPerm is filePerm for directories, which the owner must also be able
// to enter.
func dirPerm(mode fs.FileMode) fs.FileMode { return mode.Perm()&0o755 | 0o700 }

func (w *writer) mkdirParents(name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	if err := w.root.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	return nil
}

func (w *writer) dir(name string, mode fs.FileMode, mtime time.Time) error {
	if err := w.root.MkdirAll(name, dirPerm(mode)); err != nil {
		return fmt.Errorf("creating %s: %w", name, err)
	}
	// MkdirAll leaves an existing directory's mode alone, and an entry
	// for a directory can come after the files in it.
	if err := w.root.Chmod(name, dirPerm(mode)); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", name, err)
	}
	if !mtime.IsZero() {
		w.dirTimes = append(w.dirTimes, dirTime{name: name, t: mtime})
	}
	return nil
}

func (w *writer) file(name string, mode fs.FileMode, mtime time.Time, r io.Reader) error {
	if err := w.mkdirParents(name); err != nil {
		return err
	}
	// A name given twice -- tar appends newer copies -- replaces the
	// earlier one. Removing first also means a symbolic link left there by
	// an earlier entry is replaced rather than written through.
	if err := w.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replacing %s: %w", name, err)
	}
	f, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm(mode))
	if err != nil {
		return fmt.Errorf("creating %s: %w", name, err)
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", name, copyErr), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("writing %s: %w", name, closeErr)
	}
	// OpenFile's mode passes through the umask; the archive's does not.
	if err := w.root.Chmod(name, filePerm(mode)); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", name, err)
	}
	if !mtime.IsZero() {
		if err := w.root.Chtimes(name, mtime, mtime); err != nil {
			return fmt.Errorf("setting time on %s: %w", name, err)
		}
	}
	return nil
}

func (w *writer) symlink(name, target string) error {
	if err := w.mkdirParents(name); err != nil {
		return err
	}
	if err := w.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replacing %s: %w", name, err)
	}
	if err := w.root.Symlink(target, name); err != nil {
		return fmt.Errorf("creating link %s: %w", name, err)
	}
	return nil
}

func (w *writer) hardlink(name, target string) error {
	if err := w.mkdirParents(name); err != nil {
		return err
	}
	if err := w.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replacing %s: %w", name, err)
	}
	if err := w.root.Link(target, name); err != nil {
		return fmt.Errorf("creating link %s: %w", name, err)
	}
	return nil
}

// finish sets directory times, now that nothing more is written into
// them.
func (w *writer) finish() error {
	for _, d := range w.dirTimes {
		if err := w.root.Chtimes(d.name, d.t, d.t); err != nil {
			return fmt.Errorf("setting time on %s: %w", d.name, err)
		}
	}
	return nil
}
