package main

import (
	"fmt"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/stack"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func main() {
	apps, _ := desktop.Scan()
	ix := launch.NewIndex(apps)
	srv, err := xwin.NewX11()
	if err != nil {
		panic(err)
	}
	wins, err := srv.Windows()
	if err != nil {
		panic(err)
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
	b := &dock.Builder{PinnedIDs: []string{
		"thunderbird.desktop", "firefox.desktop", "com.mitchellh.ghostty.desktop",
		"thunar.desktop", "com.discordapp.Discord.desktop"},
		Index: ix, TrashDir: stack.TrashDir(), DownloadsDir: stack.DownloadsDir()}
	items, _ := b.Build(wins)
	fmt.Println("--- dock items ---")
	for i := range items {
		fmt.Printf("%2d kind=%d pinned=%-5v wins=%d icon=%-28s embedded=%-5v %s\n",
			i, items[i].Kind, items[i].Pinned, items[i].Windows, items[i].Icon,
			icons.Has(items[i].Icon), items[i].Name)
	}
}
