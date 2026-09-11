package main

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/mpdroog/osxflow/internal/dbustest"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xfconf"
)

// testDaemon is a daemon with no display: enough for everything that only
// keeps state.
func testDaemon() *daemon {
	return &daemon{
		q:              notify.NewQueue(maxShown),
		defaultTimeout: defaultTimeout,
		failed:         make(attempts),
		lim:            &errlog.Limiter{Burst: logBurst, Per: logEvery},
	}
}

func TestApplySetting(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prop        string
		v           any
		removed     bool
		wantErr     string
		wantDND     bool
		wantTimeout time.Duration
	}{
		{name: "do-not-disturb on", prop: "/do-not-disturb", v: true, wantDND: true, wantTimeout: 9 * time.Second},
		{name: "do-not-disturb off", prop: "/do-not-disturb", v: false, wantTimeout: 9 * time.Second},
		{name: "do-not-disturb removed", prop: "/do-not-disturb", removed: true, wantTimeout: 9 * time.Second},
		// The starting state has do-not-disturb on: a value that is not a
		// boolean must not be read as "off".
		{name: "do-not-disturb of the wrong type", prop: "/do-not-disturb", v: "yes",
			wantErr: "is string, want bool", wantDND: true, wantTimeout: 9 * time.Second},
		{name: "do-not-disturb with no value", prop: "/do-not-disturb", v: nil,
			wantErr: "want bool", wantDND: true, wantTimeout: 9 * time.Second},
		{name: "timeout", prop: "/expire-timeout", v: int32(3), wantDND: true, wantTimeout: 3 * time.Second},
		{name: "timeout removed", prop: "/expire-timeout", removed: true, wantDND: true, wantTimeout: defaultTimeout},
		{name: "timeout of the wrong type", prop: "/expire-timeout", v: uint32(3),
			wantErr: "is uint32, want int32", wantDND: true, wantTimeout: 9 * time.Second},
		{name: "timeout not positive", prop: "/expire-timeout", v: int32(0),
			wantErr: "positive", wantDND: true, wantTimeout: 9 * time.Second},
		{name: "another property", prop: "/theme", v: "x", wantDND: true, wantTimeout: 9 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDaemon()
			d.q.DoNotDisturb, d.defaultTimeout = true, 9*time.Second
			err := d.applySetting(tc.prop, tc.v, tc.removed)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want one mentioning %q", err, tc.wantErr)
			}
			if d.q.DoNotDisturb != tc.wantDND || d.defaultTimeout != tc.wantTimeout {
				t.Errorf("do-not-disturb %t, timeout %v; want %t, %v",
					d.q.DoNotDisturb, d.defaultTimeout, tc.wantDND, tc.wantTimeout)
			}
		})
	}
}

// fakeXfconfd answers GetProperty from a fixed table, and PropertyNotFound
// for anything else, as xfconfd does.
type fakeXfconfd struct {
	props map[string]dbus.Variant
}

func (f *fakeXfconfd) GetProperty(channel, property string) (dbus.Variant, *dbus.Error) {
	if v, ok := f.props[channel+property]; ok {
		return v, nil
	}
	return dbus.Variant{}, &dbus.Error{Name: "org.xfce.Xfconf.Error.PropertyNotFound"}
}

