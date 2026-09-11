package desktop

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseExec(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "firefox", []string{"firefox"}},
		{"with args", "code --new-window", []string{"code", "--new-window"}},
		{"field code stripped", "firefox %U", []string{"firefox"}},
		{"lowercase field code", "gedit %u", []string{"gedit"}},
		{"file field codes", "eog %F", []string{"eog"}},
		{"interior field code", "app %U --flag", []string{"app", "--flag"}},
		{"unknown field code", "app %i %c %k", []string{"app"}},
		{"escaped percent", "app 100%%", []string{"app", "100%"}},
		{"quoted path", `"/opt/my app/bin" --go`, []string{"/opt/my app/bin", "--go"}},
		{"quoted with escape", `"a\"b"`, []string{`a"b`}},
		{"explicit empty arg", `app "" x`, []string{"app", "", "x"}},
		{"backslash escape", `a\ b`, []string{"a b"}},
		{"extra whitespace", "  firefox   %U  ", []string{"firefox"}},
		{"tabs", "firefox\t--safe-mode", []string{"firefox", "--safe-mode"}},
		{"empty", "", nil},
		{"only field code", "%U", nil},
		{"absolute path", "/usr/bin/env FOO=1 app", []string{"/usr/bin/env", "FOO=1", "app"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseExec(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseExec(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestUnescape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{`a\sb`, "a b"},
		{`a\nb`, "a\nb"},
		{`a\tb`, "a\tb"},
		{`a\rb`, "a\rb"},
		{`a\\b`, `a\b`},
		{`a\qb`, `a\qb`},       // unknown escape keeps both bytes
		{`C:\temp`, "C:\temp"}, // \t IS defined: a literal backslash must be written \\
		{`trailing\`, `trailing\`},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := unescape(tc.in); got != tc.want {
				t.Errorf("unescape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseGroups(t *testing.T) {
	// The Desktop Actions group below must not leak into the entry: its
	// Name would otherwise overwrite the application's.
	src := `# a comment

[Desktop Entry]
Type=Application
Name=Real Name
Exec=realapp
Name[nl]=Echte Naam
Comment=Does things

[Desktop Action new-window]
Name=Open a New Window
Exec=realapp --new-window
`
	e, problems, err := parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none: comments, blanks and other groups are not malformed", problems)
	}
	if got := e["Name"]; got != "Real Name" {
		t.Errorf("Name = %q, want %q", got, "Real Name")
	}
	if got := e["Exec"]; got != "realapp" {
		t.Errorf("Exec = %q, want %q (the action's Exec must not win)", got, "realapp")
	}
	if _, ok := e["Name[nl]"]; ok {
		t.Error("localised key was retained, want it dropped")
	}
}

func TestParseDuplicateKeyFirstWins(t *testing.T) {
	e, _, err := parse(strings.NewReader("[Desktop Entry]\nName=First\nName=Second\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := e["Name"]; got != "First" {
		t.Errorf("Name = %q, want %q", got, "First")
	}
}

func TestParseIgnoresContentBeforeGroup(t *testing.T) {
	e, _, err := parse(strings.NewReader("Stray=value\n[Desktop Entry]\nName=X\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := e["Stray"]; ok {
		t.Error("key outside any group was retained")
	}
	if e["Name"] != "X" {
		t.Errorf("Name = %q, want X", e["Name"])
	}
}

// A line with no '=' is skipped -- the rest of the entry still counts --
// and reported with its line number.
func TestParseReportsMalformedLines(t *testing.T) {
	src := "[Desktop Entry]\nName=X\nthis line is junk\nExec=x\n[Desktop Action a]\nalso junk, but not ours\n"
	e, problems, err := parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e["Exec"] != "x" {
		t.Errorf("Exec = %q, want the line after the junk still read", e["Exec"])
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly the one junk line in [Desktop Entry]", problems)
	}
	if msg := problems[0].Error(); !strings.Contains(msg, "line 3") || !strings.Contains(msg, "this line is junk") {
		t.Errorf("problem = %q, want it to give the line number and the line", msg)
	}
}

func TestLoadNamesTheFileInProblems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.desktop")
	write(t, path, "[Desktop Entry]\nType=Application\nName=X\nExec=/bin/sh\njunk\n")
	app, ok, problems, err := Load(path, "x.desktop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok || app.Name != "X" {
		t.Errorf("Load = %+v ok=%v, want the entry despite the bad line", app, ok)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), path) {
		t.Errorf("problems = %v, want one naming %s", problems, path)
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	_, ok, _, err := Load(filepath.Join(t.TempDir(), "gone.desktop"), "gone.desktop")
	if !errors.Is(err, fs.ErrNotExist) || ok {
		t.Errorf("Load of a missing file: ok=%v err=%v, want fs.ErrNotExist", ok, err)
	}
}

// notExecutable makes a file that exists but cannot be run.
func notExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-executable")
	write(t, path, "#!/bin/sh\n")
	return path
}

func TestFindBinary(t *testing.T) {
	// Not installed is an answer, not a failure.
	for _, cmd := range []string{"", "definitely-not-installed-anywhere", "/nonexistent/binary"} {
		if got, err := findBinary(cmd); got != "" || err != nil {
			t.Errorf("findBinary(%q) = %q, %v; want \"\", nil", cmd, got, err)
		}
	}
	if got, err := findBinary("sh"); got == "" || err != nil {
		t.Errorf("findBinary(sh) = %q, %v; want a path", got, err)
	}
	// There but not runnable is a fault on this system, and says so.
	path := notExecutable(t)
	if got, err := findBinary(path); got != "" || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("findBinary(%s) = %q, %v; want a permission error", path, got, err)
	}
}

// A TryExec that cannot be checked hides the entry, as a missing one does,
// but reports why.
func TestToAppTryExecThatCannotRunIsHiddenAndReported(t *testing.T) {
	path := notExecutable(t)
	e := entry{"Type": "Application", "Name": "T", "Exec": "/bin/sh", "TryExec": path}
	_, ok, problems := e.toApp("t.desktop")
	if ok {
		t.Error("entry is visible, want it hidden")
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "TryExec") {
		t.Errorf("problems = %v, want one about TryExec", problems)
	}
}

// An Exec binary that cannot be resolved costs matching by executable, not
// the entry: it is still listed, and the problem is reported.
func TestToAppUnresolvableBinaryIsReportedNotHidden(t *testing.T) {
	path := notExecutable(t)
	e := entry{"Type": "Application", "Name": "T", "Exec": path}
	app, ok, problems := e.toApp("t.desktop")
	if !ok {
		t.Fatal("entry is hidden, want it listed")
	}
	if app.Binary != "" {
		t.Errorf("Binary = %q, want empty", app.Binary)
	}
	if len(problems) != 1 || !errors.Is(problems[0], fs.ErrPermission) {
		t.Errorf("problems = %v, want the permission error", problems)
	}
}

func TestToAppVisibility(t *testing.T) {
	base := "[Desktop Entry]\nType=Application\nName=Thing\nExec=/bin/sh\n"
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"normal", base, true},
		{"not an application", "[Desktop Entry]\nType=Link\nName=T\nExec=/bin/sh\n", false},
		{"no type", "[Desktop Entry]\nName=T\nExec=/bin/sh\n", false},
		{"NoDisplay", base + "NoDisplay=true\n", false},
		{"NoDisplay mixed case", base + "NoDisplay=True\n", false},
		{"NoDisplay false", base + "NoDisplay=false\n", true},
		{"Hidden", base + "Hidden=true\n", false},
		{"no name", "[Desktop Entry]\nType=Application\nExec=/bin/sh\n", false},
		{"no exec", "[Desktop Entry]\nType=Application\nName=T\n", false},
		{"TryExec present", base + "TryExec=/bin/sh\n", true},
		{"TryExec missing", base + "TryExec=/nonexistent/binary\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, _, err := parse(strings.NewReader(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, ok, problems := e.toApp("t.desktop")
			if ok != tc.want {
				t.Errorf("toApp visible = %v, want %v", ok, tc.want)
			}
			// Hidden and not installed are answers, not problems.
			if len(problems) != 0 {
				t.Errorf("problems = %v, want none", problems)
			}
		})
	}
}

func TestToAppFields(t *testing.T) {
	src := "[Desktop Entry]\nType=Application\nName=My App\nComment=Nice\n" +
		"Exec=/bin/sh -c true %U\nIcon=myicon\nStartupWMClass=MyApp\nTerminal=true\n"
	e, _, err := parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	app, ok, problems := e.toApp("my.desktop")
	if !ok {
		t.Fatal("toApp said not visible, want visible")
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none", problems)
	}
	if app.Name != "My App" || app.Comment != "Nice" || app.Icon != "myicon" {
		t.Errorf("fields wrong: %+v", app)
	}
	if app.StartupWMClass != "MyApp" || !app.Terminal {
		t.Errorf("StartupWMClass/Terminal wrong: %+v", app)
	}
	if want := []string{"/bin/sh", "-c", "true"}; !reflect.DeepEqual(app.Argv, want) {
		t.Errorf("Argv = %#v, want %#v", app.Argv, want)
	}
	// /bin/sh is a symlink on many systems; Binary must be the resolved
	// target, because that is what /proc/<pid>/exe will report.
	if app.Binary == "" {
		t.Error("Binary is empty, want the resolved path to sh")
	}
	if resolved, err := filepath.EvalSymlinks("/bin/sh"); err == nil && app.Binary != resolved {
		t.Errorf("Binary = %q, want %q (symlinks must be resolved)", app.Binary, resolved)
	}
}

func TestScanDirsPrecedenceAndShadowing(t *testing.T) {
	root := t.TempDir()
	high := filepath.Join(root, "high")
	low := filepath.Join(root, "low")

	// Same id in both directories: the earlier directory must win.
	write(t, filepath.Join(high, "shared.desktop"),
		"[Desktop Entry]\nType=Application\nName=High Version\nExec=/bin/sh\n")
	write(t, filepath.Join(low, "shared.desktop"),
		"[Desktop Entry]\nType=Application\nName=Low Version\nExec=/bin/sh\n")
	write(t, filepath.Join(low, "only-low.desktop"),
		"[Desktop Entry]\nType=Application\nName=Only Low\nExec=/bin/sh\n")
	// A nested file becomes a dashed id.
	write(t, filepath.Join(low, "kde", "konsole.desktop"),
		"[Desktop Entry]\nType=Application\nName=Konsole\nExec=/bin/sh\n")
	// Hidden entries and non-.desktop files must not appear.
	write(t, filepath.Join(low, "hidden.desktop"),
		"[Desktop Entry]\nType=Application\nName=Hidden\nExec=/bin/sh\nNoDisplay=true\n")
	write(t, filepath.Join(low, "notes.txt"), "not a desktop file")

	apps, problems := ScanDirs([]string{high, low, filepath.Join(root, "does-not-exist")})
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none (a missing directory is normal)", problems)
	}

	byID := make(map[string]App, len(apps))
	for i := range apps {
		byID[apps[i].ID] = apps[i]
	}
	if got := byID["shared.desktop"].Name; got != "High Version" {
		t.Errorf("shadowing failed: shared.desktop = %q, want High Version", got)
	}
	if _, ok := byID["only-low.desktop"]; !ok {
		t.Error("only-low.desktop missing")
	}
	if _, ok := byID["kde-konsole.desktop"]; !ok {
		t.Errorf("nested file did not get a dashed id; got ids %v", ids(apps))
	}
	if _, ok := byID["hidden.desktop"]; ok {
		t.Error("NoDisplay entry appeared in results")
	}
	if len(apps) != 3 {
		t.Errorf("got %d apps %v, want 3", len(apps), ids(apps))
	}
}

func TestScanDirsSortedByName(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zebra", "Apple", "monkey"} {
		write(t, filepath.Join(dir, n+".desktop"),
			"[Desktop Entry]\nType=Application\nName="+n+"\nExec=/bin/sh\n")
	}
	apps, _ := ScanDirs([]string{dir})
	got := make([]string, 0, len(apps))
	for i := range apps {
		got = append(got, apps[i].Name)
	}
	// Case-insensitive: Apple before monkey before zebra.
	want := []string{"Apple", "monkey", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestScanDirsBadFileDoesNotStopScan(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "good.desktop"),
		"[Desktop Entry]\nType=Application\nName=Good\nExec=/bin/sh\n")
	// Not valid, but must not prevent good.desktop from being found.
	write(t, filepath.Join(dir, "junk.desktop"), "\x00\x01 not remotely a desktop file")

	apps, _ := ScanDirs([]string{dir})
	if len(apps) != 1 || apps[0].Name != "Good" {
		t.Errorf("got %v, want just Good", ids(apps))
	}
}

func TestDataDirsDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	t.Setenv("XDG_DATA_DIRS", "/a:/b")
	want := []string{"/custom/data/applications", "/a/applications", "/b/applications"}
	if got, err := DataDirs(); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("DataDirs() = %v, %v; want %v", got, err, want)
	}

	t.Setenv("XDG_DATA_DIRS", "")
	got, err := DataDirs()
	if err != nil || len(got) != 3 || got[1] != "/usr/local/share/applications" || got[2] != "/usr/share/applications" {
		t.Errorf("DataDirs() with empty XDG_DATA_DIRS = %v, %v; want the spec defaults", got, err)
	}
}

// With no way to find the user's directory the system ones are still
// returned, and the missing one is an error rather than a silent gap.
func TestDataDirsWithoutHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_DIRS", "/a")
	got, err := DataDirs()
	if err == nil {
		t.Error("DataDirs() reported nothing with $HOME unset")
	}
	if want := []string{"/a/applications"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DataDirs() = %v, want %v", got, want)
	}
}

func TestScanReportsAMissingHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_DIRS", t.TempDir()) // no applications directory: silent
	_, problems := Scan()
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "user's own applications") {
		t.Errorf("problems = %v, want the one about $HOME", problems)
	}
}

// Only a missing top-level directory is normal. Trouble inside one that
// exists -- an unreadable subdirectory, an entry that points nowhere -- is
// reported.
func TestScanDirsReportsTroubleBelowTheRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 directory")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "good.desktop"),
		"[Desktop Entry]\nType=Application\nName=Good\nExec=/bin/sh\n")
	locked := filepath.Join(dir, "locked")
	write(t, filepath.Join(locked, "hidden.desktop"),
		"[Desktop Entry]\nType=Application\nName=Hidden\nExec=/bin/sh\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o700); err != nil {
			t.Error(err)
		}
	})
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling.desktop")); err != nil {
		t.Fatal(err)
	}

	apps, problems := ScanDirs([]string{dir})
	if len(apps) != 1 || apps[0].Name != "Good" {
		t.Errorf("apps = %v, want just Good", ids(apps))
	}
	var sawLocked, sawDangling bool
	for _, p := range problems {
		sawLocked = sawLocked || errors.Is(p, fs.ErrPermission)
		sawDangling = sawDangling || (errors.Is(p, fs.ErrNotExist) && strings.Contains(p.Error(), "dangling.desktop"))
	}
	if !sawLocked || !sawDangling {
		t.Errorf("problems = %v, want the unreadable directory and the dangling entry", problems)
	}
}

func TestDesktopID(t *testing.T) {
	id, depth, err := desktopID("/usr/share/applications", "/usr/share/applications/kde/konsole.desktop")
	if err != nil || id != "kde-konsole.desktop" || depth != 1 {
		t.Errorf("desktopID = %q, %d, %v; want kde-konsole.desktop, 1, nil", id, depth, err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ids(apps []App) []string {
	out := make([]string, 0, len(apps))
	for i := range apps {
		out = append(out, apps[i].ID)
	}
	return out
}

// TestScanDirsDedupesIdenticalEntries covers the case XDG id-shadowing
// does not: /usr/share/applications ships both webapp-manager.desktop and
// kde4/webapp-manager.desktop, whose ids differ ("webapp-manager.desktop"
// vs "kde4-webapp-manager.desktop"), so neither shadows the other and the
// same application is listed twice.
func TestScanDirsDedupesIdenticalEntries(t *testing.T) {
	dir := t.TempDir()
	entry := "[Desktop Entry]\nType=Application\nName=Web Apps\nExec=webapp-manager\n"
	write(t, filepath.Join(dir, "webapp-manager.desktop"), entry)
	write(t, filepath.Join(dir, "kde4", "webapp-manager.desktop"), entry)

	apps, _ := ScanDirs([]string{dir})
	if len(apps) != 1 {
		t.Fatalf("got %d entries %v, want 1", len(apps), ids(apps))
	}
	// The top-level entry is canonical; the nested one is compatibility
	// packaging.
	if apps[0].ID != "webapp-manager.desktop" {
		t.Errorf("kept %q, want the shallower webapp-manager.desktop", apps[0].ID)
	}
}

func TestScanDirsKeepsDifferentAppsWithTheSameName(t *testing.T) {
	// Several packages ship a "Settings" that runs a different program.
	// Those must all survive; only same-name-same-command pairs collapse.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.desktop"),
		"[Desktop Entry]\nType=Application\nName=Settings\nExec=/bin/sh -c a\n")
	write(t, filepath.Join(dir, "b.desktop"),
		"[Desktop Entry]\nType=Application\nName=Settings\nExec=/bin/sh -c b\n")

	apps, _ := ScanDirs([]string{dir})
	if len(apps) != 2 {
		t.Errorf("got %d entries %v, want both to survive", len(apps), ids(apps))
	}
}

func TestScanDirsDedupePrefersEarlierDirectory(t *testing.T) {
	root := t.TempDir()
	high, low := filepath.Join(root, "high"), filepath.Join(root, "low")
	entry := "[Desktop Entry]\nType=Application\nName=Thing\nExec=thing\n"
	// Different ids, so id-shadowing does not apply; the dedupe must still
	// prefer the higher-precedence directory.
	write(t, filepath.Join(high, "thing.desktop"), entry)
	write(t, filepath.Join(low, "vendor", "thing.desktop"), entry)

	apps, _ := ScanDirs([]string{high, low})
	if len(apps) != 1 {
		t.Fatalf("got %d entries %v, want 1", len(apps), ids(apps))
	}
	if apps[0].ID != "thing.desktop" {
		t.Errorf("kept %q, want the entry from the first directory", apps[0].ID)
	}
}
