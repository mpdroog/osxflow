package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xfconf"
)

// fakeSystem is a desktop as the tests describe it.
type fakeSystem struct {
	procs    map[string]bool
	procsErr error
	owners   map[string]string // bus name -> owning process
	busErr   error
	xfconf   map[string]map[string]any // channel -> properties
	xfErr    error
	path     map[string]bool
	env      map[string]string
}

func (f *fakeSystem) Processes() (map[string]bool, error) { return f.procs, f.procsErr }

func (f *fakeSystem) BusOwner(name string) (comm string, owned bool, err error) {
	if f.busErr != nil {
		return "", false, f.busErr
	}
	comm, ok := f.owners[name]
	return comm, ok, nil
}

func (f *fakeSystem) XfconfAll(channel, base string) (map[string]any, error) {
	if f.xfErr != nil {
		return nil, f.xfErr
	}
	out := map[string]any{}
	for k, v := range f.xfconf[channel] {
		if strings.HasPrefix(k, base) {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeSystem) LookPath(name string) (string, error) {
	if f.path[name] {
		return "/usr/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

func (f *fakeSystem) Getenv(name string) string { return f.env[name] }

// sub is a path under base given with slashes.
func sub(base, rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// healthy builds a home, a system root and a system that pass every check,
// as this desktop did on 2026-09-24. Tests then break one thing each.
func healthy(t *testing.T) (*doctor, *fakeSystem) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()

	for _, tool := range ourTools {
		write(t, filepath.Join(sub(home, ".local/bin"), tool.name), "#!/bin/sh\n", 0o755)
		if tool.autostart {
			write(t, filepath.Join(sub(home, ".config/autostart"), tool.name+".desktop"),
				fmt.Sprintf("[Desktop Entry]\nType=Application\nExec=%s\n", filepath.Join(sub(home, ".local/bin"), tool.name)), 0o644)
		}
	}
	for _, o := range dbusOverrides {
		target := strings.Replace(o.exec, "~", home, 1)
		write(t, filepath.Join(sub(home, ".local/share/dbus-1/services"), o.name+".service"),
			fmt.Sprintf("[D-BUS Service]\nName=%s\nExec=%s\n", o.name, target), 0o644)
		write(t, filepath.Join(sub(root, "usr/share/dbus-1/services"), "system-"+o.name+".service"),
			fmt.Sprintf("[D-BUS Service]\nName=%s\nExec=/usr/bin/whatever\n", o.name), 0o644)
	}
	for _, m := range masks {
		link := filepath.Join(sub(home, ".config/systemd/user"), m.unit)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/null", link); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(sub(root, "usr/lib/systemd/user"), m.unit), "[Unit]\n", 0o644)
	}
	for _, h := range hiddenAutostart {
		write(t, filepath.Join(sub(home, ".config/autostart"), h.file), "[Desktop Entry]\nHidden=true\n", 0o644)
	}
	for _, name := range knownAutostart {
		write(t, filepath.Join(sub(root, "etc/xdg/autostart"), name), "[Desktop Entry]\nName=x\n", 0o644)
	}
	write(t, sub(home, ".config/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml"),
		`<channel name="xsettings"><property name="Gdk" type="empty">`+
			`<property name="WindowScalingFactor" type="int" value="2"/></property></channel>`, 0o644)
	for _, fonts := range [][]string{text.Candidates, text.BoldCandidates} {
		src, err := os.ReadFile(realFont(t, fonts))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, fonts[0]), string(src), 0o644)
	}
	write(t, filepath.Join(sub(home, ".local/share/applications"), unpackDesktop),
		"[Desktop Entry]\nMimeType=application/zip;application/x-tar;\n", 0o644)
	write(t, sub(home, ".config/mimeapps.list"),
		"[Default Applications]\napplication/zip=unpack.desktop\napplication/x-tar=unpack.desktop;org.gnome.FileRoller.desktop;\n", 0o644)

	procs := map[string]bool{}
	for _, tool := range ourTools {
		if tool.running {
			procs[tool.name] = true
		}
	}
	for _, c := range companions {
		procs[c.comm] = true
	}
	path := map[string]bool{"xfce4-screenshooter": true}
	for _, c := range commands {
		path[strings.Split(c.names, "|")[0]] = true
	}
	sys := &fakeSystem{
		procs:  procs,
		owners: map[string]string{"org.kde.StatusNotifierWatcher": "menubar"},
		xfconf: map[string]map[string]any{
			"xfce4-session": {
				"/general/SaveOnExit":                false,
				"/sessions/Failsafe/Count":           int32(3),
				"/sessions/Failsafe/Client0_Command": []dbus.Variant{dbus.MakeVariant("xfwm4")},
				"/sessions/Failsafe/Client1_Command": []dbus.Variant{dbus.MakeVariant("xfsettingsd")},
				"/sessions/Failsafe/Client2_Command": []dbus.Variant{dbus.MakeVariant("Thunar"), dbus.MakeVariant("--daemon")},
				"/sessions/Failsafe/Client3_Command": []dbus.Variant{dbus.MakeVariant("xfce4-panel")},
			},
			"xfce4-keyboard-shortcuts": {
				"/commands/custom/<Alt>F1":              sub(home, ".local/bin/launcher"),
				"/commands/custom/Print":                "xfce4-screenshooter",
				"/commands/custom/Print/startup-notify": true,
				"/commands/custom/override":             true,
			},
		},
		path: path,
		env:  map[string]string{"XDG_SESSION_TYPE": "x11"},
	}
	return &doctor{sys: sys, home: home, root: root}, sys
}

// realFont finds a font file on this machine to stand in for the first
// candidate: the check only needs one that parses.
func realFont(t *testing.T, candidates []string) string {
	t.Helper()
	_, path, err := text.Load(append(slices.Clone(candidates), text.Candidates...))
	if err != nil {
		t.Skipf("no font installed to test with: %v", err)
	}
	return path
}

func runChecks(d *doctor) []result {
	d.run()
	return d.results
}

func problems(results []result) []result {
	var out []result
	for _, r := range results {
		if r.status == statusWarn || r.status == statusFail {
			out = append(out, r)
		}
	}
	return out
}

// expectOne runs d and wants exactly one problem, of status s, mentioning
// want.
func expectOne(t *testing.T, d *doctor, s status, want string) {
	t.Helper()
	got := problems(runChecks(d))
	if len(got) != 1 || got[0].status != s || !strings.Contains(got[0].what, want) {
		t.Fatalf("problems = %+v; want one %s mentioning %q", got, s, want)
	}
	if got[0].fix == "" {
		t.Errorf("%q has no fix", got[0].what)
	}
}

func TestHealthy(t *testing.T) {
	d, _ := healthy(t)
	if got := problems(runChecks(d)); len(got) != 0 {
		t.Fatalf("a healthy setup has problems: %+v", got)
	}
	for _, r := range d.results {
		if r.status == statusSkip {
			t.Errorf("skipped on a healthy setup: %+v", r)
		}
	}
}

func TestPanelBack(t *testing.T) {
	d, sys := healthy(t)
	sys.procs["xfce4-panel"] = true
	sys.owners["org.kde.StatusNotifierWatcher"] = "panel-8-systray"
	sys.xfconf["xfce4-session"]["/sessions/Failsafe/Count"] = int32(4)
	got := problems(runChecks(d))
	if len(got) != 3 {
		t.Fatalf("problems = %+v; want the process, the tray and the session", got)
	}
	for i, want := range []string{"xfce4-panel is running", "owned by panel-8-systray", "starts xfce4-panel again"} {
		if got[i].status != statusFail || !strings.Contains(got[i].what, want) {
			t.Errorf("problem %d = %+v, want a failure mentioning %q", i, got[i], want)
		}
	}
}

func TestToolNotRunning(t *testing.T) {
	d, sys := healthy(t)
	delete(sys.procs, "dock")
	expectOne(t, d, statusFail, "dock is not running")
}

func TestCompanionNotRunning(t *testing.T) {
	d, sys := healthy(t)
	delete(sys.procs, "gokeyd")
	expectOne(t, d, statusWarn, "gokeyd is not running")
}

func TestNotificationsTakenBack(t *testing.T) {
	d, sys := healthy(t)
	sys.owners["org.freedesktop.Notifications"] = "xfce4-notifyd"
	expectOne(t, d, statusFail, "owned by xfce4-notifyd, not notifyd")
}

func TestMaskGone(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Remove(sub(d.home, ".config/systemd/user/xfce4-notifyd.service")); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusFail, "xfce4-notifyd.service is no longer masked")
}

// A package renaming its unit gets around the mask: the old name still
// masked, guarding nothing.
func TestMaskedUnitRenamed(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Remove(sub(d.root, "usr/lib/systemd/user/obex.service")); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusWarn, "no longer has obex.service")
}

