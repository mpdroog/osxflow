package main

// Finding the file behind an Icon= name.
//
// The icon theme specification defines a lookup by (name, size, context)
// walking an inheritance chain. This does something blunter: it indexes
// every icon file in every theme once, then picks the best candidate per
// name. That is affordable here because it happens at build time and only
// once, and it is more robust than following index.theme, because the
// themes on this machine do not describe themselves accurately -- the
// directories in WhiteSur-dark are symlinks into WhiteSur, which a spec
// walker following Directories= would never discover.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// iconExts are the formats gdk-pixbuf can rasterise, best first. SVG wins
// because it renders cleanly at whatever size the dock asks for.
var iconExts = []string{".svg", ".png", ".xpm"}

// candidate is one file that could serve an icon name.
type candidate struct {
	path string

	// theme is the index of the theme it came from: earlier themes in the
	// chain win outright, which is what "the user chose this theme" means.
	theme int

	// size is the nominal pixel size parsed from the directory name, with
	// scalable treated as larger than any fixed size.
	size int

	// ext is the index into iconExts.
	ext int

	// apps is true for a file in an apps/ directory. An application icon
	// found under apps/ is more likely to be the right one than a
	// same-named file under mimes/ or status/.
	apps bool
}

// better reports whether a should be preferred over b.
func better(a, b *candidate) bool {
	if a.theme != b.theme {
		return a.theme < b.theme
	}
	if a.apps != b.apps {
		return a.apps
	}
	if a.ext != b.ext {
		return a.ext < b.ext
	}
	// Largest available, so that downscaling rather than upscaling is what
	// produces the dock's icon.
	return a.size > b.size
}

// themeChain is the icon themes to search, best first.
//
// The list is deliberately literal. This tool runs on one machine, for one
// desktop, and reading the active theme out of xfconf at build time would
// add a way for the build to produce different output on different days
// without anybody asking it to.
func themeChain() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	themes := []string{
		"WhiteSur-dark", // the active theme
		"WhiteSur",      // which is mostly symlinks into this one
		"Papirus-Dark",  // broad coverage for anything WhiteSur lacks
		"Papirus",
		"Adwaita",
		"breeze",
		"hicolor", // the freedesktop fallback every package installs into
	}
	const systemIcons = "/usr/share/icons"
	dirs := make([]string, 0, 2*len(themes)+1)
	for _, theme := range themes {
		dirs = append(dirs,
			filepath.Join(home, ".local", "share", "icons", theme),
			filepath.Join(systemIcons, theme),
		)
	}
	// Loose files that belong to no theme at all. Plenty of packages still
	// drop a PNG here and name it from Icon=.
	dirs = append(dirs, "/usr/share/pixmaps")
	return dirs
}

// buildIndex maps an icon name to the best file found for it.
func buildIndex(dirs []string) (map[string]candidate, error) {
	index := make(map[string]candidate, 4096)
	for i, dir := range dirs {
		if err := walkTheme(dir, i, index); err != nil {
			return nil, err
		}
	}
	if len(index) == 0 {
		return nil, fmt.Errorf("no icon files found; searched %v", dirs)
	}
	return index, nil
}

// walkTheme indexes one theme directory.
//
// It follows directory symlinks, which the standard walkers do not, and
// which this must: every size directory in WhiteSur-dark is a symlink.
// Cycles are guarded by remembering the resolved path of each directory
// entered, so a theme that links to its own parent is visited once rather
// than forever.
func walkTheme(root string, theme int, index map[string]candidate) error {
	seen := make(map[string]bool)
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		// A theme is a handful of levels deep. The limit is a backstop
		// against a symlink arrangement the cycle check cannot see, such
		// as two directories linking to each other through /proc.
		const maxDepth = 8
		if depth > maxDepth {
			return nil
		}
		// A dangling link, or a theme that is not installed: not an error,
		// just a directory that is not there. Most of the chain is absent
		// on any given machine.
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil //nolint:nilerr // an absent theme is the normal case
		}
		if seen[resolved] {
			return nil
		}
		seen[resolved] = true

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil //nolint:nilerr // an unreadable directory costs its icons, not the build
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path) // Stat, not e.Info: follows symlinks
			if err != nil {
				continue
			}
			if info.IsDir() {
				if err := walk(path, depth+1); err != nil {
					return err
				}
				continue
			}
			add(index, path, theme, dir)
		}
		return nil
	}
	return walk(root, 0)
}

func add(index map[string]candidate, path string, theme int, dir string) {
	ext := strings.ToLower(filepath.Ext(path))
	extIdx := -1
	for i, want := range iconExts {
		if ext == want {
			extIdx = i
			break
		}
	}
	if extIdx < 0 {
		return
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	c := candidate{
		path:  path,
		theme: theme,
		size:  dirSize(dir),
		ext:   extIdx,
		apps:  strings.Contains(dir, "apps"),
	}
	if prev, ok := index[name]; ok && !better(&c, &prev) {
		return
	}
	index[name] = c
}

// dirSize reads the nominal icon size out of a theme directory path.
//
// Themes disagree about where the number goes: WhiteSur writes apps/48,
// hicolor writes 48x48/apps, and both write @2x variants. Anything
// scalable is reported as larger than every fixed size, because an SVG
// renders perfectly at the dock's size and a 512px PNG does not.
func dirSize(dir string) int {
	const scalable = 1 << 20
	best := 0
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		if part == "scalable" || part == "symbolic" {
			// symbolic icons are monochrome glyphs meant for toolbars; they
			// are scalable but wrong for a dock, so they rank below real
			// artwork of any size.
			if part == "symbolic" {
				continue
			}
			return scalable
		}
		// "48x48" and "48" and "48@2x" all mean 48.
		part, _, _ = strings.Cut(part, "@")
		if x, _, ok := strings.Cut(part, "x"); ok {
			part = x
		}
		if n, err := strconv.Atoi(part); err == nil && n > best {
			best = n
		}
	}
	return best
}

// resolve turns an Icon= value into a file on disk.
//
// The value is usually a bare theme name ("firefox"), but the spec allows
// an absolute path, and a handful of entries give a filename complete with
// extension. All three appear on this machine.
func resolve(icon string, index map[string]candidate) (string, bool) {
	if icon == "" {
		return "", false
	}
	if filepath.IsAbs(icon) {
		if _, err := os.Stat(icon); err == nil {
			return icon, true
		}
		// An absolute path that does not exist can still name an icon that
		// does: fall through and try its basename.
		icon = filepath.Base(icon)
	}
	if c, ok := index[icon]; ok {
		return c.path, true
	}
	// Trim an extension the entry should not have included.
	if ext := filepath.Ext(icon); ext != "" {
		if c, ok := index[strings.TrimSuffix(icon, ext)]; ok {
			return c.path, true
		}
	}
	return "", false
}
