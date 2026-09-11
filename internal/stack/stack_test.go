package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func writeFile(t *testing.T, path, content string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecentIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeFile(t, filepath.Join(dir, "old.txt"), "x", base)
	writeFile(t, filepath.Join(dir, "middle.txt"), "x", base.Add(10*time.Minute))
	writeFile(t, filepath.Join(dir, "new.txt"), "x", base.Add(20*time.Minute))

	got, err := Recent(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new.txt", "middle.txt", "old.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("order = %v, want %v", entryNames(got), want)
		}
	}
}

func TestRecentLimits(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(dir, string(rune('a'+i))+".txt"), "x", base.Add(time.Duration(i)*time.Minute))
	}
	got, err := Recent(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("got %d entries, want the 5 asked for", len(got))
	}
	if got[0].Name != "j.txt" {
		t.Errorf("newest = %q, want j.txt", got[0].Name)
	}
}

// TestRecentSkipsHiddenFiles matters because a partial download is a dot
// file, and it would otherwise always be the newest thing in the stack.
func TestRecentSkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeFile(t, filepath.Join(dir, "real.zip"), "x", base)
	writeFile(t, filepath.Join(dir, ".real.zip.part"), "x", base.Add(time.Minute))

	got, err := Recent(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "real.zip" {
		t.Errorf("entries = %v, want only real.zip", entryNames(got))
	}
}

func TestRecentMarksDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsDir {
		t.Errorf("entries = %+v, want one directory", got)
	}
}

func TestRecentMissingDir(t *testing.T) {
	if _, err := Recent(filepath.Join(t.TempDir(), "nope"), 5); err == nil {
		t.Error("Recent on a missing directory returned no error")
	}
}

func makeTrash(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func trashItem(t *testing.T, dir, name, deleted string, modTime time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "files", name), "x", modTime)
	if deleted != "" {
		writeFile(t, filepath.Join(dir, "info", name+".trashinfo"),
			"[Trash Info]\nPath=/home/someone/"+name+"\nDeletionDate="+deleted+"\n", time.Time{})
	}
}

