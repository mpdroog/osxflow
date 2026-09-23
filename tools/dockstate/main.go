package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/stack"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dockstate:", err)
		os.Exit(1)
	}
}

func run() (err error) {
	// A .desktop file that does not parse costs that one entry, exactly as
	// it does in the dock, but this tool exists to explain what the dock
	// sees, so it says which ones.
	apps, problems := desktop.Scan()
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "warning:", p)
	}
	ix := launch.NewIndex(apps)
	srv, err := xwin.NewServer()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := srv.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing the X connection: %w", closeErr))
		}
	}()
	wins, err := srv.Windows()
	if err != nil {
		return err
	}
	fmt.Println("--- windows ---")
	exe := launch.CachedExe(launch.ProcExe)
	for i := range wins {
		app, conf := ix.Owner(&wins[i], exe)
		name := "<none>"
		id := ""
		if app != nil {
			name, id = app.Name, app.ID
		}
		fmt.Printf("%-22s %-26s -> %-26s %-14s %s\n", wins[i].Instance, wins[i].Class, id, conf, name)
	}
	// Neither stops the dock: Downloads falls back to ~/Downloads alongside
	// its error, and an empty trash directory makes the Builder warn rather
	// than count. So they are warnings here too, not reasons to stop.
	trashDir, trashErr := stack.TrashDir()
	if trashErr != nil {
		fmt.Fprintln(os.Stderr, "warning: trash:", trashErr)
	}
	downloadsDir, downloadsErr := stack.DownloadsDir()
	if downloadsErr != nil {
		fmt.Fprintln(os.Stderr, "warning: downloads:", downloadsErr)
	}
	// Must match pinnedIDs in cmd/dock/theme.go. This tool exists to
	// explain what the dock sees, so a copy that has drifted from the
	// dock's own list does not merely go stale -- it reports confidently
	// about a dock that does not exist.
	b := &dock.Builder{PinnedIDs: []string{
		"thunderbird.desktop", "org.mozilla.firefox.desktop", "com.mitchellh.ghostty.desktop",
		"thunar.desktop", "com.discordapp.Discord.desktop"},
		Index: ix, TrashDir: trashDir, DownloadsDir: downloadsDir}
	items, _ := b.Build(wins) // the second result is the stacks, not an error
	fmt.Println("--- dock items ---")
	for i := range items {
		fmt.Printf("%2d kind=%d pinned=%-5v wins=%d icon=%-28s embedded=%-5v %s\n",
			i, items[i].Kind, items[i].Pinned, items[i].Windows, items[i].Icon,
			icons.Has(items[i].Icon), items[i].Name)
	}
	return nil
}
