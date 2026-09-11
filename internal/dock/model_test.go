package dock

import (
	"errors"
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func testBuilder(t *testing.T, pinned []string, apps []desktop.App) *Builder {
	t.Helper()
	return &Builder{
		PinnedIDs: pinned,
		Index:     launch.NewIndex(apps),
	}
}

func testApps() []desktop.App {
	return []desktop.App{
		{ID: "firefox.desktop", Name: "Firefox", Binary: "/usr/bin/firefox", StartupWMClass: "firefox"},
		{ID: "ghostty.desktop", Name: "Ghostty", Binary: "/usr/bin/ghostty", StartupWMClass: "ghostty"},
		{ID: "editor.desktop", Name: "Editor", Binary: "/usr/bin/editor", StartupWMClass: "editor"},
		{ID: "other.desktop", Name: "Other", Binary: "/usr/bin/other", StartupWMClass: "other"},
	}
}

// noTrash keeps the item-building tests off the real filesystem.
func noTrash(t *testing.T) {
	t.Helper()
	prev := trashCounter
	trashCounter = func(string) (int, error) { return 0, nil }
	t.Cleanup(func() { trashCounter = prev })
}

func win(class string) xwin.Window {
	return xwin.Window{Instance: class, Class: class, Type: "NORMAL"}
}

func names(items []Item) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		switch items[i].Kind {
		case KindSeparator:
			out = append(out, "|")
		case KindApp, KindStack:
			out = append(out, items[i].Name)
		}
	}
	return out
}

func TestBuildPinnedOrderAndTail(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, []string{"firefox.desktop", "ghostty.desktop"}, testApps())
	items, stacks := b.Build(nil)

	want := []string{"Firefox", "Ghostty", "|", "Downloads", "Trash"}
	got := names(items)
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	if len(stacks) != 2 {
		t.Errorf("got %d stacks, want 2", len(stacks))
	}
	if stacks[len(items)-2].Kind != StackDownloads || stacks[len(items)-1].Kind != StackTrash {
		t.Error("the stacks are not Downloads then Trash")
	}
}

func TestBuildCountsWindows(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, []string{"firefox.desktop"}, testApps())
	items, _ := b.Build([]xwin.Window{win("firefox"), win("firefox")})
	if items[0].Windows != 2 {
		t.Errorf("Firefox has %d windows, want 2", items[0].Windows)
	}
}

// TestBuildIgnoresDesktopFurniture is the fix for the panel and the dock
// itself turning up as applications: they are real processes with real
// windows that match real .desktop entries, and only the window type says
// otherwise.
func TestBuildIgnoresDesktopFurniture(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, nil, testApps())

	for _, w := range []xwin.Window{
		{Instance: "editor", Class: "editor", Type: "DOCK"},
		{Instance: "editor", Class: "editor", Type: "DESKTOP"},
		{Instance: "editor", Class: "editor", Type: "SPLASH"},
		{Instance: "editor", Class: "editor", Type: "NORMAL", SkipTaskbar: true},
	} {
		items, _ := b.Build([]xwin.Window{w})
		if got := names(items); len(got) != 3 { // separator + two stacks
			t.Errorf("window type %q skip=%v produced %v; it should be ignored",
				w.Type, w.SkipTaskbar, got)
		}
		b.transient = nil
	}

	// A window with no declared type at all is a normal one.
	items, _ := b.Build([]xwin.Window{{Instance: "editor", Class: "editor"}})
	if got := names(items); got[0] != "Editor" {
		t.Errorf("an untyped window was dropped: %v", got)
	}
}

func TestBuildAppendsRunningApps(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, []string{"firefox.desktop"}, testApps())
	items, _ := b.Build([]xwin.Window{win("firefox"), win("editor")})
	got := names(items)
	want := []string{"Firefox", "Editor", "|", "Downloads", "Trash"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	if items[1].Pinned {
		t.Error("a running-but-unpinned app is marked pinned")
	}
}

