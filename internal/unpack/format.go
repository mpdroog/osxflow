// Package unpack extracts an archive next to itself, the way macOS's
// Archive Utility does when an archive is double-clicked: one top-level
// item lands beside the archive as it is, several go into a folder named
// after it, nothing is ever overwritten, and nothing lands anywhere until
// the whole archive has extracted cleanly.
package unpack

import (
	"strings"
)

// Container is how the entries are laid out once decompressed.
type Container int

const (
	// Single is one compressed file with no container: foo.txt.gz.
	// A stream that turns out to hold a tar is read as one anyway.
	Single Container = iota
	Tar
	Zip
	// External is anything handed to 7-Zip: 7z and rar.
	External
)

// Compression is the stream the container is wrapped in.
type Compression int

const (
	None Compression = iota
	Gzip
	Bzip2
	XZ
	Zstd
)

// Format is what a file name says an archive is.
type Format struct {
	Container   Container
	Compression Compression
	// Stem is the name without its archive extension: what a folder
	// holding several top-level entries is called, and what a single
	// decompressed file is called.
	Stem string
}

// suffixes are matched longest first, so .tar.gz wins over .gz.
var suffixes = []struct {
	ext string
	Format
}{
	{".tar.gz", Format{Container: Tar, Compression: Gzip}},
	{".tar.bz2", Format{Container: Tar, Compression: Bzip2}},
	{".tar.xz", Format{Container: Tar, Compression: XZ}},
	{".tar.zst", Format{Container: Tar, Compression: Zstd}},
	{".tgz", Format{Container: Tar, Compression: Gzip}},
	{".taz", Format{Container: Tar, Compression: Gzip}},
	{".tbz2", Format{Container: Tar, Compression: Bzip2}},
	{".tbz", Format{Container: Tar, Compression: Bzip2}},
	{".txz", Format{Container: Tar, Compression: XZ}},
	{".tzst", Format{Container: Tar, Compression: Zstd}},
	{".tar", Format{Container: Tar, Compression: None}},
	{".zip", Format{Container: Zip, Compression: None}},
	{".gz", Format{Container: Single, Compression: Gzip}},
	{".bz2", Format{Container: Single, Compression: Bzip2}},
	{".xz", Format{Container: Single, Compression: XZ}},
	{".zst", Format{Container: Single, Compression: Zstd}},
	{".7z", Format{Container: External, Compression: None}},
	{".rar", Format{Container: External, Compression: None}},
}

// Detect names the format of an archive from its file name alone. It never
// looks inside: a .docx or an .epub is a zip too, and must not be taken
// apart (and deleted) because a file manager counted it as one.
func Detect(name string) (Format, bool) {
	lower := strings.ToLower(name)
	for i := range suffixes {
		s := &suffixes[i]
		if len(lower) > len(s.ext) && strings.HasSuffix(lower, s.ext) {
			f := s.Format
			f.Stem = name[:len(name)-len(s.ext)]
			return f, true
		}
	}
	return Format{}, false
}
