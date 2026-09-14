package backlight

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// device lays out one device the way sysfs does: the real directory lives
// elsewhere and the class directory holds a symlink to it.
func device(t *testing.T, root, subsystem, name string, files map[string]string) {
	t.Helper()
	target := filepath.Join(root, "devices", subsystem, strings.ReplaceAll(name, ":", "_"))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, content := range files {
		if err := os.WriteFile(filepath.Join(target, f), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	class := filepath.Join(root, "class", subsystem)
	if err := os.MkdirAll(class, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(class, name)); err != nil {
		t.Fatal(err)
	}
}

func TestScreenPrefersFirmware(t *testing.T) {
	root := t.TempDir()
	device(t, root, "backlight", "intel_backlight", map[string]string{"type": "raw\n", "max_brightness": "1000\n", "brightness": "500\n"})
	device(t, root, "backlight", "acpi_video0", map[string]string{"type": "firmware\n", "max_brightness": "90\n", "brightness": "9\n"})
	device(t, root, "backlight", "apple_bl", map[string]string{"type": "platform\n", "max_brightness": "15\n", "brightness": "3\n"})

	d, err := Sysfs{Root: filepath.Join(root, "class")}.Screen()
	if err != nil {
		t.Fatalf("Screen: %v", err)
	}
	want := Device{Subsystem: "backlight", Name: "acpi_video0", Max: 90, Brightness: 9, MinRaw: 1}
	if *d != want {
		t.Errorf("Screen = %+v, want %+v", *d, want)
	}
}

func TestScreenPlatformOverRaw(t *testing.T) {
	root := t.TempDir()
	device(t, root, "backlight", "intel_backlight", map[string]string{"type": "raw", "max_brightness": "1000", "brightness": "500"})
	device(t, root, "backlight", "apple_bl", map[string]string{"type": "platform", "max_brightness": "15", "brightness": "3"})
	d, err := Sysfs{Root: filepath.Join(root, "class")}.Screen()
	if err != nil || d.Name != "apple_bl" {
		t.Errorf("Screen = %+v, %v; want apple_bl", d, err)
	}
}

func TestScreenActualBrightness(t *testing.T) {
	root := t.TempDir()
	device(t, root, "backlight", "acpi_video0", map[string]string{
		"type": "firmware", "max_brightness": "90", "brightness": "40", "actual_brightness": "35",
	})
	s := Sysfs{Root: filepath.Join(root, "class")}
	d, err := s.Screen()
	if err != nil {
		t.Fatal(err)
	}
	if d.Brightness != 35 {
		t.Errorf("Brightness = %d, want actual_brightness 35", d.Brightness)
	}

	// Read refreshes after a brightness key.
	if err := os.WriteFile(filepath.Join(root, "devices", "backlight", "acpi_video0", "actual_brightness"), []byte("60\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(d); err != nil {
		t.Fatal(err)
	}
	if d.Brightness != 60 {
		t.Errorf("after Read, Brightness = %d, want 60", d.Brightness)
	}
}

func TestScreenNone(t *testing.T) {
	if _, err := (Sysfs{Root: t.TempDir()}).Screen(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Screen with no backlight class: %v, want ErrNotFound", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backlight"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Sysfs{Root: root}).Screen(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Screen with an empty backlight class: %v, want ErrNotFound", err)
	}
}

// An unreadable device is passed over for a readable one, and reported.
func TestScreenGarbage(t *testing.T) {
	root := t.TempDir()
	device(t, root, "backlight", "acpi_video0", map[string]string{"type": "firmware", "max_brightness": "ninety", "brightness": "9"})
	device(t, root, "backlight", "intel_backlight", map[string]string{"type": "raw", "max_brightness": "1000", "brightness": "500"})
	d, err := Sysfs{Root: filepath.Join(root, "class")}.Screen()
	if d == nil || d.Name != "intel_backlight" {
		t.Fatalf("Screen = %+v, want intel_backlight", d)
	}
	if err == nil || !strings.Contains(err.Error(), "ninety") {
		t.Errorf("Screen error = %v, want the unreadable acpi_video0 reported", err)
	}

	// With nothing readable, the error says why rather than "not found".
	root = t.TempDir()
	device(t, root, "backlight", "acpi_video0", map[string]string{"type": "firmware", "max_brightness": "90", "brightness": "-3"})
	device(t, root, "backlight", "odd", map[string]string{"type": "hologram", "max_brightness": "90", "brightness": "3"})
	device(t, root, "backlight", "untyped", map[string]string{"max_brightness": "90", "brightness": "3"})
	d, err = Sysfs{Root: filepath.Join(root, "class")}.Screen()
	if d != nil || err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Screen with nothing readable = %+v, %v; want nil and a reason", d, err)
	}
	for _, want := range []string{"negative", "hologram", "type"} {
		if err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("Screen error %v does not mention %q", err, want)
		}
	}
}

// An actual_brightness that exists but cannot be read is a failure, not a
// reason to fall back quietly.
func TestReadActualBrightnessUnreadable(t *testing.T) {
	root := t.TempDir()
	device(t, root, "backlight", "acpi_video0", map[string]string{"type": "firmware", "max_brightness": "90", "brightness": "9"})
	// A directory where a file should be reads as an error other than
	// "does not exist".
	if err := os.Mkdir(filepath.Join(root, "devices", "backlight", "acpi_video0", "actual_brightness"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Device{Subsystem: SubsystemBacklight, Name: "acpi_video0", Max: 90}
	err := Sysfs{Root: filepath.Join(root, "class")}.Read(d)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read = %v, want the unreadable actual_brightness reported", err)
	}
}

func TestKeyboard(t *testing.T) {
	root := t.TempDir()
	device(t, root, "leds", "input3::capslock", map[string]string{"max_brightness": "1", "brightness": "0"})
	device(t, root, "leds", "spi::kbd_backlight", map[string]string{"max_brightness": "255\n", "brightness": "36\n"})
	d, err := Sysfs{Root: filepath.Join(root, "class")}.Keyboard()
	if err != nil {
		t.Fatalf("Keyboard: %v", err)
	}
	want := Device{Subsystem: "leds", Name: "spi::kbd_backlight", Max: 255, Brightness: 36, MinRaw: 0}
	if *d != want {
		t.Errorf("Keyboard = %+v, want %+v", *d, want)
	}
}

func TestKeyboardAbsent(t *testing.T) {
	root := t.TempDir()
	device(t, root, "leds", "input3::capslock", map[string]string{"max_brightness": "1", "brightness": "0"})
	if _, err := (Sysfs{Root: filepath.Join(root, "class")}).Keyboard(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Keyboard with only a caps lock LED: %v, want ErrNotFound", err)
	}
	if _, err := (Sysfs{Root: t.TempDir()}).Keyboard(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Keyboard with no leds class: %v, want ErrNotFound", err)
	}

	root = t.TempDir()
	device(t, root, "leds", "spi::kbd_backlight", map[string]string{"max_brightness": "255"})
	d, err := Sysfs{Root: filepath.Join(root, "class")}.Keyboard()
	if d != nil || err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Keyboard with an unreadable light = %+v, %v; want nil and a reason", d, err)
	}
}

func TestFractionAndRawFor(t *testing.T) {
	screen := Device{Max: 90, Brightness: 9, MinRaw: 1}
	if got := screen.Fraction(); math.Abs(got-0.1) > 1e-9 {
		t.Errorf("Fraction = %v, want 0.1", got)
	}
	for _, tc := range []struct {
		fraction float64
		want     int
	}{
		{0.5, 45},
		{0, 1}, // never black
		{-3, 1},
		{1, 90},
		{7, 90},
		{math.NaN(), 1},
		{0.004, 1},
		{0.994, 89},
	} {
		if got := screen.RawFor(tc.fraction); got != tc.want {
			t.Errorf("screen RawFor(%v) = %d, want %d", tc.fraction, got, tc.want)
		}
	}

	keyboard := Device{Max: 255}
	if got := keyboard.RawFor(0); got != 0 {
		t.Errorf("keyboard RawFor(0) = %d, want 0 (a keyboard may go dark)", got)
	}
	var broken Device
	if broken.Fraction() != 0 || broken.RawFor(0.5) != 0 {
		t.Errorf("a device with no maximum: Fraction %v RawFor %d, want 0 and 0", broken.Fraction(), broken.RawFor(0.5))
	}
	over := Device{Max: 10, Brightness: 30}
	if over.Fraction() != 1 {
		t.Errorf("Fraction with brightness above max = %v, want 1", over.Fraction())
	}
}

func TestParseValue(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"90\n", 90, true},
		{"  0 ", 0, true},
		{"", 0, false},
		{"\n", 0, false},
		{"-1", 0, false},
		{"12abc", 0, false},
		{"0x10", 0, false},
		{"99999999999999999999999", 0, false},
	} {
		got, err := parseValue([]byte(tc.in))
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseValue(%q) = %d, %v; want %d, ok %t", tc.in, got, err, tc.want, tc.ok)
		}
	}
	_, err := parseValue([]byte(strings.Repeat("x", 500)))
	if err == nil || len(err.Error()) > 200 {
		t.Errorf("parseValue(garbage) error = %q, want a short one", err)
	}
}

