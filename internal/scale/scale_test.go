package scale

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// unsetEnv removes a variable for the duration of a test. t.Setenv first,
// so that it is restored afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetting %s: %v", key, err)
	}
}

// writeXsettings puts an xsettings.xml where FromXfconf looks for it,
// under a fresh HOME.
func writeXsettings(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "xfce4", "xfconf", "xfce-perchannel-xml")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xsettings.xml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const scaledXsettings = `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Gdk" type="empty">
    <property name="WindowScalingFactor" type="int" value="2"/>
  </property>
</channel>`

func TestDetectOverrideWins(t *testing.T) {
	t.Setenv("GDK_SCALE", "3")
	got, err := Detect(nil, 1.25)
	if err != nil || got != 1.25 {
		t.Errorf("Detect = %g, %v; want the 1.25 override", got, err)
	}
}

// A nonsensical -scale is reported and then ignored, not drawn at.
func TestDetectRejectsABadOverride(t *testing.T) {
	for _, override := range []float64{-1, math.NaN(), math.Inf(1)} {
		t.Setenv("GDK_SCALE", "2")
		got, err := Detect(nil, override)
		if got != 2 {
			t.Errorf("Detect(override %g) = %g, want GDK_SCALE's 2", override, got)
		}
		if err == nil || errors.Is(err, ErrUnset) {
			t.Errorf("Detect(override %g) error = %v, want it reported", override, err)
		}
	}
}

func TestDetectFallsBackToOne(t *testing.T) {
	t.Setenv("GDK_SCALE", "")
	t.Setenv("HOME", t.TempDir())
	got, err := Detect(nil, 0)
	if got != 1 {
		t.Errorf("Detect = %g, want 1 when nothing says otherwise", got)
	}
	// Nothing answering is an unscaled desktop, not a fault: it must not
	// put a line in the log on every start.
	if err != nil {
		t.Errorf("Detect error = %v, want none when nothing is configured", err)
	}
}

// A broken GDK_SCALE must neither win nor vanish: the next source answers,
// and the broken one is still reported.
func TestDetectReportsABrokenSourceItSkipped(t *testing.T) {
	t.Setenv("GDK_SCALE", "two")
	writeXsettings(t, scaledXsettings)
	got, err := Detect(nil, 0)
	if got != 2 {
		t.Errorf("Detect = %g, want xfconf's 2", got)
	}
	if err == nil || errors.Is(err, ErrUnset) {
		t.Errorf("Detect error = %v, want the broken GDK_SCALE reported", err)
	}
}

// On this desktop GDK_SCALE and Xft.dpi are both absent and xfconf
// answers; that must be silent.
func TestDetectQuietWhenOneSourceAnswers(t *testing.T) {
	unsetEnv(t, "GDK_SCALE")
	writeXsettings(t, scaledXsettings)
	got, err := Detect(nil, 0)
	if err != nil || got != 2 {
		t.Errorf("Detect = %g, %v; want 2, nil", got, err)
	}
}

func TestFromEnv(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  float64
		unset bool // want ErrUnset
		bad   bool // want some other error
	}{
		{"2", 2, false, false},
		{" 2 ", 2, false, false},
		{"1.5", 1.5, false, false},
		{"", 0, true, false},
		{"   ", 0, true, false},
		{"0", 0, false, true},
		{"-1", 0, false, true},
		{"two", 0, false, true},
		{"NaN", 0, false, true},
		{"nan", 0, false, true},
		{"Inf", 0, false, true},
		{"-Inf", 0, false, true},
		{"1e400", 0, false, true}, // overflows to Inf
	} {
		t.Setenv("GDK_SCALE", tc.in)
		got, err := FromEnv()
		switch {
		case tc.unset:
			if !errors.Is(err, ErrUnset) {
				t.Errorf("GDK_SCALE=%q: got %g, %v; want ErrUnset", tc.in, got, err)
			}
		case tc.bad:
			if err == nil || errors.Is(err, ErrUnset) {
				t.Errorf("GDK_SCALE=%q: got %g, %v; want a descriptive error", tc.in, got, err)
			}
		default:
			if err != nil || got != tc.want {
				t.Errorf("GDK_SCALE=%q: got %g, %v; want %g", tc.in, got, err, tc.want)
			}
		}
	}

	unsetEnv(t, "GDK_SCALE")
	if _, err := FromEnv(); !errors.Is(err, ErrUnset) {
		t.Errorf("GDK_SCALE unset: error = %v, want ErrUnset", err)
	}
}

func TestFromResourcesWithoutX(t *testing.T) {
	if _, err := FromResources(nil); !errors.Is(err, ErrUnset) {
		t.Errorf("FromResources(nil) error = %v, want ErrUnset", err)
	}
}

