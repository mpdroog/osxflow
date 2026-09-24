package sni

import (
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

func TestParseService(t *testing.T) {
	for _, tc := range []struct {
		sender, service string
		want            Ref
		ok              bool
	}{
		{":1.5", "org.kde.StatusNotifierItem-12-1", Ref{"org.kde.StatusNotifierItem-12-1", Path}, true},
		{":1.5", "/StatusNotifierItem", Ref{":1.5", "/StatusNotifierItem"}, true},
		{":1.5", "/org/ayatana/NotificationItem/x", Ref{":1.5", "/org/ayatana/NotificationItem/x"}, true},
		{":1.5", ":1.7/StatusNotifierItem", Ref{":1.7", "/StatusNotifierItem"}, true},
		{":1.5", "org.foo.Bar/a/b", Ref{"org.foo.Bar", "/a/b"}, true},
		{":1.5", "", Ref{}, false},
		{":1.5", "nodots", Ref{}, false},
		{":1.5", "org.foo.1bad", Ref{}, false},
		{":1.5", "org.foo/", Ref{"org.foo", "/"}, true},
		{":1.5", "/bad//path", Ref{}, false},
		{"", "/StatusNotifierItem", Ref{}, false},
	} {
		got, err := ParseService(tc.sender, tc.service)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseService(%q, %q) = %+v, %v; want %+v, ok=%v", tc.sender, tc.service, got, err, tc.want, tc.ok)
		}
	}
}

func FuzzParseService(f *testing.F) {
	for _, s := range []string{"org.kde.X", "/StatusNotifierItem", ":1.2/a", "a.b/c/d", "", "/", "x"} {
		f.Add(":1.9", s)
	}
	f.Fuzz(func(t *testing.T, sender, service string) {
		ref, err := ParseService(sender, service)
		if err != nil {
			return
		}
		if !validBusName(ref.Service) || !ref.Path.IsValid() {
			t.Fatalf("ParseService(%q, %q) accepted %+v", sender, service, ref)
		}
	})
}

func waitItems(t *testing.T, w *Watcher, want int) []Ref {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if items := w.Items(); len(items) == want {
			return items
		}
		select {
		case <-w.Changes():
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("watcher has %d items, want %d", len(w.Items()), want)
		}
	}
}

// An item registers, is listed, and is dropped when its connection goes;
// a second registration of the same item is not a second item.
func TestWatcherTracksItems(t *testing.T) {
	addr := dbustest.Start(t)
	w, err := NewWatcher(dbustest.Conn(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := w.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	itemConn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	it, err := Export(itemConn, &Options{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if regErr := registration(t, it); regErr != nil {
		t.Fatalf("registering: %v", regErr)
	}
	items := waitItems(t, w, 1)
	if items[0] != (Ref{Service: it.name, Path: Path}) {
		t.Errorf("listed %+v", items[0])
	}

	// A path alone, as fyne's systray registers, means the caller's own
	// connection.
	pathConn := dbustest.Conn(t, addr)
	for range 2 {
		if callErr := pathConn.Object(WatcherName, WatcherPath).Call(WatcherName+".RegisterStatusNotifierItem", 0, "/StatusNotifierItem").Err; callErr != nil {
			t.Fatal(callErr)
		}
	}
	items = waitItems(t, w, 2)
	if !slices.Contains(items, Ref{Service: pathConn.Names()[0], Path: "/StatusNotifierItem"}) {
		t.Errorf("path registration missing from %+v", items)
	}

	v, err := pathConn.Object(WatcherName, WatcherPath).GetProperty(WatcherName + ".RegisteredStatusNotifierItems")
	if err != nil {
		t.Fatal(err)
	}
	if list, ok := v.Value().([]string); !ok || len(list) != 2 {
		t.Errorf("RegisteredStatusNotifierItems = %v", v)
	}

	if err := itemConn.Close(); err != nil {
		t.Fatal(err)
	}
	items = waitItems(t, w, 1)
	if items[0].Service == it.name {
		t.Errorf("item still listed after its connection closed: %+v", items)
	}
}

func TestWatcherRefusesASecondTray(t *testing.T) {
	addr := dbustest.Start(t)
	w, err := NewWatcher(dbustest.Conn(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := w.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, err := NewWatcher(dbustest.Conn(t, addr)); err != ErrTrayRunning { //nolint:errorlint // NewWatcher returns the sentinel itself
		t.Errorf("second watcher: %v, want ErrTrayRunning", err)
	}
}

// The host reads an item's properties and clicks it.
func TestRemote(t *testing.T) {
	addr := dbustest.Start(t)
	icon := []Pixmap{{Width: 1, Height: 1, Pix: []byte{0xff, 0x10, 0x20, 0x30}}}
	it, err := Export(dbustest.Conn(t, addr), &Options{ID: "remote-test", Title: "Remote", Icon: icon,
		ToolTip: ToolTip{Title: "tip"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := it.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	go func() {
		for range it.Registrations() {
		}
	}()

	r := NewRemote(dbustest.Conn(t, addr), Ref{Service: it.name, Path: Path})
	ctx := t.Context()
	state, problems := r.Fetch(ctx)
	if len(problems) != 0 {
		t.Fatalf("Fetch problems: %v", problems)
	}
	if state.ID != "remote-test" || state.Title != "Remote" || state.ToolTip.Title != "tip" ||
		len(state.Icon) != 1 || state.Hidden() || state.Menu != "" {
		t.Errorf("Fetch = %+v", state)
	}
	owner, err := r.Owner(ctx)
	if err != nil || owner == "" || owner[0] != ':' {
		t.Errorf("Owner = %q, %v", owner, err)
	}

	done := make(chan error, 1)
	go func() { done <- r.Click(ctx, Click{Kind: Scroll, Delta: 3, Horizontal: true}) }()
	select {
	case c := <-it.Clicks():
		if c != (Click{Kind: Scroll, Delta: 3, Horizontal: true}) {
			t.Errorf("delivered %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no click delivered")
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestDecodeStateReportsWrongTypes(t *testing.T) {
	s, problems := DecodeState(map[string]dbus.Variant{
		"Id":         dbus.MakeVariant("x"),
		"Title":      dbus.MakeVariant(int32(3)),
		"Status":     dbus.MakeVariant("Passive"),
		"Menu":       dbus.MakeVariant(dbus.ObjectPath("/")),
		"ItemIsMenu": dbus.MakeVariant(true),
	})
	if s.ID != "x" || s.Title != "" || !s.Hidden() || s.Menu != "" || !s.ItemIsMenu {
		t.Errorf("DecodeState = %+v", s)
	}
	if len(problems) != 1 {
		t.Errorf("problems = %v, want one for Title", problems)
	}
}

func TestChanged(t *testing.T) {
	for name, want := range map[string]bool{
		Interface + ".NewIcon":    true,
		Interface + ".NewStatus":  true,
		Interface + ".Activate":   false,
		"org.other.NewIcon":       false,
		Interface + ".NewToolTip": true,
	} {
		if got := Changed(&dbus.Signal{Name: name}); got != want {
			t.Errorf("Changed(%s) = %v", name, got)
		}
	}
	if Changed(nil) {
		t.Error("Changed(nil)")
	}
}
