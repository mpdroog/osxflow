package unpack

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDetect: whatever the name, a detected stem is the name minus a
// suffix, and never empty.
func FuzzDetect(f *testing.F) {
	for _, s := range []string{"a.tar.gz", "b.ZIP", ".tgz", "x.7z", "report.docx", "a.gz.zip"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, ok := Detect(name)
		if !ok {
			return
		}
		if got.Stem == "" || !strings.HasPrefix(name, got.Stem) || len(got.Stem) >= len(name) {
			t.Fatalf("Detect(%q) stem %q", name, got.Stem)
		}
	})
}

// FuzzClean: a name clean accepts is always local.
func FuzzClean(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "/abs", "a\\..\\..\\b", "__MACOSX/y", "."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, err := clean(name)
		if err != nil || got == "" {
			return
		}
		if !filepath.IsLocal(got) || strings.Contains(got, "\\") {
			t.Fatalf("clean(%q) = %q, not local", name, got)
		}
	})
}

// checkContained extracts into a directory inside a sandbox and fails if
// anything appears in the sandbox outside it.
func checkContained(t *testing.T, extract func(w *writer) error) {
	t.Helper()
	sandbox := t.TempDir()
	inner := filepath.Join(sandbox, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(inner)
	if err != nil {
		t.Fatal(err)
	}
	w := newWriter(root)
	if extractErr := extract(w); extractErr == nil {
		if finishErr := w.finish(); finishErr != nil {
			t.Logf("finish: %v", finishErr)
		}
	}
	if closeErr := root.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	entries, err := os.ReadDir(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "inner" {
		t.Fatalf("extraction escaped: sandbox holds %v", entries)
	}
	// Make sure TempDir can clean up whatever modes were set.
	if err := filepath.WalkDir(inner, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			return os.Chmod(p, 0o755)
		}
		return nil
	}); err != nil {
		t.Logf("resetting modes: %v", err)
	}
}

func FuzzExtractTar(f *testing.F) {
	f.Add(makeTar(f, []entry{file("a", "A"), {name: "d", link: "/tmp"}, file("d/x", "X")}))
	f.Add(makeTar(f, []entry{{name: "h", hard: "../x"}, file("../y", "Y")}))
	f.Fuzz(func(t *testing.T, data []byte) {
		checkContained(t, func(w *writer) error { return extractTar(bytes.NewReader(data), w) })
	})
}

func FuzzExtractZip(f *testing.F) {
	f.Add(makeZip(f, []entry{file("a", "A"), {name: "d", link: "/tmp"}, file("d/x", "X")}))
	f.Add(makeZip(f, []entry{file("..\\y", "Y"), {name: "caf\x82", cp437: true}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		checkContained(t, func(w *writer) error { return extractZip(zr, w) })
	})
}