func TestFromResourceString(t *testing.T) {
	for _, tc := range []struct {
		name  string
		res   string
		want  float64
		unset bool
		bad   bool
	}{
		{"scaled", "Xft.antialias:\t1\nXft.dpi:\t192\n", 2, false, false},
		{"unscaled", "Xft.dpi: 96", 1, false, false},
		{"no dpi", "Xft.antialias:\t1\nXcursor.size:\t48\n", 0, true, false},
		{"empty", "", 0, true, false},
		{"garbage", "Xft.dpi:\tlarge\n", 0, false, true},
		{"zero", "Xft.dpi:\t0\n", 0, false, true},
		{"nan", "Xft.dpi:\tNaN\n", 0, false, true},
		{"inf", "Xft.dpi:\t+Inf\n", 0, false, true},
	} {
		got, err := fromResourceString(tc.res)
		switch {
		case tc.unset:
			if !errors.Is(err, ErrUnset) {
				t.Errorf("%s: got %g, %v; want ErrUnset", tc.name, got, err)
			}
		case tc.bad:
			if err == nil || errors.Is(err, ErrUnset) {
				t.Errorf("%s: got %g, %v; want a descriptive error", tc.name, got, err)
			}
		default:
			if err != nil || got != tc.want {
				t.Errorf("%s: got %g, %v; want %g", tc.name, got, err, tc.want)
			}
		}
	}
}

// TestFromXfconfFile covers the source that actually answers on this
// desktop: XFCE records the scale factor here and sets no Xft.dpi at all,
// so without this the dock would draw at half size.
func TestFromXfconfFile(t *testing.T) {
	const unscaled = `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Gdk" type="empty">
    <property name="WindowScalingFactor" type="int" value="1"/>
  </property>
</channel>`

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if got, err := FromXfconfFile(write("scaled.xml", scaledXsettings)); err != nil || got != 2 {
		t.Errorf("scaled: got %g, %v; want 2, nil", got, err)
	}
	if got, err := FromXfconfFile(write("unscaled.xml", unscaled)); err != nil || got != 1 {
		t.Errorf("unscaled: got %g, %v; want 1, nil", got, err)
	}

	// Not configuring a scale is not a fault.
	if _, err := FromXfconfFile(write("missing-key.xml",
		`<channel name="xsettings"><property name="Gdk" type="empty"/></channel>`)); !errors.Is(err, ErrUnset) {
		t.Errorf("missing-key.xml: error = %v, want ErrUnset", err)
	}
	if _, err := FromXfconfFile(filepath.Join(dir, "does-not-exist.xml")); !errors.Is(err, ErrUnset) {
		t.Errorf("does-not-exist.xml: error = %v, want ErrUnset", err)
	}

	// Configuring one badly is.
	for name, content := range map[string]string{
		"malformed.xml":  `<channel`,
		"wrong-type.xml": `<channel><property name="Gdk"><property name="WindowScalingFactor" type="string" value="2"/></property></channel>`,
		"fraction.xml":   `<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="1.5"/></property></channel>`,
		"zero.xml":       `<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="0"/></property></channel>`,
		"negative.xml":   `<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="-2"/></property></channel>`,
		"nan.xml":        `<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="NaN"/></property></channel>`,
		"empty.xml":      ``,
	} {
		got, err := FromXfconfFile(write(name, content))
		if err == nil || errors.Is(err, ErrUnset) {
			t.Errorf("%s: got %g, %v; want a descriptive error", name, got, err)
		}
	}

	// A file that exists but cannot be read is not "unset" either.
	if os.Geteuid() != 0 {
		locked := write("locked.xml", scaledXsettings)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := FromXfconfFile(locked); err == nil || errors.Is(err, ErrUnset) {
			t.Errorf("unreadable file: error = %v, want a descriptive error", err)
		}
	}
}

// Without a home directory there is no telling where the file is, and
// that is a broken environment rather than an unset scale.
func TestFromXfconfWithoutHome(t *testing.T) {
	unsetEnv(t, "HOME")
	if _, err := FromXfconf(); err == nil || errors.Is(err, ErrUnset) {
		t.Errorf("FromXfconf with no HOME: error = %v, want a descriptive error", err)
	}
}

// FuzzParseXfconf checks the xsettings parser never returns a scale that
// cannot be drawn at, whatever is in the file.
func FuzzParseXfconf(f *testing.F) {
	f.Add([]byte(scaledXsettings))
	f.Add([]byte(`<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="0"/></property></channel>`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := parseXfconf(data)
		if err != nil {
			return
		}
		if check(s) != nil {
			t.Errorf("parseXfconf(%q) = %g, which is not a usable scale", data, s)
		}
	})
}

// FuzzFromResourceString does the same for the resource database.
func FuzzFromResourceString(f *testing.F) {
	f.Add("Xft.dpi:\t192\n")
	f.Add("Xft.dpi:\tNaN\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, res string) {
		s, err := fromResourceString(res)
		if err != nil {
			return
		}
		if check(s) != nil {
			t.Errorf("fromResourceString(%q) = %g, which is not a usable scale", res, s)
		}
	})
}
