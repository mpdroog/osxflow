package dock

// Turning the state of the desktop into a row of items.

import (
	"strings"

	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// StackKind says which of the two stacks an item is, so the dock knows
// what to list when it is clicked.
type StackKind int

const (
	// NotAStack is the zero value, for every item that is an application.
	NotAStack StackKind = iota
	StackDownloads
	StackTrash
)

// Stack is set on stack items. It lives beside Item rather than inside it
// so that Item stays the shape the layout code wants.
type Stack struct {
	Kind StackKind
	Dir  string
}

// Builder assembles the dock's items from the pinned list and whatever is
// currently running.
//
// It is stateful for one reason: the order of unpinned applications. A
// running app that is not pinned has no natural position, and recomputing
// one from the window list would let icons swap places whenever a window
// opened -- so the order each app first appeared in is remembered and
// reused for as long as it keeps running.
type Builder struct {
	// PinnedIDs is the dock's fixed contents, in order, as desktop file
	// ids.
	PinnedIDs []string

	// Index resolves windows to applications and ids to applications.
	Index *launch.Index

	// TrashDir is where the trash lives, so the item can show whether it
	// is empty.
	TrashDir string

	// DownloadsDir is what the Downloads stack lists.
	DownloadsDir string

	// transient is the remembered order of unpinned running apps.
	transient []string
}

// Build returns the row of items for a given window list, together with
// the stack metadata for each index.
func (b *Builder) Build(wins []xwin.Window) (items []Item, stacks map[int]Stack) {
	counts := b.countWindows(wins)

	pinned := make(map[string]bool, len(b.PinnedIDs))
	items = make([]Item, 0, len(b.PinnedIDs)+len(b.transient)+3)

	for _, id := range b.PinnedIDs {
		app := b.Index.ByDesktopID(id)
		if app == nil {
			// A pinned application that is no longer installed. Skipping it
			// silently is right: the dock should not carry a dead icon, and
			// this is not a failure the user can act on from here.
			continue
		}
		pinned[app.ID] = true
		items = append(items, Item{
			Icon:      iconKey(app.ID),
			Name:      app.Name,
			Kind:      KindApp,
			DesktopID: app.ID,
			Pinned:    true,
			Windows:   counts[app.ID],
		})
	}

	for _, id := range b.transientOrder(counts, pinned) {
		app := b.Index.ByDesktopID(id)
		if app == nil {
			continue
		}
		items = append(items, Item{
			Icon:      iconKey(app.ID),
			Name:      app.Name,
			Kind:      KindApp,
			DesktopID: app.ID,
			Windows:   counts[app.ID],
		})
	}

	stacks = make(map[int]Stack, 2)
	items = append(items,
		Item{Kind: KindSeparator},
		Item{Icon: icons.Downloads, Name: "Downloads", Kind: KindStack},
	)
	stacks[len(items)-1] = Stack{Kind: StackDownloads, Dir: b.DownloadsDir}

	trashIcon := icons.Trash
	if b.trashCount() > 0 {
		trashIcon = icons.TrashFull
	}
	items = append(items, Item{
		Icon: trashIcon,
		Name: "Trash",
		Kind: KindStack,
	})
	stacks[len(items)-1] = Stack{Kind: StackTrash, Dir: b.TrashDir}

	return items, stacks
}

// trashCount is a variable so tests can drive the icon choice without a
// trash directory on disk.
var trashCounter = defaultTrashCount

func (b *Builder) trashCount() int { return trashCounter(b.TrashDir) }

// countWindows maps each application to how many windows it has open.
func (b *Builder) countWindows(wins []xwin.Window) map[string]int {
	// One cache for the whole sweep: a browser with four windows is four
	// entries with the same pid, and this makes that one /proc read.
	exe := launch.CachedExe(launch.ProcExe)
	counts := make(map[string]int, len(wins))
	for i := range wins {
		// Panels, the desktop and anything asking to be left out of the
		// taskbar are not applications the user opened, even though they
		// are processes with windows that match a .desktop entry.
		if !wins[i].Listable() {
			continue
		}
		app, conf := b.Index.Owner(&wins[i], exe)
		if conf == launch.None || app == nil {
			continue
		}
		counts[app.ID]++
	}
	return counts
}

// transientOrder returns the running-but-unpinned applications, keeping
// the order they were first seen in and dropping the ones that have
// exited.
func (b *Builder) transientOrder(counts map[string]int, pinned map[string]bool) []string {
	kept := b.transient[:0]
	seen := make(map[string]bool, len(counts))
	for _, id := range b.transient {
		if counts[id] > 0 && !pinned[id] {
			kept = append(kept, id)
			seen[id] = true
		}
	}
	// Newly appeared applications go on the end, in a stable order so that
	// two starting at once do not race for position.
	fresh := make([]string, 0, len(counts))
	for id := range counts {
		if !seen[id] && !pinned[id] {
			fresh = append(fresh, id)
		}
	}
	sortStrings(fresh)
	// kept aliases b.transient's array by construction (it is a truncated
	// reslice of it), so this append writes back into the same storage.
	// Assigning through a differently named variable is what appendAssign
	// warns about, and naming it b.transient makes the aliasing explicit.
	b.transient = kept
	b.transient = append(b.transient, fresh...)
	return b.transient
}

func sortStrings(s []string) {
	// A tiny insertion sort: these lists are a handful of entries and this
	// avoids pulling sort into a hot path that runs on every window change.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// iconKey is the embedded icon name for a desktop file id, which is what
// tools/mkicons named the file.
func iconKey(desktopID string) string {
	return strings.TrimSuffix(desktopID, ".desktop")
}
