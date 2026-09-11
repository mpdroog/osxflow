package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirSize(t *testing.T) {
	for _, tc := range []struct {
		dir  string
		want int
	}{
		{"/usr/share/icons/hicolor/48x48/apps", 48},
		{"/home/u/.local/share/icons/WhiteSur/apps/64", 64},
		{"/usr/share/icons/Papirus/32x32@2x/apps", 32},
		{"/usr/share/icons/hicolor/scalable/apps", 1 << 20},
		{"/usr/share/icons/Adwaita/symbolic/apps", 0},
		{"/usr/share/pixmaps", 0},
		// The largest number on the path wins.
		{"/icons/16/apps/128", 128},
	} {
		if got := dirSize(tc.dir); got != tc.want {
			t.Errorf("dirSize(%q) = %d, want %d", tc.dir, got, tc.want)
		}
	}
}

// Without a home directory the user's themes cannot be found, and the
// build must say so rather than search a directory literally named "~".
func TestThemeChainNeedsAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if dirs, err := themeChain(); err == nil {
		t.Errorf("themeChain = %v with no HOME, want an error", dirs)
	}

	t.Setenv("HOME", "/home/someone")
	dirs, err := themeChain()
	if err != nil {
		t.Fatalf("themeChain: %v", err)
	}
	if !strings.HasPrefix(dirs[0], "/home/someone/") {
		t.Errorf("themeChain()[0] = %q, want it under HOME", dirs[0])
	}
}

// collect returns a warn func and the warnings it has seen.
func collect() (warn func(error), got *[]error) {
	var errs []error
	return func(err error) { errs = append(errs, err) }, &errs
}

func mkfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWalkThemeFollowsLinksAndCountsDanglingOnes(t *testing.T) {
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "base", "apps", "48", "firefox.svg"))
	// A directory reached only through a symlink, as WhiteSur-dark does.
	if err := os.Symlink(filepath.Join(root, "base", "apps"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	// A symlink to nothing: themes ship these, and they are not faults.
	if err := os.Symlink(filepath.Join(root, "nowhere.svg"), filepath.Join(root, "base", "dangling.svg")); err != nil {
		t.Fatal(err)
	}
	// A cycle, which must be visited once rather than forever.
	if err := os.Symlink(root, filepath.Join(root, "base", "loop")); err != nil {
		t.Fatal(err)
	}

	warn, warnings := collect()
	ix, err := buildIndex([]string{root, filepath.Join(root, "not-installed")}, warn)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if _, ok := ix.index["firefox"]; !ok {
		t.Errorf("firefox not indexed; index = %v", ix.index)
	}
	if ix.dangling != 1 {
		t.Errorf("dangling = %d, want 1", ix.dangling)
	}
	// An absent theme and a dangling link are both normal: no warnings.
	if len(*warnings) != 0 {
		t.Errorf("warnings = %v, want none", *warnings)
	}
}

func TestWalkThemeWarnsAboutUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "apps", "firefox.svg"))
	locked := filepath.Join(root, "locked")
	mkfile(t, filepath.Join(locked, "hidden.svg"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o750); err != nil {
			t.Errorf("restoring permissions: %v", err)
		}
	})

	warn, warnings := collect()
	ix, err := buildIndex([]string{root}, warn)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if _, ok := ix.index["firefox"]; !ok {
		t.Error("the readable part of the theme was not indexed")
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0].Error(), "locked") {
		t.Errorf("warnings = %v, want one about the locked directory", *warnings)
	}
}

func TestWalkThemeWarnsWhenTooDeep(t *testing.T) {
	root := t.TempDir()
	deep := root
	for range 12 {
		deep = filepath.Join(deep, "d")
	}
	mkfile(t, filepath.Join(deep, "buried.svg"))
	mkfile(t, filepath.Join(root, "shallow.svg"))

	warn, warnings := collect()
	ix, err := buildIndex([]string{root}, warn)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if _, ok := ix.index["buried"]; ok {
		t.Error("an icon below the depth limit was indexed")
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0].Error(), "levels deep") {
		t.Errorf("warnings = %v, want one about the depth limit", *warnings)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	onDisk := filepath.Join(dir, "custom.png")
	mkfile(t, onDisk)
	index := map[string]candidate{
		"firefox": {path: "/themes/firefox.svg"},
		"steam":   {path: "/themes/steam.svg"},
	}

	for _, tc := range []struct {
		icon string
		want string
	}{
		{"firefox", "/themes/firefox.svg"},
		{"steam.png", "/themes/steam.svg"},       // an extension it should not have had
		{onDisk, onDisk},                         // an absolute path that exists
		{"/gone/firefox", "/themes/firefox.svg"}, // falls back to the basename
	} {
		got, err := resolve(tc.icon, index)
		if err != nil || got != tc.want {
			t.Errorf("resolve(%q) = %q, %v; want %q", tc.icon, got, err, tc.want)
		}
	}

	for _, icon := range []string{"", "no-such-icon", "/gone/no-such-icon"} {
		if got, err := resolve(icon, index); !errors.Is(err, errNoIcon) {
			t.Errorf("resolve(%q) = %q, %v; want errNoIcon", icon, got, err)
		}
	}
}

// A file that exists but cannot be looked at is a failure to report, not
// an icon that is merely missing.
func TestResolveReportsUnreadablePaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	mkfile(t, filepath.Join(locked, "icon.png"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o750); err != nil {
			t.Errorf("restoring permissions: %v", err)
		}
	})

	_, err := resolve(filepath.Join(locked, "icon.png"), map[string]candidate{})
	if err == nil || errors.Is(err, errNoIcon) {
		t.Errorf("resolve(unreadable) error = %v, want a permission error", err)
	}
}
