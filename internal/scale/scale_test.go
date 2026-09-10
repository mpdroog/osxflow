package scale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectOverrideWins(t *testing.T) {
	t.Setenv("GDK_SCALE", "3")
	if got := Detect(nil, 1.25); got != 1.25 {
		t.Errorf("Detect = %g, want the 1.25 override", got)
	}
}

func TestDetectFallsBackToOne(t *testing.T) {
	t.Setenv("GDK_SCALE", "")
	t.Setenv("HOME", t.TempDir())
	if got := Detect(nil, 0); got != 1 {
		t.Errorf("Detect = %g, want 1 when nothing says otherwise", got)
	}
}

func TestFromEnv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"2", 2, true},
		{" 2 ", 2, true},
		{"1.5", 1.5, true},
		{"0", 0, false},
		{"-1", 0, false},
		{"two", 0, false},
		{"", 0, false},
	} {
		t.Setenv("GDK_SCALE", tc.in)
		got, ok := FromEnv()
		if ok != tc.ok || got != tc.want {
			t.Errorf("GDK_SCALE=%q: got %g, %v; want %g, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestFromXfconfFile covers the source that actually answers on this
// desktop: XFCE records the scale factor here and sets no Xft.dpi at all,
// so without this the dock would draw at half size.
func TestFromXfconfFile(t *testing.T) {
	const scaled = `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Gdk" type="empty">
    <property name="WindowScalingFactor" type="int" value="2"/>
  </property>
</channel>`
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

	if got, ok := FromXfconfFile(write("scaled.xml", scaled)); !ok || got != 2 {
		t.Errorf("scaled: got %g, %v; want 2, true", got, ok)
	}
	if got, ok := FromXfconfFile(write("unscaled.xml", unscaled)); !ok || got != 1 {
		t.Errorf("unscaled: got %g, %v; want 1, true", got, ok)
	}

	for name, content := range map[string]string{
		"missing-key.xml": `<channel name="xsettings"><property name="Gdk" type="empty"/></channel>`,
		"malformed.xml":   `<channel`,
		"wrong-type.xml":  `<channel><property name="Gdk"><property name="WindowScalingFactor" type="string" value="2"/></property></channel>`,
		"zero.xml":        `<channel><property name="Gdk"><property name="WindowScalingFactor" type="int" value="0"/></property></channel>`,
		"empty.xml":       ``,
	} {
		if _, ok := FromXfconfFile(write(name, content)); ok {
			t.Errorf("%s: reported a scale it should not have found", name)
		}
	}

	if _, ok := FromXfconfFile(filepath.Join(dir, "does-not-exist.xml")); ok {
		t.Error("a missing file reported a scale")
	}
}