func startXfconfd(t *testing.T, addr string, props map[string]dbus.Variant) *dbus.Conn {
	t.Helper()
	conn := dbustest.Conn(t, addr)
	if err := conn.Export(&fakeXfconfd{props: props}, xfconf.ObjectPath, xfconf.Interface); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(xfconf.BusName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestLoadSettingsReadsAndWatches(t *testing.T) {
	addr := dbustest.Start(t)
	server := startXfconfd(t, addr, map[string]dbus.Variant{
		settingsChannel + "/do-not-disturb": dbus.MakeVariant(true),
		settingsChannel + "/expire-timeout": dbus.MakeVariant(int32(8)),
	})
	d := testDaemon()
	d.bus = dbustest.Conn(t, addr)
	if err := d.loadSettings(); err != nil {
		t.Fatal(err)
	}
	if !d.q.DoNotDisturb || d.defaultTimeout != 8*time.Second {
		t.Fatalf("do-not-disturb %t, timeout %v; want what xfconfd holds", d.q.DoNotDisturb, d.defaultTimeout)
	}
	if err := server.Emit(xfconf.ObjectPath, xfconf.Interface+".PropertyChanged",
		settingsChannel, "/do-not-disturb", dbus.MakeVariant(false)); err != nil {
		t.Fatal(err)
	}
	select {
	case ch := <-d.settings:
		if ch.Err != nil || ch.Property != "/do-not-disturb" || ch.Value != false {
			t.Errorf("change = %+v, want do-not-disturb off", ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change was not delivered")
	}
}

// A setting that cannot be used is reported, and the rest carries on: the
// other property is still read and the channel still watched.
func TestLoadSettingsKeepsGoingPastABadValue(t *testing.T) {
	addr := dbustest.Start(t)
	startXfconfd(t, addr, map[string]dbus.Variant{
		settingsChannel + "/do-not-disturb": dbus.MakeVariant("yes"),
		settingsChannel + "/expire-timeout": dbus.MakeVariant(int32(8)),
	})
	d := testDaemon()
	d.bus = dbustest.Conn(t, addr)
	err := d.loadSettings()
	if err == nil || !strings.Contains(err.Error(), "want bool") {
		t.Errorf("err = %v, want the bad do-not-disturb reported", err)
	}
	if d.defaultTimeout != 8*time.Second {
		t.Errorf("timeout %v: the good property was not read", d.defaultTimeout)
	}
	if d.settings == nil {
		t.Error("the channel is not being watched")
	}
}

// Without xfconfd there is nothing wrong to report: the defaults apply, and
// the watch is set up anyway, for an xfconfd that starts later.
func TestLoadSettingsWithoutXfconfd(t *testing.T) {
	d := testDaemon()
	d.bus = dbustest.Conn(t, dbustest.Start(t))
	if err := d.loadSettings(); err != nil {
		t.Errorf("err = %v, want none", err)
	}
	if d.q.DoNotDisturb || d.defaultTimeout != defaultTimeout {
		t.Errorf("do-not-disturb %t, timeout %v; want the defaults", d.q.DoNotDisturb, d.defaultTimeout)
	}
	if d.settings == nil {
		t.Error("the channel is not being watched")
	}
}

func TestWorkAreaFrom(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	for _, tc := range []struct {
		name    string
		vals    []uint32
		desktop uint32
		want    image.Rectangle
		wantErr string
	}{
		{name: "one desktop", vals: []uint32{0, 32, 1920, 1048}, want: image.Rect(0, 32, 1920, 1080)},
		{name: "second desktop", vals: []uint32{0, 32, 1920, 1048, 0, 0, 1920, 1000}, desktop: 1,
			want: image.Rect(0, 0, 1920, 1000)},
		{name: "clipped to the screen", vals: []uint32{0, 0, 4000, 4000}, want: full},
		{name: "empty", vals: nil, want: full, wantErr: "0 values"},
		{name: "ragged", vals: []uint32{0, 32, 1920, 1048, 7}, want: image.Rect(0, 32, 1920, 1080), wantErr: "extra"},
		{name: "desktop out of range", vals: []uint32{0, 32, 1920, 1048}, desktop: 3,
			want: image.Rect(0, 32, 1920, 1080), wantErr: "covers 1"},
		{name: "huge desktop", vals: []uint32{0, 32, 1920, 1048}, desktop: 1 << 31,
			want: image.Rect(0, 32, 1920, 1080), wantErr: "covers 1"},
		{name: "off the screen", vals: []uint32{5000, 5000, 10, 10}, want: full, wantErr: "misses"},
		{name: "zero size", vals: []uint32{0, 0, 0, 0}, want: full, wantErr: "misses"},
		{name: "values past int32", vals: []uint32{0, 0, 1 << 31, 1 << 31}, want: full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workAreaFrom(full, tc.vals, tc.desktop)
			if got != tc.want {
				t.Errorf("area = %v, want %v", got, tc.want)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestAttemptsGiveUpAfterTheLimit(t *testing.T) {
	a := make(attempts)
	for i := 1; i < maxAttempts; i++ {
		if a.fail(7) {
			t.Fatalf("gave up after %d failures, want %d", i, maxAttempts)
		}
	}
	if !a.fail(7) {
		t.Fatalf("still trying after %d failures", maxAttempts)
	}
	if a[7] != 0 {
		t.Errorf("count %d left behind after giving up", a[7])
	}
}

func TestAttemptsForgetWhatIsNoLongerShown(t *testing.T) {
	a := make(attempts)
	a.fail(1)
	a.fail(2)
	a.keep(map[uint32]bool{2: true})
	if _, ok := a[1]; ok {
		t.Error("a notification no longer shown kept its count")
	}
	if a[2] != 1 {
		t.Errorf("a shown notification's count is %d, want 1", a[2])
	}
}

func TestDestroyedMeansOnlyAWindowThatHasGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{xproto.WindowError{}, true},
		{xproto.DrawableError{}, true},
		{fmt.Errorf("inspecting: %w", xproto.WindowError{}), true},
		{xproto.AccessError{}, false},
		{xproto.AllocError{}, false},
		{errors.New("connection reset"), false},
	} {
		if got := destroyed(tc.err); got != tc.want {
			t.Errorf("destroyed(%T) = %t, want %t", tc.err, got, tc.want)
		}
	}
}

func TestIconFallbackExpected(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "icon.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.png")
	if err := os.WriteFile(broken, []byte("\x89PNG\r\n\x1a\n\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "missing.png"), true},
		{svg, true},
		{broken, false},
		{dir, false},
	} {
		_, err := notify.LoadImageFile(tc.path)
		if err == nil {
			t.Fatalf("%s loaded", tc.path)
		}
		if got := iconFallbackExpected(err); got != tc.want {
			t.Errorf("%s: iconFallbackExpected(%v) = %t, want %t", filepath.Base(tc.path), err, got, tc.want)
		}
	}
}

// noGlyphs is a face that has no glyph for anything.
type noGlyphs struct{ font.Face }

func (noGlyphs) GlyphAdvance(rune) (fixed.Int26_6, bool) { return 0, false }

func TestCloseMark(t *testing.T) {
	if mark, ok := closeMark(noGlyphs{}); ok || mark != "x" {
		t.Errorf("font without ×: mark %q, ok %t; want a plain x and false", mark, ok)
	}

	f, path, err := text.Load(text.Candidates)
	if err != nil {
		t.Skipf("no system font to try: %v", err)
	}
	face, err := text.Face(f, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := face.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	// Every candidate font has the multiplication sign.
	if mark, ok := closeMark(face); !ok || mark != string(preferredCloseMark) {
		t.Errorf("%s: mark %q, ok %t; want %q", path, mark, ok, preferredCloseMark)
	}
}
