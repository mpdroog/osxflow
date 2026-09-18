package unpack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

type entry struct {
	name   string
	body   string
	mode   fs.FileMode
	link   string // symlink target
	hard   string // hard link target (tar only)
	isDir  bool
	cp437  bool // zip only: store the name without the UTF-8 flag
	secret bool // zip only: set the encryption flag
}

func file(name, body string) entry { return entry{name: name, body: body, mode: 0o644} }

func makeTar(t testing.TB, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: int64(e.mode), ModTime: mtime, Format: tar.FormatPAX}
		switch {
		case e.isDir:
			hdr.Typeflag = tar.TypeDir
		case e.link != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.link
		case e.hard != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeLink, e.hard
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t testing.TB, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
		body := e.body
		switch {
		case e.isDir:
			hdr.SetMode(fs.ModeDir | e.mode)
		case e.link != "":
			hdr.SetMode(fs.ModeSymlink | 0o777)
			body = e.link
		default:
			hdr.SetMode(e.mode)
		}
		if e.cp437 {
			hdr.NonUTF8 = true
		}
		if e.secret {
			hdr.Flags |= 0x1
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if !e.isDir {
			if _, err := io.WriteString(w, body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func xzip(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zst(t *testing.T, data []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := w.EncodeAll(data, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// put writes an archive into a fresh directory and returns its path.
func put(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// tree lists every path under dir, directories with a trailing slash and
// files with their contents, symbolic links as "-> target".
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		switch {
		case rel == ".":
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			got[rel] = "-> " + target
		case d.IsDir():
			got[rel+"/"] = ""
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			got[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func sameTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	keys := func(m map[string]string) []string {
		k := make([]string, 0, len(m))
		for key := range m {
			k = append(k, key)
		}
		slices.Sort(k)
		return k
	}
	if !slices.Equal(keys(got), keys(want)) {
		t.Fatalf("tree = %v, want %v", keys(got), keys(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestUnpackLayouts(t *testing.T) {
	twoFiles := []entry{file("a.txt", "A"), file("b.txt", "B")}
	oneFolder := []entry{{name: "proj/", isDir: true, mode: 0o755}, file("proj/main.go", "package main"), file("proj/sub/x", "X")}
	tests := []struct {
		name    string
		archive string
		data    func(t *testing.T) []byte
		want    map[string]string
	}{
		{"zip with several top-level files gets a folder", "photos.zip",
			func(t *testing.T) []byte { return makeZip(t, twoFiles) },
			map[string]string{"photos/": "", "photos/a.txt": "A", "photos/b.txt": "B"}},
		{"zip with one folder lands as it is", "download.zip",
			func(t *testing.T) []byte { return makeZip(t, oneFolder) },
			map[string]string{"proj/": "", "proj/main.go": "package main", "proj/sub/": "", "proj/sub/x": "X"}},
		{"tar.gz with one folder", "release.tar.gz",
			func(t *testing.T) []byte { return gz(t, makeTar(t, oneFolder)) },
			map[string]string{"proj/": "", "proj/main.go": "package main", "proj/sub/": "", "proj/sub/x": "X"}},
		{"tgz with several files", "release.tgz",
			func(t *testing.T) []byte { return gz(t, makeTar(t, twoFiles)) },
			map[string]string{"release/": "", "release/a.txt": "A", "release/b.txt": "B"}},
		{"plain tar", "stuff.TAR",
			func(t *testing.T) []byte { return makeTar(t, twoFiles) },
			map[string]string{"stuff/": "", "stuff/a.txt": "A", "stuff/b.txt": "B"}},
		{"tar.xz", "x.tar.xz",
			func(t *testing.T) []byte { return xzip(t, makeTar(t, twoFiles)) },
			map[string]string{"x/": "", "x/a.txt": "A", "x/b.txt": "B"}},
		{"tar.zst", "z.tar.zst",
			func(t *testing.T) []byte { return zst(t, makeTar(t, twoFiles)) },
			map[string]string{"z/": "", "z/a.txt": "A", "z/b.txt": "B"}},
		{"gz of a single file", "notes.txt.gz",
			func(t *testing.T) []byte { return gz(t, []byte("my notes")) },
			map[string]string{"notes.txt": "my notes"}},
		{"xz of a single file", "notes.txt.xz",
			func(t *testing.T) []byte { return xzip(t, []byte("my notes")) },
			map[string]string{"notes.txt": "my notes"}},
		{"zst of a single file", "notes.txt.zst",
			func(t *testing.T) []byte { return zst(t, []byte("my notes")) },
			map[string]string{"notes.txt": "my notes"}},
		{"gz that is really a tar", "backup.gz",
			func(t *testing.T) []byte { return gz(t, makeTar(t, twoFiles)) },
			map[string]string{"backup/": "", "backup/a.txt": "A", "backup/b.txt": "B"}},
		{"finder litter is left out", "mac.zip",
			func(t *testing.T) []byte {
				return makeZip(t, []entry{file("doc.pdf", "PDF"), file("__MACOSX/._doc.pdf", "junk"), file(".DS_Store", "junk")})
			},
			map[string]string{"doc.pdf": "PDF"}},
		{"code page 437 names are decoded", "dos.zip",
			func(t *testing.T) []byte {
				return makeZip(t, []entry{{name: "caf\x82.txt", body: "C", mode: 0o644, cp437: true}})
			},
			map[string]string{"café.txt": "C"}},
		{"links inside the archive", "links.tar",
			func(t *testing.T) []byte {
				return makeTar(t, []entry{file("d/real", "R"), {name: "d/sym", link: "real"}, {name: "d/hard", hard: "d/real"}})
			},
			map[string]string{"d/": "", "d/real": "R", "d/sym": "-> real", "d/hard": "R"}},
		{"zip symlink", "zl.zip",
			func(t *testing.T) []byte {
				return makeZip(t, []entry{file("d/real", "R"), {name: "d/sym", link: "real"}})
			},
			map[string]string{"d/": "", "d/real": "R", "d/sym": "-> real"}},
		{"later tar copy of a name wins", "dup.tar",
			func(t *testing.T) []byte { return makeTar(t, []entry{file("f", "old"), file("f", "new")}) },
			map[string]string{"f": "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := put(t, tt.archive, tt.data(t))
			dir := filepath.Dir(p)
			dest, err := Unpack(t.Context(), p)
			if err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if filepath.Dir(dest) != dir {
				t.Errorf("dest %s is not in %s", dest, dir)
			}
			sameTree(t, tree(t, dir), tt.want)
		})
	}
}

func TestUnpackFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    map[string]string
	}{
		{"two.tar.bz2", map[string]string{"two/": "", "two/b.txt": "world\n", "two/sub/": "", "two/sub/a.txt": "hello\n"}},
		{"solo.txt.bz2", map[string]string{"solo.txt": "solo\n"}},
		{"pair.7z", map[string]string{"pair/": "", "pair/a.txt": "hello\n", "pair/b.txt": "world\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if filepath.Ext(tt.fixture) == ".7z" {
				if _, err := sevenZip(); err != nil {
					t.Skip(err)
				}
			}
			data, err := os.ReadFile(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			p := put(t, tt.fixture, data)
			if _, err := Unpack(t.Context(), p); err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			sameTree(t, tree(t, filepath.Dir(p)), tt.want)
		})
	}
}

// Failures leave the archive where it was and nothing else behind.
func TestUnpackFailuresLeaveArchive(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		data    func(t *testing.T) []byte
		want    error
	}{
		{"dot-dot", "evil.tar", func(t *testing.T) []byte {
			return makeTar(t, []entry{file("ok", "fine"), file("../../escaped", "pwned")})
		}, ErrUnsafePath},
		{"absolute", "evil.zip", func(t *testing.T) []byte {
			return makeZip(t, []entry{file("/tmp/escaped", "pwned")})
		}, ErrUnsafePath},
		{"backslash dot-dot", "win.zip", func(t *testing.T) []byte {
			return makeZip(t, []entry{file("..\\escaped", "pwned")})
		}, ErrUnsafePath},
		{"hard link out", "hard.tar", func(t *testing.T) []byte {
			return makeTar(t, []entry{{name: "h", hard: "../../../etc/passwd"}})
		}, ErrUnsafePath},
		{"write through a symlink out", "sym.tar", func(t *testing.T) []byte {
			return makeTar(t, []entry{{name: "d", link: "/tmp"}, file("d/escaped", "pwned")})
		}, nil},
		{"encrypted zip", "secret.zip", func(t *testing.T) []byte {
			return makeZip(t, []entry{file("plain", "P"), {name: "locked", body: "L", mode: 0o644, secret: true}})
		}, ErrEncrypted},
		{"empty zip", "empty.zip", func(t *testing.T) []byte { return makeZip(t, nil) }, ErrEmpty},
		{"only litter", "litter.zip", func(t *testing.T) []byte {
			return makeZip(t, []entry{file("__MACOSX/x", "junk")})
		}, ErrEmpty},
		{"truncated gzip", "cut.tar.gz", func(t *testing.T) []byte {
			b := gz(t, makeTar(t, []entry{file("a", string(bytes.Repeat([]byte("x"), 10000)))}))
			return b[:len(b)/2]
		}, nil},
		{"not a zip at all", "fake.zip", func(*testing.T) []byte { return []byte("hello") }, nil},
		{"not an archive", "report.docx", func(t *testing.T) []byte { return makeZip(t, []entry{file("word/document.xml", "")}) }, ErrUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outer := t.TempDir()
			dir := filepath.Join(outer, "in")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			data := tt.data(t)
			p := filepath.Join(dir, tt.archive)
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Unpack(t.Context(), p)
			if err == nil {
				t.Fatal("Unpack succeeded, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Unpack = %v, want %v", err, tt.want)
			}
			sameTree(t, tree(t, outer), map[string]string{"in/": "", "in/" + tt.archive: string(data)})
		})
	}
}

func TestUnpackEncrypted7z(t *testing.T) {
	if _, err := sevenZip(); err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile("testdata/locked.7z")
	if err != nil {
		t.Fatal(err)
	}
	p := put(t, "locked.7z", data)
	if _, err := Unpack(t.Context(), p); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("Unpack = %v, want ErrEncrypted", err)
	}
	sameTree(t, tree(t, filepath.Dir(p)), map[string]string{"locked.7z": string(data)})
}

func TestUnpackNeverOverwrites(t *testing.T) {
	p := put(t, "photos.zip", makeZip(t, []entry{file("a.txt", "new A"), file("b.txt", "new B")}))
	dir := filepath.Dir(p)
	for name, body := range map[string]string{"photos/keep": "mine", "photos 2": "a file in the way"} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dest, err := Unpack(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "photos 3"); dest != want {
		t.Errorf("dest = %s, want %s", dest, want)
	}

	single := filepath.Join(dir, "report.pdf.gz")
	if err = os.WriteFile(single, gz(t, []byte("new pdf")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("old pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dest, err = Unpack(t.Context(), single); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "report 2.pdf"); dest != want {
		t.Errorf("dest = %s, want %s", dest, want)
	}
	sameTree(t, tree(t, dir), map[string]string{
		"photos/": "", "photos/keep": "mine",
		"photos 2":  "a file in the way",
		"photos 3/": "", "photos 3/a.txt": "new A", "photos 3/b.txt": "new B",
		"report.pdf": "old pdf", "report 2.pdf": "new pdf",
	})
}

func TestUnpackKeepsModesAndTimes(t *testing.T) {
	p := put(t, "bin.tar", makeTar(t, []entry{
		{name: "tool/", isDir: true, mode: 0o755},
		{name: "tool/run", body: "#!/bin/sh", mode: 0o4755},
		{name: "tool/private", body: "k", mode: 0o000},
	}))
	dest, err := Unpack(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for name, mode := range map[string]fs.FileMode{"run": 0o755, "private": 0o600, ".": fs.ModeDir | 0o755} {
		fi, err := os.Lstat(filepath.Join(dest, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != mode {
			t.Errorf("%s mode = %v, want %v", name, fi.Mode(), mode)
		}
		if !fi.ModTime().Equal(want) {
			t.Errorf("%s mtime = %v, want %v", name, fi.ModTime(), want)
		}
	}
}

func TestPlaceNamesDirectoriesWhole(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"v1.2", "src"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "src", "v1.2"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest, err := place(filepath.Join(dir, "src", "v1.2"), dir, "v1.2", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "v1.2 2"); dest != want {
		t.Errorf("dest = %s, want %s", dest, want)
	}
}

// moveNoReplace is what place falls back to on ecryptfs; the test
// filesystem has RENAME_NOREPLACE, so it is exercised directly.
func TestMoveNoReplace(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/file", "new")
	write("src/folder/inner", "new inner")
	write("taken", "old")
	write("taken dir/keep", "old inner")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	j := func(name string) string { return filepath.Join(dir, name) }

	if err := moveNoReplace(j("src/file"), j("taken"), false); !errors.Is(err, fs.ErrExist) {
		t.Errorf("file onto a file = %v, want ErrExist", err)
	}
	if err := moveNoReplace(j("src/folder"), j("taken dir"), true); !errors.Is(err, fs.ErrExist) {
		t.Errorf("folder onto a folder = %v, want ErrExist", err)
	}
	if err := moveNoReplace(j("src/folder"), j("empty"), true); !errors.Is(err, fs.ErrExist) {
		t.Errorf("folder onto an empty folder = %v, want ErrExist", err)
	}
	if err := moveNoReplace(j("src/file"), j("file"), false); err != nil {
		t.Errorf("file to a free name: %v", err)
	}
	if err := moveNoReplace(j("src/folder"), j("folder"), true); err != nil {
		t.Errorf("folder to a free name: %v", err)
	}
	sameTree(t, tree(t, dir), map[string]string{
		"src/": "", "empty/": "",
		"taken": "old", "taken dir/": "", "taken dir/keep": "old inner",
		"file": "new", "folder/": "", "folder/inner": "new inner",
	})
}

func TestSplitExt(t *testing.T) {
	tests := []struct{ in, base, ext string }{
		{"report.pdf", "report", ".pdf"},
		{"archive.tar", "archive", ".tar"},
		{".bashrc", ".bashrc", ""},
		{"README", "README", ""},
		{"trailing.", "trailing.", ""},
	}
	for _, tt := range tests {
		base, ext := splitExt(tt.in)
		if base != tt.base || ext != tt.ext {
			t.Errorf("splitExt(%q) = %q, %q; want %q, %q", tt.in, base, ext, tt.base, tt.ext)
		}
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
		want Format
	}{
		{"a.tar.gz", true, Format{Tar, Gzip, "a"}},
		{"A.TGZ", true, Format{Tar, Gzip, "A"}},
		{"v1.2.3.tar.bz2", true, Format{Tar, Bzip2, "v1.2.3"}},
		{"x.tbz2", true, Format{Tar, Bzip2, "x"}},
		{"x.tar.xz", true, Format{Tar, XZ, "x"}},
		{"x.tar.zst", true, Format{Tar, Zstd, "x"}},
		{"x.tar", true, Format{Tar, None, "x"}},
		{"Photos.zip", true, Format{Zip, None, "Photos"}},
		{"log.1.gz", true, Format{Single, Gzip, "log.1"}},
		{"a.7z", true, Format{External, None, "a"}},
		{"a.rar", true, Format{External, None, "a"}},
		{".zip", false, Format{}},
		{"report.docx", false, Format{}},
		{"book.epub", false, Format{}},
		{"app.jar", false, Format{}},
		{"zip", false, Format{}},
	}
	for _, tt := range tests {
		got, ok := Detect(tt.name)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Detect(%q) = %+v, %v; want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestClean(t *testing.T) {
	tests := []struct {
		in, want string
		unsafe   bool
	}{
		{"a/b.txt", "a/b.txt", false},
		{"./a/./b/", "a/b", false},
		{"a/../b", "b", false},
		{".", "", false},
		{"__MACOSX/a", "", false},
		{"a/.DS_Store", "", false},
		{"../a", "", true},
		{"a/../../b", "", true},
		{"/etc/passwd", "", true},
		{"..\\a", "", true},
		{"C:\\x", "C:/x", false},
	}
	for _, tt := range tests {
		got, err := clean(tt.in)
		if got != tt.want || errors.Is(err, ErrUnsafePath) != tt.unsafe {
			t.Errorf("clean(%q) = %q, %v; want %q, unsafe=%v", tt.in, got, err, tt.want, tt.unsafe)
		}
	}
}

// The external path must not hang waiting for a password prompt.
func TestSevenZipDoesNotPrompt(t *testing.T) {
	bin, err := sevenZip()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.CommandContext(t.Context(), bin, "i")
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s i: %v", bin, err)
	}
}
