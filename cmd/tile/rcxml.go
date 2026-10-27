package main

// labwc's half: the regions and the chords that snap to them, written into
// rc.xml between two markers.
//
// tile keeps that block up to date itself, on every start, because the
// layouts live here in Go and a block that drifted from them would snap
// windows into the wrong cells with nothing to say so. Everything outside
// the markers is the user's and is left byte for byte as it was.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	blockBegin = "<!-- osxflow tile: BEGIN."
	blockEnd   = "<!-- osxflow tile: END -->"
	configEnd  = "</labwc_config>"

	// reconfigureTimeout bounds `labwc --reconfigure`.
	reconfigureTimeout = 5 * time.Second
)

// block is what goes between the markers, markers included.
func block() string {
	var b strings.Builder
	b.WriteString("  " + blockBegin + " Written by cmd/tile on every start; change it there,\n")
	b.WriteString("       not here. Each region is one cell of tile's layout for n windows;\n")
	b.WriteString("       each chord snaps the focused window into one, and only tile's\n")
	b.WriteString("       virtual keyboard has the keys to press it. -->\n")
	b.WriteString("  <regions>\n")
	all := bindings()
	for n := 1; n <= maxTiled; n++ {
		for i, c := range grid(n) {
			fmt.Fprintf(&b, "    <region name=%q x=\"%d%%\" y=\"%d%%\" width=\"%d%%\" height=\"%d%%\" />\n",
				all[n][i].region, c.x, c.y, c.w, c.h)
		}
	}
	b.WriteString("  </regions>\n  <keyboard>\n")
	for n := 1; n <= maxTiled; n++ {
		for _, bd := range all[n] {
			fmt.Fprintf(&b, "    <keybind key=\"%sF%d\"><action name=\"SnapToRegion\" region=%q /></keybind>\n",
				modRC(bd.mods), bd.key, bd.region)
		}
	}
	b.WriteString("  </keyboard>\n  " + blockEnd + "\n")
	return b.String()
}

func modRC(mask uint32) string {
	for _, m := range modSets {
		if m.mask == mask {
			return m.rc
		}
	}
	return ""
}

// splice puts the block into rc.xml's text: in place of the old one, or
// just before </labwc_config> the first time.
func splice(rc string) (string, error) {
	if i := strings.Index(rc, blockBegin); i >= 0 {
		j := strings.Index(rc[i:], blockEnd)
		if j < 0 {
			return "", errors.New("rc.xml has tile's BEGIN marker but not its END; fix it by hand")
		}
		// From the start of the BEGIN line to the end of the END line.
		start := strings.LastIndex(rc[:i], "\n") + 1
		end := i + j + len(blockEnd)
		if end < len(rc) && rc[end] == '\n' {
			end++
		}
		return rc[:start] + block() + rc[end:], nil
	}
	i := strings.LastIndex(rc, configEnd)
	if i < 0 {
		return "", errors.New("rc.xml has no </labwc_config> to put tile's regions before")
	}
	return rc[:i] + block() + "\n" + rc[i:], nil
}

// rcPath is labwc's rc.xml, where labwc looks first.
func rcPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding labwc's config: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "labwc", "rc.xml"), nil
}

// installBlock brings rc.xml's block up to date and has labwc reread it,
// and does neither when it already is.
func installBlock() error {
	path, err := rcPath()
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path) //nolint:gosec // labwc's own rc.xml, from rcPath
	if err != nil {
		return fmt.Errorf("reading labwc's config: %w", err)
	}
	next, err := splice(string(old))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if next == string(old) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("reading labwc's config: %w", err)
	}
	// Through a temporary file, so that labwc -- or a crash -- never sees
	// half a config.
	tmp := path + ".tile-tmp"
	if err := os.WriteFile(tmp, []byte(next), info.Mode().Perm()); err != nil { //nolint:gosec // beside rc.xml, keeping its mode
		return fmt.Errorf("writing labwc's config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("writing labwc's config: %w", err), os.Remove(tmp))
	}
	logf("wrote tile's regions and chords into %s", path)
	// Bounded: labwc answers at once, and a hung compositor should not
	// hang tile's start with it.
	ctx, cancel := context.WithTimeout(context.Background(), reconfigureTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "labwc", "--reconfigure").CombinedOutput(); err != nil {
		return fmt.Errorf("labwc --reconfigure: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
