package unpack

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/text/encoding/charmap"
)

// extractNative reads the archive at p with Go's own readers.
func extractNative(p string, format *Format, w *writer) error {
	if format.Container == Zip {
		return extractZipFile(p, w)
	}
	f, err := os.Open(p) //nolint:gosec // the archive is whatever the user opened; reading it is the point
	if err != nil {
		return err
	}
	extractErr := extractStream(f, format, w)
	if closeErr := f.Close(); closeErr != nil {
		log.Printf("closing %s: %v", p, closeErr)
	}
	return extractErr
}

// extractStream decompresses r and reads what comes out as a tar, or as a
// single file named after the stem.
func extractStream(r io.Reader, format *Format, w *writer) error {
	stream, closeStream, err := decompress(r, format.Compression)
	if err != nil {
		return err
	}
	defer closeStream()

	if format.Container == Tar {
		return extractTar(stream, w)
	}
	// foo.gz is often a tar under a name that does not say so.
	br := bufio.NewReaderSize(stream, 4096)
	head, err := br.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return fmt.Errorf("decompressing: %w", err)
	}
	if isTar(head) {
		return extractTar(br, w)
	}
	name, err := clean(path.Base(format.Stem))
	if err != nil {
		return err
	}
	if name == "" {
		return ErrEmpty
	}
	return w.file(name, 0o644, time.Time{}, br)
}

// decompress wraps r in the reader for c. The returned func releases what
// the reader holds and is safe to call once.
func decompress(r io.Reader, c Compression) (io.Reader, func(), error) {
	noop := func() {}
	switch c {
	case None:
		return r, noop, nil
	case Gzip:
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("reading gzip: %w", err)
		}
		return zr, func() {
			if err := zr.Close(); err != nil {
				log.Printf("closing gzip reader: %v", err)
			}
		}, nil
	case Bzip2:
		return bzip2.NewReader(r), noop, nil
	case XZ:
		xr, err := xz.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("reading xz: %w", err)
		}
		return xr, noop, nil
	case Zstd:
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("reading zstd: %w", err)
		}
		return zr, zr.Close, nil
	default:
		return nil, nil, fmt.Errorf("compression %d: %w", c, ErrUnknown)
	}
}

// isTar reports whether a block looks like a tar header: POSIX ustar and
// GNU tar both write "ustar" at offset 257.
func isTar(block []byte) bool {
	return len(block) >= 262 && string(block[257:262]) == "ustar"
}

func extractTar(r io.Reader, w *writer) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		name, err := clean(hdr.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		mode := fs.FileMode(hdr.Mode).Perm() //nolint:gosec // only the permission bits are kept, which fit
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = w.dir(name, mode, hdr.ModTime)
		case tar.TypeReg: // the reader reports the old '\x00' spelling as TypeReg
			err = w.file(name, mode, hdr.ModTime, tr)
		case tar.TypeSymlink:
			err = w.symlink(name, hdr.Linkname)
		case tar.TypeLink:
			err = linkTar(w, name, hdr.Linkname)
		case tar.TypeXGlobalHeader:
			continue
		default:
			log.Printf("skipping %s: tar entry type %q is not a file, directory or link", name, hdr.Typeflag)
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// linkTar makes a hard link to an entry earlier in the same archive. A
// link to something skipped, such as Finder litter, is skipped too.
func linkTar(w *writer, name, linkname string) error {
	target, err := clean(linkname)
	if err != nil {
		return err
	}
	if target == "" {
		return nil
	}
	return w.hardlink(name, target)
}

func extractZipFile(p string, w *writer) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("reading zip: %w", err)
	}
	extractErr := extractZip(&zr.Reader, w)
	if closeErr := zr.Close(); closeErr != nil {
		log.Printf("closing %s: %v", p, closeErr)
	}
	return extractErr
}

func extractZip(zr *zip.Reader, w *writer) error {
	// Checked before writing anything, so a half-encrypted archive fails
	// straight away rather than after extracting the plain half.
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			return ErrEncrypted
		}
	}
	for _, f := range zr.File {
		name, err := clean(zipName(f))
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if err := extractZipEntry(f, name, w); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, name string, w *writer) error {
	mode := f.Mode()
	switch {
	case mode.IsDir():
		return w.dir(name, mode, f.Modified)
	case mode&fs.ModeSymlink != 0:
		target, err := readZipEntry(f, 4096)
		if err != nil {
			return err
		}
		return w.symlink(name, string(target))
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	// A checksum mismatch surfaces as the last read's error, so it fails
	// the write.
	writeErr := w.file(name, mode, f.Modified, rc)
	if closeErr := rc.Close(); closeErr != nil {
		return errors.Join(writeErr, fmt.Errorf("reading %s: %w", name, closeErr))
	}
	return writeErr
}

func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, limit))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Name, errors.Join(readErr, closeErr))
	}
	return data, nil
}

// zipName is the entry's name as UTF-8. A zip that does not flag its
// names as UTF-8 is, from Windows's Explorer and most old tools, in the
// DOS code page 437; a name that is valid UTF-8 anyway is left as it is,
// which covers macOS's and every modern tool's.
func zipName(f *zip.File) string {
	if !f.NonUTF8 || utf8.ValidString(f.Name) {
		return f.Name
	}
	decoded, err := charmap.CodePage437.NewDecoder().String(f.Name)
	if err != nil {
		log.Printf("decoding zip entry name %q as code page 437: %v", f.Name, err)
		return f.Name
	}
	return decoded
}