// TestTrashEntriesUsesDeletionDate is the whole reason .trashinfo is read:
// a file edited years ago and deleted a minute ago must come first.
func TestTrashEntriesUsesDeletionDate(t *testing.T) {
	dir := makeTrash(t)
	ancient := time.Date(2019, 3, 1, 12, 0, 0, 0, time.Local)

	trashItem(t, dir, "deleted-first.txt", "2026-01-01T09:00:00", time.Now())
	trashItem(t, dir, "deleted-last.txt", "2026-06-01T09:00:00", ancient)

	got, err := TrashEntries(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Name != "deleted-last.txt" {
		t.Errorf("order = %v; the most recently deleted file should be first, "+
			"not the most recently modified", entryNames(got))
	}
}

func TestTrashEntriesFallsBackToModTime(t *testing.T) {
	dir := makeTrash(t)
	base := time.Now().Add(-time.Hour)
	trashItem(t, dir, "no-info.txt", "", base)

	got, err := TrashEntries(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want the file kept even without a .trashinfo", len(got))
	}
	if got[0].When.IsZero() {
		t.Error("entry has no time at all")
	}
}

func TestTrashEntriesEmptyAndMissing(t *testing.T) {
	dir := makeTrash(t)
	got, err := TrashEntries(dir, 5)
	if err != nil || len(got) != 0 {
		t.Errorf("empty trash: got %v, %v", got, err)
	}
	if n, countErr := TrashCount(dir); n != 0 || countErr != nil {
		t.Errorf("TrashCount on an empty trash = %d, %v; want 0, nil", n, countErr)
	}

	// A trash directory that was never created is not an error.
	missing := filepath.Join(t.TempDir(), "never-used")
	got, err = TrashEntries(missing, 5)
	if err != nil || got != nil {
		t.Errorf("missing trash: got %v, %v; want nil, nil", got, err)
	}
	if n, err := TrashCount(missing); n != 0 || err != nil {
		t.Errorf("TrashCount on a missing trash = %d, %v; want 0, nil", n, err)
	}
}

// TestTrashUnreadableIsAnError is the other half of the one above: a trash
// that exists but cannot be listed is not "empty", and saying so would
// show the empty icon over a trash full of files.
func TestTrashUnreadableIsAnError(t *testing.T) {
	dir := t.TempDir()
	// A regular file where the files directory should be makes ReadDir fail
	// with something other than "does not exist", and works as any user.
	writeFile(t, filepath.Join(dir, "files"), "not a directory", time.Time{})

	if n, err := TrashCount(dir); err == nil {
		t.Errorf("TrashCount = %d, nil; want an error", n)
	}
	if got, err := TrashEntries(dir, 5); err == nil {
		t.Errorf("TrashEntries = %v, nil; want an error", entryNames(got))
	}
}

// TestTrashEntriesReportsBadTrashInfo keeps the entry -- the file is
// there, and the user can see it in their file manager -- but says that
// its .trashinfo could not be understood.
func TestTrashEntriesReportsBadTrashInfo(t *testing.T) {
	dir := makeTrash(t)
	trashItem(t, dir, "good.txt", "2026-01-01T09:00:00", time.Now())
	trashItem(t, dir, "bad.txt", "yesterday-ish", time.Now())

	got, err := TrashEntries(dir, 5)
	if err == nil {
		t.Fatal("a malformed DeletionDate was not reported")
	}
	if errors.Is(err, errNoDeletionDate) {
		t.Errorf("malformed date reported as absent: %v", err)
	}
	if !strings.Contains(err.Error(), "bad.txt.trashinfo") {
		t.Errorf("error %q does not name the file", err)
	}
	if len(got) != 2 {
		t.Errorf("entries = %v; the file with the bad .trashinfo should be kept", entryNames(got))
	}
}

func TestTrashCount(t *testing.T) {
	dir := makeTrash(t)
	for i := 0; i < 3; i++ {
		trashItem(t, dir, string(rune('a'+i))+".txt", "", time.Now())
	}
	if got, err := TrashCount(dir); got != 3 || err != nil {
		t.Errorf("TrashCount = %d, %v; want 3, nil", got, err)
	}
}

func TestParseTrashInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		// absent is set when the file simply does not say, which callers
		// stay quiet about; bad when it says something unreadable.
		absent, bad bool
		year        int
	}{
		{"normal", "[Trash Info]\nPath=/x\nDeletionDate=2026-08-24T20:42:11\n", false, false, 2026},
		{"spaces", "DeletionDate= 2026-08-24T20:42:11 \n", false, false, 2026},
		{"missing", "[Trash Info]\nPath=/x\n", true, false, 0},
		{"malformed date", "DeletionDate=not-a-date\n", false, true, 0},
		{"empty", "", true, false, 0},
		{"truncated", "[Trash Info]\nPath=/x\nDeletionDa", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTrashInfo(strings.NewReader(tc.in))
			switch {
			case tc.absent:
				if !errors.Is(err, errNoDeletionDate) {
					t.Fatalf("err = %v, want errNoDeletionDate", err)
				}
			case tc.bad:
				if err == nil || errors.Is(err, errNoDeletionDate) {
					t.Fatalf("err = %v, want a parse error distinct from absence", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if got.Year() != tc.year {
					t.Errorf("year = %d, want %d", got.Year(), tc.year)
				}
			}
		})
	}
}

// TestParseTrashInfoReadError is the case a scanner hides unless asked: a
// read that fails part-way must not look like a file without the key.
func TestParseTrashInfoReadError(t *testing.T) {
	failing := iotest.ErrReader(errors.New("disk on fire"))
	if _, err := parseTrashInfo(failing); err == nil || errors.Is(err, errNoDeletionDate) {
		t.Errorf("err = %v, want the read error", err)
	}
}

