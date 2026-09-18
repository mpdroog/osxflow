package unpack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrEncrypted is returned for a password-protected archive, which this
// package does not ask for a password for.
var ErrEncrypted = errors.New("archive is password-protected")

// ErrEmpty is returned for an archive with nothing in it worth extracting.
var ErrEmpty = errors.New("archive is empty")

// ErrUnknown is returned for a file whose name is not one Detect knows.
var ErrUnknown = errors.New("not a known archive type")

// Unpack extracts the archive at path into its own directory and, once
// everything is in place, removes the archive. It returns where the
// result went: the one top-level entry, or the folder holding several.
//
// Extraction goes into a hidden scratch directory beside the archive
// first, so a failure part way leaves nothing behind but the archive
// itself, untouched.
func Unpack(ctx context.Context, path string) (string, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	format, ok := Detect(name)
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrUnknown)
	}

	scratch, err := os.MkdirTemp(dir, ".unpack-*")
	if err != nil {
		return "", fmt.Errorf("making a scratch directory: %w", err)
	}
	dest, err := extractAndPlace(ctx, path, dir, scratch, &format)
	if err != nil {
		if rmErr := os.RemoveAll(scratch); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("removing scratch directory: %w", rmErr))
		}
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return dest, fmt.Errorf("unpacked to %s, but removing the archive: %w", dest, err)
	}
	return dest, nil
}

func extractAndPlace(ctx context.Context, path, dir, scratch string, format *Format) (string, error) {
	if err := extract(ctx, path, scratch, format); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return "", fmt.Errorf("reading what was extracted: %w", err)
	}
	switch len(entries) {
	case 0:
		return "", ErrEmpty
	case 1:
		// The scratch directory is left for the caller to remove only on
		// failure; on success it is empty and removed here.
		dest, err := place(filepath.Join(scratch, entries[0].Name()), dir, entries[0].Name(), entries[0].IsDir())
		if err != nil {
			return "", err
		}
		if err := os.Remove(scratch); err != nil {
			log.Printf("removing scratch directory %s: %v", scratch, err)
		}
		return dest, nil
	default:
		// MkdirTemp makes it 0700; a folder the user gets should look like
		// any other they made.
		if err := os.Chmod(scratch, 0o755); err != nil { //nolint:gosec // an ordinary folder in the user's own directory
			return "", fmt.Errorf("setting folder permissions: %w", err)
		}
		return place(scratch, dir, filepath.Base(format.Stem), true)
	}
}

// extract fills scratch with the archive's entries.
func extract(ctx context.Context, path, scratch string, format *Format) error {
	if format.Container == External {
		return extractExternal(ctx, path, scratch)
	}
	root, err := os.OpenRoot(scratch)
	if err != nil {
		return fmt.Errorf("opening scratch directory: %w", err)
	}
	w := newWriter(root)
	extractErr := extractNative(path, format, w)
	if extractErr == nil {
		extractErr = w.finish()
	}
	if closeErr := root.Close(); closeErr != nil {
		extractErr = errors.Join(extractErr, fmt.Errorf("closing scratch directory: %w", closeErr))
	}
	return extractErr
}

// place moves src into dir under name, or "name 2", "name 3" and so on
// when that is taken, as Finder does. It never replaces anything: the
// rename refuses rather than overwrite, so a file that appears between
// looking and moving is not lost either.
func place(src, dir, name string, isDir bool) (string, error) {
	base, ext := name, ""
	if !isDir {
		base, ext = splitExt(name)
	}
	for n := 1; n < 10000; n++ {
		candidate := name
		if n > 1 {
			candidate = base + " " + strconv.Itoa(n) + ext
		}
		dest := filepath.Join(dir, candidate)
		err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
			// Not every filesystem has RENAME_NOREPLACE: ecryptfs, which
			// holds an encrypted home directory, does not.
			err = moveNoReplace(src, dest, isDir)
		}
		switch {
		case err == nil:
			return dest, nil
		case errors.Is(err, unix.EEXIST):
			continue
		default:
			return "", fmt.Errorf("moving into place as %s: %w", dest, err)
		}
	}
	return "", fmt.Errorf("no free name for %s in %s", name, dir)
}

// moveNoReplace is RENAME_NOREPLACE built from calls every filesystem
// has. The name is claimed first with a call that fails if it is taken --
// mkdir for a directory, link for anything else -- and only then is src
// moved onto the claim, so nothing that was already there is replaced.
func moveNoReplace(src, dest string, isDir bool) error {
	if !isDir {
		if err := os.Link(src, dest); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("removing %s after linking it into place: %w", src, err)
		}
		return nil
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	// Renaming a directory onto an empty one replaces it; the empty one
	// is the claim just made. os.Rename refuses any existing directory,
	// so this is the system call itself.
	if err := unix.Rename(src, dest); err != nil {
		if rmErr := os.Remove(dest); rmErr != nil {
			return errors.Join(err, fmt.Errorf("removing the claimed name %s: %w", dest, rmErr))
		}
		return err
	}
	return nil
}

// splitExt splits a file name for numbering, so "report.pdf" becomes
// "report 2.pdf". A dotfile keeps its name whole: ".bashrc 2".
func splitExt(name string) (base, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return name, ""
	}
	return name[:i], name[i:]
}
