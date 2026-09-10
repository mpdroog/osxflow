// Command mkicons rasterises the desktop's icon theme into PNGs that
// cmd/dock embeds in its binary.
//
// This tool never ships. That is the entire point of it existing.
//
// The icon theme on this machine (WhiteSur-dark) is 13,099 SVG files and
// not one PNG, and those SVGs are not the simple path-and-fill kind: they
// layer a base64-embedded raster shadow under vector plates under, in
// some cases, a second embedded raster logo. Rendering them correctly
// needs a real SVG implementation. The pure-Go rasterisers get them
// visibly wrong -- oksvg drops every <image> element and mangles
// transform lists written without separators, which turns Firefox into a
// blank white square.
//
// So the rendering happens here, at build time, through librsvg (via
// gdk-pixbuf-thumbnailer, which is what GTK itself draws with, so the
// result matches the rest of the desktop exactly). The dock binary gets
// finished pixels. It contains no SVG parser, does no icon-theme lookup,
// and reads nothing from ~/.local/share/icons at runtime -- which means a
// theme update, a moved file or a malformed SVG cannot break a dock that
// is already compiled.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mpdroog/osxflow/internal/desktop"
)

func main() {
	var (
		size    = flag.Int("size", 160, "pixel size to rasterise at")
		outDir  = flag.String("out", "internal/icons/data", "directory to write PNGs into")
		verbose = flag.Bool("v", false, "report every icon resolved")
	)
	flag.Parse()

	if err := run(*size, *outDir, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "mkicons:", err)
		os.Exit(1)
	}
}

// sysIcons are the icons the dock needs that no .desktop file names: the
// two trash states and the Downloads stack. The key is the name the dock
// asks for, the value the icon-theme name to look up.
var sysIcons = map[string]string{
	"sys-trash":      "user-trash",
	"sys-trash-full": "user-trash-full",
	"sys-downloads":  "folder-download",
}

func run(size int, outDir string, verbose bool) error {
	if _, err := exec.LookPath(thumbnailer); err != nil {
		return fmt.Errorf("%s is required to rasterise the icon theme: %w", thumbnailer, err)
	}

	index, err := buildIndex(themeChain())
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d icon names across the theme chain\n", len(index))

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}
	if err := clean(outDir); err != nil {
		return err
	}

	wanted := map[string]string{} // output key -> icon name or absolute path
	for key, name := range sysIcons {
		wanted[key] = name
	}

	apps, problems := desktop.Scan()
	for _, err := range problems {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	for i := range apps {
		app := &apps[i]
		if app.Icon == "" {
			continue
		}
		wanted[strings.TrimSuffix(app.ID, ".desktop")] = app.Icon
	}

	keys := make([]string, 0, len(wanted))
	for k := range wanted {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var written, missing, failed int
	for _, key := range keys {
		src, ok := resolve(wanted[key], index)
		if !ok {
			missing++
			if verbose {
				fmt.Printf("  no icon file for %-40s (Icon=%s)\n", key, wanted[key])
			}
			continue
		}
		dst := filepath.Join(outDir, key+".png")
		if err := rasterise(src, dst, size); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", key, err)
			continue
		}
		written++
		if verbose {
			fmt.Printf("  %-40s <- %s\n", key, src)
		}
	}

	fmt.Printf("wrote %d icons at %dpx into %s (%d without an icon file, %d failed)\n",
		written, size, outDir, missing, failed)
	if written == 0 {
		return fmt.Errorf("no icons were produced; the dock would have nothing to draw")
	}
	return nil
}

// clean removes previously generated PNGs so that an icon which has since
// disappeared from the theme does not linger in the binary forever.
func clean(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.png"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing %s: %w", path, err)
		}
	}
	return nil
}

const thumbnailer = "gdk-pixbuf-thumbnailer"

// rasterise renders one icon file to a PNG of exactly the requested size.
func rasterise(src, dst string, size int) error {
	//nolint:gosec,noctx // a build-time tool running a fixed binary on theme files
	cmd := exec.Command(thumbnailer, "-s", strconv.Itoa(size), src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", thumbnailer, err, strings.TrimSpace(string(out)))
	}
	info, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("no output produced: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("produced an empty file")
	}
	return nil
}
