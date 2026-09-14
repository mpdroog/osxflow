package mpris

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakePlayer is one media player on a private bus, on a connection of its
// own so each can export the one object path MPRIS fixes.
type fakePlayer struct {
	mu    sync.Mutex
	calls []string
	props *prop.Properties
}

func (p *fakePlayer) record(method string) *dbus.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, method)
	return nil
}

func (p *fakePlayer) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

type playerMethods struct{ p *fakePlayer }

func (m *playerMethods) PlayPause() *dbus.Error { return m.p.record("PlayPause") }
func (m *playerMethods) Next() *dbus.Error      { return m.p.record("Next") }
func (m *playerMethods) Previous() *dbus.Error  { return m.p.record("Previous") }

func ro(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }

// startPlayer claims name on its own connection. With playerProps false it
// exports only the root interface, so reading the player's state fails.
func startPlayer(t *testing.T, addr, name, identity, status string, metadata map[string]dbus.Variant, playerProps bool) *fakePlayer {
	t.Helper()
	conn := dbustest.Conn(t, addr)
	p := &fakePlayer{}
	m := prop.Map{ifaceRoot: {"Identity": ro(identity)}}
	if playerProps {
		m[ifacePlayer] = map[string]*prop.Prop{
			"PlaybackStatus": ro(status),
			"Metadata":       ro(metadata),
			"CanPlay":        ro(true),
			"CanPause":       ro(true),
			"CanGoNext":      ro(true),
			"CanGoPrevious":  ro(false),
		}
		if err := conn.Export(&playerMethods{p}, ObjectPath, ifacePlayer); err != nil {
			t.Fatal(err)
		}
	}
	props, err := prop.Export(conn, ObjectPath, m)
	if err != nil {
		t.Fatal(err)
	}
	p.props = props
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", name, reply, err)
	}
	return p
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPlayers(t *testing.T) {
	addr := dbustest.Start(t)
	startPlayer(t, addr, NamePrefix+"vlc", "VLC media player", StatusPaused,
		map[string]dbus.Variant{"xesam:title": dbus.MakeVariant("Song")}, true)
	startPlayer(t, addr, NamePrefix+"firefox.instance_1_22", "Firefox", StatusPlaying,
		map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant("A talk\nwith a newline"),
			"xesam:artist": dbus.MakeVariant([]string{"Alice", "", "Bob"}),
		}, true)
	startPlayer(t, addr, NamePrefix+"broken", "Broken", "", nil, false)
	// Not a player, however close the name.
	startPlayer(t, addr, "org.mpris.MediaPlayer2", "Not a player", StatusPlaying, nil, true)

	c := New(dbustest.Conn(t, addr))
	players, err := c.Players(testContext(t))
	if err == nil || !strings.Contains(err.Error(), NamePrefix+"broken") {
		t.Errorf("Players error = %v, want one naming the broken player", err)
	}
	if len(players) != 2 {
		t.Fatalf("got %d players, want 2: %+v", len(players), players)
	}
	ff, vlc := &players[0], &players[1]
	if ff.Identity != "Firefox" || ff.Status != StatusPlaying || ff.Title != "A talk with a newline" ||
		ff.Artist != "Alice, Bob" || !ff.CanPlay || !ff.CanPause || !ff.CanGoNext || ff.CanGoPrevious {
		t.Errorf("first player = %+v", *ff)
	}
	if vlc.Identity != "VLC media player" || vlc.Status != StatusPaused || vlc.Title != "Song" || vlc.Artist != "" {
		t.Errorf("second player = %+v", *vlc)
	}
}

func TestPlayersNone(t *testing.T) {
	c := New(dbustest.Conn(t, dbustest.Start(t)))
	players, err := c.Players(testContext(t))
	if err != nil || len(players) != 0 {
		t.Errorf("Players on an empty bus = %+v, %v", players, err)
	}
}

func TestControls(t *testing.T) {
	addr := dbustest.Start(t)
	name := NamePrefix + "vlc"
	p := startPlayer(t, addr, name, "VLC", StatusPlaying, nil, true)
	c := New(dbustest.Conn(t, addr))
	ctx := testContext(t)
	for _, f := range []func(context.Context, string) error{c.PlayPause, c.Next, c.Previous} {
		if err := f(ctx, name); err != nil {
			t.Error(err)
		}
	}
	if got, want := p.recorded(), []string{"PlayPause", "Next", "Previous"}; !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if err := c.Next(ctx, "org.example.NotAPlayer"); err == nil {
		t.Error("Next on a name that is not a player succeeded")
	}
	if err := c.Next(ctx, NamePrefix+"gone"); err == nil {
		t.Error("Next on a player that is not there succeeded")
	}
}

func TestWatch(t *testing.T) {
	addr := dbustest.Start(t)
	p := startPlayer(t, addr, NamePrefix+"vlc", "VLC", StatusPlaying, nil, true)
	c := New(dbustest.Conn(t, addr))
	changed, err := c.Watch()
	if err != nil {
		t.Fatal(err)
	}
	drain(changed, 200*time.Millisecond)

	p.props.SetMust(ifacePlayer, "PlaybackStatus", StatusPaused)
	wait(t, changed, "a property change")

	startPlayer(t, addr, NamePrefix+"spotify", "Spotify", StatusStopped, nil, true)
	wait(t, changed, "a player appearing")
}