func TestOverrideChanged(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.home, ".local/share/dbus-1/services/org.blueman.Applet.service"),
		"[D-BUS Service]\nName=org.blueman.Applet\nExec=/usr/bin/blueman-applet\n", 0o644)
	expectOne(t, d, statusFail, "Exec=/usr/bin/blueman-applet")
}

func TestOverrideMissing(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Remove(sub(d.home, ".local/share/dbus-1/services/org.gnome.Identity.service")); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusFail, "override for org.gnome.Identity is missing")
}

func TestServiceRenamed(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.root, "usr/share/dbus-1/services/system-org.freedesktop.Notifications.service"),
		"[D-BUS Service]\nName=org.freedesktop.Notifications2\n", 0o644)
	expectOne(t, d, statusWarn, "no longer provides org.freedesktop.Notifications")
}

func TestAutostartUnhidden(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.home, ".config/autostart/nm-applet.desktop"), "[Desktop Entry]\nHidden=false\n", 0o644)
	expectOne(t, d, statusFail, "nm-applet.desktop is no longer hidden")
}

func TestNewSystemAutostart(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.root, "etc/xdg/autostart/shiny-tray.desktop"), "[Desktop Entry]\nName=Shiny Tray\n", 0o644)
	expectOne(t, d, statusWarn, "shiny-tray.desktop (Shiny Tray)")

	// Hidden by the user: looked at, and not news.
	write(t, sub(d.home, ".config/autostart/shiny-tray.desktop"), "[Desktop Entry]\nHidden=true\n", 0o644)
	d.results = nil
	if got := problems(runChecks(d)); len(got) != 0 {
		t.Errorf("problems = %+v", got)
	}
}