// TestBuildKeepsTransientOrder is why Builder has state at all: an icon
// that moves when an unrelated window opens breaks the muscle memory that
// makes a dock worth using.
func TestBuildKeepsTransientOrder(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, nil, testApps())

	b.Build([]xwin.Window{win("other")})
	b.Build([]xwin.Window{win("other"), win("editor")})
	items, _ := b.Build([]xwin.Window{win("editor"), win("other")})

	got := names(items)
	if got[0] != "Other" || got[1] != "Editor" {
		t.Errorf("order = %v; the first-seen order should be kept regardless of the window list", got)
	}
}

func TestBuildDropsExitedApps(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, nil, testApps())
	b.Build([]xwin.Window{win("editor"), win("other")})
	items, _ := b.Build([]xwin.Window{win("other")})
	got := names(items)
	if got[0] != "Other" || len(got) != 4 {
		t.Errorf("items = %v; Editor should be gone after its window closed", got)
	}
}

func TestBuildSkipsUninstalledPins(t *testing.T) {
	noTrash(t)
	b := testBuilder(t, []string{"firefox.desktop", "gone.desktop"}, testApps())
	items, _ := b.Build(nil)
	if got := names(items); got[0] != "Firefox" || got[1] != "|" {
		t.Errorf("items = %v; a pin for something uninstalled should be skipped silently", got)
	}
}

func TestBuildTrashIconReflectsContents(t *testing.T) {
	prev := trashCounter
	t.Cleanup(func() { trashCounter = prev })

	b := testBuilder(t, nil, testApps())

	trashCounter = func(string) (int, error) { return 0, nil }
	items, _ := b.Build(nil)
	if got := items[len(items)-1].Icon; got != icons.Trash {
		t.Errorf("empty trash icon = %q, want %q", got, icons.Trash)
	}

	trashCounter = func(string) (int, error) { return 3, nil }
	items, _ = b.Build(nil)
	if got := items[len(items)-1].Icon; got != icons.TrashFull {
		t.Errorf("full trash icon = %q, want %q", got, icons.TrashFull)
	}
}

// TestBuildWarnsWhenTrashCannotBeCounted: the row is still built, with the
// empty icon, but the failure reaches the caller instead of passing for an
// empty trash.
func TestBuildWarnsWhenTrashCannotBeCounted(t *testing.T) {
	prev := trashCounter
	t.Cleanup(func() { trashCounter = prev })
	boom := errors.New("permission denied")
	trashCounter = func(string) (int, error) { return 0, boom }

	var warned []error
	b := testBuilder(t, nil, testApps())
	b.Warn = func(err error) { warned = append(warned, err) }
	items, _ := b.Build(nil)

	if got := items[len(items)-1].Icon; got != icons.Trash {
		t.Errorf("trash icon = %q, want the empty one when it cannot be counted", got)
	}
	if len(warned) != 1 || !errors.Is(warned[0], boom) {
		t.Errorf("warnings = %v, want the counting error once", warned)
	}
}

func TestNoTrashDirIsReported(t *testing.T) {
	if _, err := defaultTrashCount(""); !errors.Is(err, errNoTrashDir) {
		t.Errorf("defaultTrashCount(\"\") error = %v, want errNoTrashDir", err)
	}
}

func TestIconKey(t *testing.T) {
	for _, tc := range [][2]string{
		{"firefox.desktop", "firefox"},
		{"com.mitchellh.ghostty.desktop", "com.mitchellh.ghostty"},
		{"noext", "noext"},
	} {
		if got := iconKey(tc[0]); got != tc[1] {
			t.Errorf("iconKey(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

func TestSortStrings(t *testing.T) {
	got := []string{"c", "a", "b", "a"}
	sortStrings(got)
	want := []string{"a", "a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortStrings = %v, want %v", got, want)
		}
	}
	sortStrings(nil)
}
