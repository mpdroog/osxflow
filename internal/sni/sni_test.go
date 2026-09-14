package sni

import (
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

type fakeWatcher struct{ got chan string }

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.got <- service
	return nil
}

func startWatcher(t *testing.T, addr string) *fakeWatcher {
	t.Helper()
	conn := dbustest.Conn(t, addr)
	w := &fakeWatcher{got: make(chan string, 4)}
	if err := conn.Export(w, WatcherPath, WatcherName); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(WatcherName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", WatcherName, reply, err)
	}
	return w
}

func export(t *testing.T, addr string) *Item {
	t.Helper()
	it, err := Export(dbustest.Conn(t, addr), &Options{ID: "test", Title: "Test", Category: "SystemServices"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := it.Close(); err != nil {
			t.Error(err)
		}
	})
	return it
}

func registration(t *testing.T, it *Item) error {
	t.Helper()
	select {
	case err := <-it.Registrations():
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("no registration result")
	}
	return nil
}

// A tray that starts after the item -- the panel being slower than
// autostart at login -- still gets it.
func TestRegistersWhenTrayAppears(t *testing.T) {
	addr := dbustest.Start(t)
	it := export(t, addr)
	if err := registration(t, it); !errors.Is(err, ErrNoWatcher) {
		t.Fatalf("first registration with no tray: %v, want ErrNoWatcher", err)
	}

	w := startWatcher(t, addr)
	if err := registration(t, it); err != nil {
		t.Fatalf("registration once the tray appeared: %v", err)
	}
	select {
	case name := <-w.got:
		if name != it.name {
			t.Errorf("registered %q, want %q", name, it.name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher was never called")
	}
}

func TestClicks(t *testing.T) {
	addr := dbustest.Start(t)
	it := export(t, addr)
	go func() {
		for range it.Registrations() {
		}
	}()
	client := dbustest.Conn(t, addr)

	for _, tc := range []struct {
		method string
		kind   ClickKind
	}{{"Activate", Activate}, {"SecondaryActivate", SecondaryActivate}, {"ContextMenu", ContextMenu}} {
		done := make(chan error, 1)
		go func() {
			done <- client.Object(it.name, Path).Call(Interface+"."+tc.method, 0, int32(1200), int32(30)).Err
		}()
		select {
		case c := <-it.Clicks():
			if c != (Click{Kind: tc.kind, X: 1200, Y: 30}) {
				t.Errorf("%s delivered %+v", tc.method, c)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s delivered nothing", tc.method)
		}
		if err := <-done; err != nil {
			t.Errorf("%s: %v", tc.method, err)
		}
	}

	if err := client.Object(it.name, Path).Call(Interface+".Scroll", 0, int32(1), "vertical").Err; err != nil {
		t.Errorf("Scroll: %v", err)
	}
}

func TestSetIcon(t *testing.T) {
	addr := dbustest.Start(t)
	it := export(t, addr)
	go func() {
		for range it.Registrations() {
		}
	}()
	client := dbustest.Conn(t, addr)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(Interface), dbus.WithMatchMember("NewIcon")); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 8)
	client.Signal(signals)

	want := []Pixmap{{Width: 1, Height: 1, Pix: []byte{0xff, 1, 2, 3}}}
	if err := it.SetIcon(want); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
wait:
	for {
		select {
		case sig := <-signals:
			if sig.Name == Interface+".NewIcon" {
				break wait
			}
		case <-deadline:
			t.Fatal("no NewIcon signal")
		}
	}

	v, err := client.Object(it.name, Path).GetProperty(Interface + ".IconPixmap")
	if err != nil {
		t.Fatal(err)
	}
	var got []Pixmap
	if err := dbus.Store([]any{v.Value()}, &got); err != nil {
		t.Fatalf("decoding IconPixmap %v: %v", v, err)
	}
	if len(got) != 1 || got[0].Width != 1 || !slices.Equal(got[0].Pix, want[0].Pix) {
		t.Errorf("IconPixmap = %+v, want %+v", got, want)
	}
}

func TestCloseTwice(t *testing.T) {
	addr := dbustest.Start(t)
	it, err := Export(dbustest.Conn(t, addr), &Options{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := it.Close(); err != nil {
		t.Fatal(err)
	}
	if err := it.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestFromImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	// Half-transparent pure red, premultiplied, and fully transparent.
	img.SetRGBA(0, 0, color.RGBA{R: 0x80, A: 0x80})
	img.SetRGBA(1, 0, color.RGBA{})
	got := FromImage(img)
	want := Pixmap{Width: 2, Height: 1, Pix: []byte{0x80, 0xff, 0, 0, 0, 0, 0, 0}}
	if got.Width != want.Width || got.Height != want.Height || !slices.Equal(got.Pix, want.Pix) {
		t.Errorf("FromImage = %+v, want %+v", got, want)
	}
}

// A sub-image starts somewhere other than the origin; the pixmap must still
// be its own pixels, starting at its own corner.
func TestFromImageSubImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.SetRGBA(2, 2, color.RGBA{G: 0xff, A: 0xff})
	sub, ok := img.SubImage(image.Rect(2, 2, 4, 4)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage of an RGBA is not an RGBA")
	}
	got := FromImage(sub)
	if got.Width != 2 || got.Height != 2 || !slices.Equal(got.Pix[:4], []byte{0xff, 0, 0xff, 0}) {
		t.Errorf("FromImage(sub) = %+v", got)
	}
}
