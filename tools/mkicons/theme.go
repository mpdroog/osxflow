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
	"errors"
	"fmt"
	"io/fs"
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
//
// No home directory is an error rather than a reason to skip the user's
// themes: WhiteSur is installed under ~/.local, so leaving those out would
// quietly build the dock with the wrong icons.
func themeChain() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locating the user's icon themes: %w", err)
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
	return dirs, nil
}

// indexer collects the icon index across the theme chain.
//
// Nothing that goes wrong while walking a theme stops the build: an
// unreadable directory costs the icons in it, and the build reports how
// many icons it did and did not produce at the end. But every such failure
// is passed to warn, so a theme that half-indexed says why.
type indexer struct {
	index map[string]candidate
	warn  func(error)

	// dangling counts symlinks whose target does not exist. Icon themes
	// ship them by the hundred -- WhiteSur links names to icons it does not
	// include -- so they are counted rather than listed.
	dangling int
}

// buildIndex maps an icon name to the best file found for it.
func buildIndex(dirs []string, warn func(error)) (*indexer, error) {
	ix := &indexer{index: make(map[string]candidate, 4096), warn: warn}
	for i, dir := range dirs {
		ix.walkTheme(dir, i)
	}
	if len(ix.index) == 0 {
		return nil, fmt.Errorf("no icon files found; searched %v", dirs)
	}
	return ix, nil
}

// walkTheme indexes one theme directory.
//
// It follows directory symlinks, which the standard walkers do not, and
// which this must: every size directory in WhiteSur-dark is a symlink.
// Cycles are guarded by remembering the resolved path of each directory
// entered, so a theme that links to its own parent is visited once rather
// than forever.
func (ix *indexer) walkTheme(root string, theme int) {
	seen := make(map[string]bool)
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		// A theme is a handful of levels deep. The limit is a backstop
		// against a symlink arrangement the cycle check cannot see, such
		// as two directories linking to each other through /proc.
		const maxDepth = 8
		if depth > maxDepth {
			ix.warn(fmt.Errorf("%s: more than %d levels deep; not indexed", dir, maxDepth))
			return
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, fs.ErrNotExist) {
			// A theme that is not installed: not an error, just a
			// directory that is not there. Most of the chain is absent on
			// any given machine.
			return
		}
		if err != nil {
			ix.warn(fmt.Errorf("resolving %s: %w", dir, err))
			return
		}
		if seen[resolved] {
			return
		}
		seen[resolved] = true

		// An unreadable directory costs its icons, not the build. ReadDir
		// still returns the entries it read before failing, and those are
		// indexed.
		entries, err := os.ReadDir(dir)
		if err != nil {
			ix.warn(fmt.Errorf("reading %s: %w", dir, err))
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path) // Stat, not e.Info: follows symlinks
			if errors.Is(err, fs.ErrNotExist) {
				ix.dangling++
				continue
			}
			if err != nil {
				ix.warn(err)
				continue
			}
			if info.IsDir() {
				walk(path, depth+1)
				continue
			}
			add(ix.index, path, theme, dir)
		}
	}
	walk(root, 0)
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
		n, err := strconv.Atoi(part)
		if err != nil {
			// Not a size segment. Most are not -- "apps", "WhiteSur-dark",
			// "share" -- so a segment that is not a number is the expected
			// case here, not a failure.
			continue
		}
		best = max(best, n)
	}
	return best
}

// errNoIcon reports an Icon= value that no file serves. Plenty of entries
// name icons the installed themes do not have; that is counted, not
// warned about.
var errNoIcon = errors.New("no icon file")

// resolve turns an Icon= value into a file on disk.
//
// The value is usually a bare theme name ("firefox"), but the spec allows
// an absolute path, and a handful of entries give a filename complete with
// extension. All three appear on this machine.
//
// The error is errNoIcon when nothing serves the name. Anything else is a
// file that exists but could not be looked at, which is worth a warning.
func resolve(icon string, index map[string]candidate) (string, error) {
	if icon == "" {
		return "", errNoIcon
	}
	if filepath.IsAbs(icon) {
		_, err := os.Stat(icon)
		switch {
		case err == nil:
			return icon, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("icon %s: %w", icon, err)
		}
		// An absolute path that does not exist can still name an icon that
		// does: fall through and try its basename.
		icon = filepath.Base(icon)
	}
	if c, ok := index[icon]; ok {
		return c.path, nil
	}
	// Trim an extension the entry should not have included.
	if ext := filepath.Ext(icon); ext != "" {
		if c, ok := index[strings.TrimSuffix(icon, ext)]; ok {
			return c.path, nil
		}
	}
	return "", errNoIcon
}