func TestParseUserDirs(t *testing.T) {
	const home = "/home/someone"
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"home relative", `XDG_DOWNLOAD_DIR="$HOME/Downloads"`, home + "/Downloads"},
		{"absolute", `XDG_DOWNLOAD_DIR="/mnt/big/dl"`, "/mnt/big/dl"},
		{"renamed", `XDG_DOWNLOAD_DIR="$HOME/Downloads en NL"`, home + "/Downloads en NL"},
		{"other keys only", "XDG_MUSIC_DIR=\"$HOME/Music\"\n", ""},
		{"commented", "# XDG_DOWNLOAD_DIR=\"$HOME/x\"\n", ""},
		{"relative is rejected", `XDG_DOWNLOAD_DIR="relative/path"`, ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUserDirs(strings.NewReader(tc.in), home, "XDG_DOWNLOAD_DIR")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseUserDirsReportsLongLine: bufio.Scanner stops at a line longer
// than its buffer, and without checking Err that reads as "key not set".
func TestParseUserDirsReportsLongLine(t *testing.T) {
	in := strings.Repeat("x", 100_000) + "\nXDG_DOWNLOAD_DIR=\"/dl\"\n"
	if got, err := parseUserDirs(strings.NewReader(in), "/h", "XDG_DOWNLOAD_DIR"); err == nil {
		t.Errorf("got %q, nil; want an error for a line the scanner cannot hold", got)
	}
}

func TestDirsResolve(t *testing.T) {
	d, err := DownloadsDir()
	if err != nil {
		t.Logf("DownloadsDir: %v", err) // the machine's own config; the path must still be usable
	}
	if d == "" || !filepath.IsAbs(d) {
		t.Errorf("DownloadsDir = %q, want an absolute path", d)
	}
	d, err = TrashDir()
	if err != nil {
		t.Fatal(err)
	}
	if d == "" || !filepath.IsAbs(d) {
		t.Errorf("TrashDir = %q, want an absolute path", d)
	}
}

func TestTrashDirHonoursXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	if got, err := TrashDir(); got != "/custom/data/Trash" || err != nil {
		t.Errorf("TrashDir = %q, %v; want /custom/data/Trash, nil", got, err)
	}
}

func TestDownloadsDirFromUserDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".config", "user-dirs.dirs"),
		"XDG_DOWNLOAD_DIR=\"$HOME/Binnen\"\n", time.Time{})
	if got, err := DownloadsDir(); got != filepath.Join(home, "Binnen") || err != nil {
		t.Errorf("DownloadsDir = %q, %v; want %q, nil", got, err, filepath.Join(home, "Binnen"))
	}
}

// TestDownloadsDirWithoutUserDirs: no user-dirs.dirs is the ordinary case
// and says nothing.
func TestDownloadsDirWithoutUserDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := DownloadsDir(); got != filepath.Join(home, "Downloads") || err != nil {
		t.Errorf("DownloadsDir = %q, %v; want ~/Downloads, nil", got, err)
	}
}

// TestDownloadsDirUnreadableUserDirs still answers with ~/Downloads, but
// says why that may not be the folder the user configured.
func TestDownloadsDirUnreadableUserDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A directory where the file should be: opening works, reading fails.
	if err := os.MkdirAll(filepath.Join(home, ".config", "user-dirs.dirs"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := DownloadsDir()
	if err == nil {
		t.Error("an unreadable user-dirs.dirs was not reported")
	}
	if got != filepath.Join(home, "Downloads") {
		t.Errorf("DownloadsDir = %q; want the ~/Downloads fallback alongside the error", got)
	}
}

// FuzzParseTrashInfo runs the one parser here that is fed by other
// programs: .trashinfo files are written by whichever file manager did the
// deleting, and can be caught half-written.
func FuzzParseTrashInfo(f *testing.F) {
	f.Add("[Trash Info]\nPath=/x\nDeletionDate=2026-08-24T20:42:11\n")
	f.Add("DeletionDate=")
	f.Add("")
	f.Fuzz(func(t *testing.T, in string) {
		when, err := parseTrashInfo(strings.NewReader(in))
		if err != nil && !when.IsZero() {
			t.Fatalf("returned a time %v alongside %v", when, err)
		}
	})
}

// FuzzParseUserDirs guards the other file this program reads but does not
// write.
func FuzzParseUserDirs(f *testing.F) {
	f.Add(`XDG_DOWNLOAD_DIR="$HOME/Downloads"`)
	f.Add("XDG_DOWNLOAD_DIR=")
	f.Add("")
	f.Fuzz(func(t *testing.T, in string) {
		got, err := parseUserDirs(strings.NewReader(in), "/home/someone", "XDG_DOWNLOAD_DIR")
		if err != nil && got != "" {
			t.Fatalf("returned %q alongside %v", got, err)
		}
		if got != "" && !filepath.IsAbs(got) {
			t.Fatalf("returned a relative path %q; callers treat it as a directory to open", got)
		}
	})
}

func entryNames(entries []Entry) []string {
	out := make([]string, len(entries))
	for i := range entries {
		out[i] = entries[i].Name
	}
	return out
}