func drain(ch <-chan struct{}, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

func wait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatalf("watch closed instead of reporting %s", what)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no change reported after %s", what)
	}
}

func TestRelevant(t *testing.T) {
	for _, tc := range []struct {
		sig  *dbus.Signal
		want bool
	}{
		{nil, false},
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{NamePrefix + "vlc", "", ":1.5"}}, true},
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{"org.kde.StatusNotifierWatcher", "", ":1.5"}}, false},
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{42}}, false},
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged"}, false},
		{&dbus.Signal{Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Path: ObjectPath, Body: []any{ifacePlayer}}, true},
		{&dbus.Signal{Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Path: "/StatusNotifierItem", Body: []any{ifacePlayer}}, false},
		{&dbus.Signal{Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Path: ObjectPath, Body: []any{"org.example.Other"}}, false},
		{&dbus.Signal{Name: "org.kde.StatusNotifierItem.NewIcon"}, false},
	} {
		if got := Relevant(tc.sig); got != tc.want {
			t.Errorf("Relevant(%+v) = %t, want %t", tc.sig, got, tc.want)
		}
	}
}

func TestParseMetadata(t *testing.T) {
	for _, tc := range []struct {
		name          string
		value         any
		title, artist string
	}{
		{"not a map", "nope", "", ""},
		{"empty", map[string]dbus.Variant{}, "", ""},
		{"list artist", map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant(" Title "),
			"xesam:artist": dbus.MakeVariant([]string{"A", "B"}),
		}, "Title", "A, B"},
		{"string artist", map[string]dbus.Variant{"xesam:artist": dbus.MakeVariant("Solo")}, "", "Solo"},
		{"wrong types", map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant(int32(7)),
			"xesam:artist": dbus.MakeVariant([]int32{1}),
		}, "", ""},
		{"control characters", map[string]dbus.Variant{"xesam:title": dbus.MakeVariant("a\tb\x00c")}, "a b c", ""},
	} {
		title, artist := ParseMetadata(tc.value)
		if title != tc.title || artist != tc.artist {
			t.Errorf("%s: ParseMetadata = %q, %q; want %q, %q", tc.name, title, artist, tc.title, tc.artist)
		}
	}
}

func FuzzParseMetadata(f *testing.F) {
	f.Add("Title", "Artist", false)
	f.Add("", "A\x00B\nC", true)
	f.Add("\xff\xfe", "\u202e", true) // right-to-left override
	f.Fuzz(func(t *testing.T, title, artist string, asList bool) {
		md := map[string]dbus.Variant{"xesam:title": dbus.MakeVariant(title)}
		if asList {
			md["xesam:artist"] = dbus.MakeVariant(strings.Split(artist, ","))
		} else {
			md["xesam:artist"] = dbus.MakeVariant(artist)
		}
		gotTitle, gotArtist := ParseMetadata(md)
		for _, s := range []string{gotTitle, gotArtist} {
			if !utf8.ValidString(s) {
				t.Fatalf("output %q is not valid UTF-8", s)
			}
			if strings.IndexFunc(s, unicode.IsControl) >= 0 {
				t.Fatalf("output %q contains a control character", s)
			}
			if s != strings.TrimSpace(s) {
				t.Fatalf("output %q is not trimmed", s)
			}
		}
	})
}

func TestSort(t *testing.T) {
	players := []Player{
		{BusName: "d", Identity: "Zed", Status: StatusStopped},
		{BusName: "c", Identity: "Beta", Status: StatusPaused},
		{BusName: "b", Identity: "Alpha", Status: ""},
		{BusName: "a", Identity: "Mid", Status: StatusPlaying},
		{BusName: "e", Identity: "Alpha", Status: StatusPaused},
	}
	Sort(players)
	got := make([]string, len(players))
	for i := range players {
		got[i] = players[i].BusName
	}
	if want := []string{"a", "e", "c", "b", "d"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("plain"), false},
		{fmt.Errorf("w: %w", dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}), true},
		{fmt.Errorf("w: %w", &dbus.Error{Name: "org.freedesktop.DBus.Error.NameHasNoOwner"}), true},
		// What GLib players answer while starting, before their object is
		// exported: seen from Celluloid on 2026-09-14.
		{fmt.Errorf("w: %w", dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}), true},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownInterface"}, true},
		{dbus.Error{Name: "org.freedesktop.DBus.Properties.Error.InterfaceNotFound"}, false},
	} {
		if got := gone(tc.err); got != tc.want {
			t.Errorf("gone(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}

func TestIsPlayerName(t *testing.T) {
	for name, want := range map[string]bool{
		NamePrefix + "vlc":                   true,
		NamePrefix + "firefox.instance_1_22": true,
		NamePrefix:                           false,
		"org.mpris.MediaPlayer2":             false,
		"org.kde.StatusNotifierWatcher":      false,
	} {
		if got := IsPlayerName(name); got != want {
			t.Errorf("IsPlayerName(%q) = %t, want %t", name, got, want)
		}
	}
}