func TestOurAutostartMissing(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Remove(sub(d.home, ".config/autostart/menubar.desktop")); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusFail, "menubar does not start at login")
}

func TestNotInstalled(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Chmod(sub(d.home, ".local/bin/macmenu"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusFail, "not executable")
}

func TestSaveOnExit(t *testing.T) {
	d, sys := healthy(t)
	sys.xfconf["xfce4-session"]["/general/SaveOnExit"] = true
	expectOne(t, d, statusWarn, "saves the session")
}

func TestSavedSession(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.home, ".cache/sessions/xfce4-session-mbp:0"),
		"[Session: Default]\nClient0_Program=xfwm4\nClient2_Program=xfce4-panel\nCount=3\n", 0o644)
	expectOne(t, d, statusFail, "saved session starts xfce4-panel")

	// A saved session of only the programs we keep is fine.
	write(t, sub(d.home, ".cache/sessions/xfce4-session-mbp:0"),
		"[Session: Default]\nClient0_Program=xfwm4\nCount=1\n", 0o644)
	if got := problems(runChecks(&doctor{sys: d.sys, home: d.home, root: d.root})); len(got) != 0 {
		t.Fatalf("problems = %+v", got)
	}
}

func TestShortcuts(t *testing.T) {
	d, sys := healthy(t)
	short := sys.xfconf["xfce4-keyboard-shortcuts"]
	short["/commands/custom/<Primary>Escape"] = "xfdesktop --menu"
	sys.path["xfdesktop"] = true // installed, and still no use: it is retired
	expectOne(t, d, statusWarn, "<Primary>Escape runs xfdesktop, which wallpaper replaced")

	d, sys = healthy(t)
	sys.xfconf["xfce4-keyboard-shortcuts"]["/commands/custom/<Super>F9"] = "no-such-program --x"
	expectOne(t, d, statusWarn, "<Super>F9 runs no-such-program, which is not installed")

	d, sys = healthy(t)
	sys.xfconf["xfce4-keyboard-shortcuts"]["/commands/custom/<Alt>F1"] = "xfce4-appfinder"
	expectOne(t, d, statusFail, "not the launcher")
}

func TestCommandMissing(t *testing.T) {
	d, sys := healthy(t)
	delete(sys.path, "xflock4")
	expectOne(t, d, statusFail, "xflock4 is not installed, which breaks macmenu: Lock Screen")

	d, sys = healthy(t)
	delete(sys.path, "pavucontrol")
	expectOne(t, d, statusWarn, "pavucontrol")

	// Any one of the alternatives will do.
	d, sys = healthy(t)
	delete(sys.path, "7z")
	sys.path["7zz"] = true
	if got := problems(runChecks(d)); len(got) != 0 {
		t.Errorf("problems = %+v", got)
	}
}

func TestScaleUnset(t *testing.T) {
	d, _ := healthy(t)
	if err := os.Remove(sub(d.home, ".config/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml")); err != nil {
		t.Fatal(err)
	}
	expectOne(t, d, statusWarn, "draw at 1x")
}

func TestUnpackDefaultTaken(t *testing.T) {
	d, _ := healthy(t)
	write(t, sub(d.home, ".config/mimeapps.list"),
		"[Default Applications]\napplication/zip=org.gnome.FileRoller.desktop\napplication/x-tar=unpack.desktop\n", 0o644)
	expectOne(t, d, statusWarn, "application/zip opens with \"org.gnome.FileRoller.desktop\"")
}