func FuzzParseValue(f *testing.F) {
	for _, s := range []string{"90\n", "0", "", "-1", " 255 ", "abc", "1e3"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := parseValue(b)
		if err != nil {
			if v != 0 {
				t.Fatalf("parseValue(%q) = %d with error %v; want 0", b, v, err)
			}
			return
		}
		if v < 0 {
			t.Fatalf("parseValue(%q) = %d, negative", b, v)
		}
		if back, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr != nil || back != v {
			t.Fatalf("parseValue(%q) = %d, but the trimmed input reads as %d, %v", b, v, back, convErr)
		}
	})
}

// fakeSession is logind's session object, recording what it was asked.
type fakeSession struct {
	mu    sync.Mutex
	calls []string
}

func (s *fakeSession) SetBrightness(subsystem, name string, value uint32) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, subsystem+" "+name+" "+strconv.FormatUint(uint64(value), 10))
	return nil
}

func (s *fakeSession) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSetter(t *testing.T) {
	addr := dbustest.Start(t)
	server := dbustest.Conn(t, addr)
	session := &fakeSession{}
	if err := server.Export(session, sessionPath, "org.freedesktop.login1.Session"); err != nil {
		t.Fatal(err)
	}
	if reply, err := server.RequestName(login1Name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", login1Name, reply, err)
	}
	s := NewSetter(dbustest.Conn(t, addr))
	ctx := testContext(t)

	screen := &Device{Subsystem: SubsystemBacklight, Name: "acpi_video0", Max: 90, Brightness: 9, MinRaw: 1}
	keyboard := &Device{Subsystem: SubsystemLEDs, Name: "spi::kbd_backlight", Max: 255, Brightness: 36}
	for _, tc := range []struct {
		d    *Device
		raw  int
		want int
	}{
		{screen, 45, 45},
		{screen, 0, 1},    // clamped to MinRaw
		{screen, 500, 90}, // clamped to Max
		{keyboard, 0, 0},
		{keyboard, -7, 0},
	} {
		if err := s.Set(ctx, tc.d, tc.raw); err != nil {
			t.Fatalf("Set(%s, %d): %v", tc.d.Name, tc.raw, err)
		}
		if tc.d.Brightness != tc.want {
			t.Errorf("after Set(%s, %d), Brightness = %d, want %d", tc.d.Name, tc.raw, tc.d.Brightness, tc.want)
		}
	}
	want := []string{
		"backlight acpi_video0 45",
		"backlight acpi_video0 1",
		"backlight acpi_video0 90",
		"leds spi::kbd_backlight 0",
		"leds spi::kbd_backlight 0",
	}
	if got := session.recorded(); !slices.Equal(got, want) {
		t.Errorf("SetBrightness calls:\n got %q\nwant %q", got, want)
	}
}

// A refused call leaves the device as it was, so a slider does not show a
// brightness the screen never got.
func TestSetterFailureLeavesDevice(t *testing.T) {
	s := NewSetter(dbustest.Conn(t, dbustest.Start(t)))
	d := &Device{Subsystem: SubsystemBacklight, Name: "acpi_video0", Max: 90, Brightness: 9, MinRaw: 1}
	if err := s.Set(testContext(t), d, 45); err == nil {
		t.Fatal("Set with no logind on the bus succeeded")
	}
	if d.Brightness != 9 {
		t.Errorf("Brightness = %d after a failed Set, want 9 unchanged", d.Brightness)
	}
}

func TestToUint32(t *testing.T) {
	if toUint32(-1) != 0 || toUint32(90) != 90 || toUint32(math.MaxInt) != math.MaxUint32 {
		t.Errorf("toUint32 does not clamp: %d %d %d", toUint32(-1), toUint32(90), toUint32(math.MaxInt))
	}
}