func TestWayland(t *testing.T) {
	d, sys := healthy(t)
	sys.env["XDG_SESSION_TYPE"] = "wayland"
	expectOne(t, d, statusFail, "Wayland")
}

// Outside the desktop session the bus checks are skipped, not failed.
func TestNoBus(t *testing.T) {
	d, sys := healthy(t)
	sys.busErr = errNoBus
	sys.xfErr = fmt.Errorf("connecting: %w", errNoBus)
	sys.env["XDG_SESSION_TYPE"] = "tty"
	results := runChecks(d)
	if got := problems(results); len(got) != 0 {
		t.Fatalf("problems = %+v", got)
	}
	skipped := 0
	for _, r := range results {
		if r.status == statusSkip {
			skipped++
		}
	}
	// Session type, the two bus names, the XFCE session, the shortcuts.
	if skipped != 5 {
		t.Errorf("%d skipped, want 5", skipped)
	}

	d, sys = healthy(t)
	sys.xfErr = xfconf.ErrNoXfconf
	if got := problems(runChecks(d)); len(got) != 0 {
		t.Errorf("without xfconfd: problems = %+v", got)
	}
}

func TestProcessesUnreadable(t *testing.T) {
	d, sys := healthy(t)
	sys.procsErr = errors.New("no /proc")
	if got := problems(runChecks(d)); len(got) != 0 {
		t.Errorf("problems = %+v", got)
	}
}

func TestReport(t *testing.T) {
	results := make([]result, 0, 4)
	results = append(results,
		result{status: statusOK, area: "a", what: "fine"},
		result{status: statusSkip, area: "b", what: "bus-check", fix: "no bus"},
		result{status: statusWarn, area: "c", what: "hmm", fix: "do this"},
	)
	var quiet bytes.Buffer
	if report(&quiet, results, false) {
		t.Error("a warning counted as a failure")
	}
	if strings.Contains(quiet.String(), "fine") || strings.Contains(quiet.String(), "bus-check") ||
		!strings.Contains(quiet.String(), "WARN  c             hmm") || !strings.Contains(quiet.String(), "fix: do this") ||
		!strings.Contains(quiet.String(), "3 checks: 1 ok, 1 warnings, 0 failed, 1 skipped") {
		t.Errorf("quiet report:\n%s", quiet.String())
	}

	var loud bytes.Buffer
	failed := report(&loud, append(results, result{status: statusFail, area: "d", what: "broken"}), true)
	if !failed || !strings.Contains(loud.String(), "fine") || !strings.Contains(loud.String(), "why: no bus") {
		t.Errorf("verbose report (failed=%v):\n%s", failed, loud.String())
	}

	var clean bytes.Buffer
	report(&clean, results[:1], false)
	if !strings.Contains(clean.String(), "everything is as osxflow left it") {
		t.Errorf("clean report:\n%s", clean.String())
	}
}

func TestParseGroup(t *testing.T) {
	keys, err := parseGroup([]byte(`# comment
[Other]
Name=wrong
[Desktop Entry]
Name = Right
Name=second
Exec=/bin/x --flag=1
broken line

[Desktop Action new]
Exec=nope
`), "Desktop Entry")
	if err != nil {
		t.Fatal(err)
	}
	if keys["Name"] != "Right" || keys["Exec"] != "/bin/x --flag=1" || len(keys) != 2 {
		t.Errorf("keys = %v", keys)
	}
}

func FuzzParseGroup(f *testing.F) {
	f.Add([]byte("[Desktop Entry]\nName=x\n"), "Desktop Entry")
	f.Add([]byte("[a]\n=\n[b]\nk=v=w\n"), "b")
	f.Fuzz(func(t *testing.T, data []byte, group string) {
		keys, err := parseGroup(data, group)
		if err != nil {
			return
		}
		for k, v := range keys {
			if strings.TrimSpace(k) != k || strings.TrimSpace(v) != v || strings.Contains(k, "=") {
				t.Fatalf("key %q value %q not trimmed or split", k, v)
			}
		}
	})
}

func TestStrs(t *testing.T) {
	if got := strs([]dbus.Variant{dbus.MakeVariant("a"), dbus.MakeVariant(int32(1)), dbus.MakeVariant("b")}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("strs(variants) = %v", got)
	}
	if got := strs("x"); !slices.Equal(got, []string{"x"}) {
		t.Errorf("strs(string) = %v", got)
	}
	if got := strs(int32(3)); got != nil {
		t.Errorf("strs(int32) = %v", got)
	}
}
