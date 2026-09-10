package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// exeMap builds an ExeFunc from pid -> path, and reports a missing pid the
// way /proc does: as an error, not an empty string.
func exeMap(m map[uint32]string) ExeFunc {
	return func(pid uint32) (string, error) {
		if p, ok := m[pid]; ok {
			return p, nil
		}
		return "", fmt.Errorf("no such process %d", pid)
	}
}

func TestMatchByExecutable(t *testing.T) {
	app := desktop.App{Name: "Ghostty", Binary: "/usr/bin/ghostty", Argv: []string{"ghostty"}}
	wins := []xwin.Window{
		{ID: 1, PID: 100, Instance: "xfce4-panel", Class: "Xfce4-panel"},
		{ID: 2, PID: 200, Instance: "ghostty", Class: "com.mitchellh.ghostty"},
	}
	exe := exeMap(map[uint32]string{100: "/usr/bin/xfce4-panel", 200: "/usr/bin/ghostty"})

	win, conf := Match(&app, wins, exe)
	if conf != ByExecutable {
		t.Fatalf("confidence = %v, want ByExecutable", conf)
	}
	if win.ID != 2 {
		t.Errorf("matched window %d, want 2", win.ID)
	}
}

// The executable match must win even when a different window's WM_CLASS
// happens to look right, because it is the only rule that cannot collide.
func TestMatchExecutableBeatsClass(t *testing.T) {
	app := desktop.App{
		Name: "Real", Binary: "/usr/bin/real",
		Argv: []string{"real"}, StartupWMClass: "Real",
	}
	wins := []xwin.Window{
		{ID: 1, PID: 100, Instance: "real", Class: "Real"}, // class says yes
		{ID: 2, PID: 200, Instance: "other", Class: "Other"},
	}
	exe := exeMap(map[uint32]string{100: "/usr/bin/impostor", 200: "/usr/bin/real"})

	win, conf := Match(&app, wins, exe)
	if conf != ByExecutable || win.ID != 2 {
		t.Errorf("matched window %d by %v, want window 2 by executable", win.ID, conf)
	}
}

func TestMatchByStartupWMClass(t *testing.T) {
	// A wrapper script: /proc reports the wrapped binary, so the exe rule
	// cannot fire and StartupWMClass is what saves the match.
	app := desktop.App{
		Name: "Firefox", Binary: "/usr/bin/firefox",
		Argv: []string{"firefox"}, StartupWMClass: "Navigator",
	}
	wins := []xwin.Window{{ID: 7, PID: 300, Instance: "Navigator", Class: "firefox"}}
	exe := exeMap(map[uint32]string{300: "/usr/lib/firefox/firefox-bin"})

	win, conf := Match(&app, wins, exe)
	if conf != ByDeclaredClass || win.ID != 7 {
		t.Errorf("matched %d by %v, want 7 by StartupWMClass", win.ID, conf)
	}
}

func TestMatchByBinaryName(t *testing.T) {
	tests := []struct {
		name string
		app  desktop.App
		win  xwin.Window
	}{
		{
			"instance half matches",
			desktop.App{Binary: "/usr/bin/ghostty", Argv: []string{"ghostty"}},
			xwin.Window{ID: 1, Instance: "ghostty", Class: "com.mitchellh.ghostty"},
		},
		{
			"class half matches",
			desktop.App{Binary: "/usr/bin/plank", Argv: []string{"plank"}},
			xwin.Window{ID: 1, Instance: "plank", Class: "Plank"},
		},
		{
			"case insensitive",
			desktop.App{Binary: "/usr/bin/xfdesktop", Argv: []string{"xfdesktop"}},
			xwin.Window{ID: 1, Instance: "XFDESKTOP", Class: "Xfdesktop"},
		},
		{
			"unresolved argv name matches when binary basename does not",
			desktop.App{Binary: "/usr/lib/foo/foo-bin", Argv: []string{"foo"}},
			xwin.Window{ID: 1, Instance: "foo", Class: "Foo"},
		},
		{
			"no binary at all, argv only",
			desktop.App{Argv: []string{"someapp"}},
			xwin.Window{ID: 1, Instance: "someapp", Class: "SomeApp"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			win, conf := Match(&tc.app, []xwin.Window{tc.win}, exeMap(nil))
			if conf != ByBinaryName {
				t.Fatalf("confidence = %v, want ByBinaryName", conf)
			}
			if win.ID != tc.win.ID {
				t.Errorf("matched %d, want %d", win.ID, tc.win.ID)
			}
		})
	}
}

func TestMatchNone(t *testing.T) {
	app := desktop.App{Name: "Nothing", Binary: "/usr/bin/nothing", Argv: []string{"nothing"}}
	wins := []xwin.Window{
		{ID: 1, PID: 100, Instance: "xfce4-panel", Class: "Xfce4-panel"},
		{ID: 2, PID: 200, Instance: "ghostty", Class: "com.mitchellh.ghostty"},
	}
	exe := exeMap(map[uint32]string{100: "/usr/bin/xfce4-panel", 200: "/usr/bin/ghostty"})

	if _, conf := Match(&app, wins, exe); conf != None {
		t.Errorf("confidence = %v, want None", conf)
	}
}

func TestMatchEmptyWindowList(t *testing.T) {
	app := desktop.App{Binary: "/usr/bin/x", Argv: []string{"x"}}
	if _, conf := Match(&app, nil, exeMap(nil)); conf != None {
		t.Errorf("confidence = %v, want None", conf)
	}
}

// A window with no _NET_WM_PID must not be compared against /proc, and
// must still be matchable by class.
func TestMatchWindowWithoutPID(t *testing.T) {
	app := desktop.App{Binary: "/usr/bin/thing", Argv: []string{"thing"}}
	wins := []xwin.Window{{ID: 5, PID: 0, Instance: "thing", Class: "Thing"}}

	called := false
	exe := func(uint32) (string, error) { called = true; return "", errors.New("nope") }

	win, conf := Match(&app, wins, exe)
	if called {
		t.Error("ExeFunc was called for a window with pid 0")
	}
	if conf != ByBinaryName || win.ID != 5 {
		t.Errorf("matched %d by %v, want 5 by binary name", win.ID, conf)
	}
}

// Ties go to the topmost window: wins is bottom-to-top, so the later of
// two equal matches is the one most recently raised.
func TestMatchPrefersTopmostOnTie(t *testing.T) {
	app := desktop.App{Binary: "/usr/bin/term", Argv: []string{"term"}}
	wins := []xwin.Window{
		{ID: 1, PID: 10, Instance: "term", Class: "Term"},
		{ID: 2, PID: 20, Instance: "term", Class: "Term"},
		{ID: 3, PID: 30, Instance: "term", Class: "Term"},
	}
	exe := exeMap(map[uint32]string{10: "/usr/bin/term", 20: "/usr/bin/term", 30: "/usr/bin/term"})

	win, _ := Match(&app, wins, exe)
	if win.ID != 3 {
		t.Errorf("matched %d, want 3 (topmost)", win.ID)
	}
}

// A weaker match appearing after a stronger one must not displace it.
func TestMatchStrongerEarlierWins(t *testing.T) {
	app := desktop.App{Binary: "/usr/bin/app", Argv: []string{"app"}, StartupWMClass: "AppClass"}
	wins := []xwin.Window{
		{ID: 1, PID: 10, Instance: "x", Class: "y"},     // exe match, bottom
		{ID: 2, PID: 20, Instance: "app", Class: "App"}, // name match, top
	}
	exe := exeMap(map[uint32]string{10: "/usr/bin/app", 20: "/usr/bin/other"})

	win, conf := Match(&app, wins, exe)
	if conf != ByExecutable || win.ID != 1 {
		t.Errorf("matched %d by %v, want 1 by executable", win.ID, conf)
	}
}

func TestMatchNilExeFunc(t *testing.T) {
	// Nil ExeFunc must degrade to class matching, not panic.
	app := desktop.App{Binary: "/usr/bin/app", Argv: []string{"app"}}
	wins := []xwin.Window{{ID: 1, PID: 10, Instance: "app", Class: "App"}}
	if _, conf := Match(&app, wins, nil); conf != ByBinaryName {
		t.Errorf("confidence = %v, want ByBinaryName", conf)
	}
}

func TestOpenFocusesExisting(t *testing.T) {
	app := desktop.App{Name: "Ghostty", Binary: "/usr/bin/ghostty", Argv: []string{"ghostty"}}
	server := &xwin.Fake{List: []xwin.Window{
		{ID: 42, PID: 200, Instance: "ghostty", Class: "com.mitchellh.ghostty"},
	}}
	spawned := 0
	l := &Launcher{
		Server: server,
		Exe:    exeMap(map[uint32]string{200: "/usr/bin/ghostty"}),
		Spawn:  func(*desktop.App) error { spawned++; return nil },
	}

	res, err := l.Open(&app)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !res.Focused {
		t.Error("Focused = false, want true")
	}
	if spawned != 0 {
		t.Errorf("spawned %d processes, want 0", spawned)
	}
	if len(server.Activated) != 1 || server.Activated[0] != 42 {
		t.Errorf("activated = %v, want [42]", server.Activated)
	}
}

func TestOpenSpawnsWhenNotRunning(t *testing.T) {
	app := desktop.App{Name: "Nothing", Binary: "/usr/bin/nothing", Argv: []string{"nothing"}}
	server := &xwin.Fake{List: []xwin.Window{{ID: 1, PID: 100, Instance: "other", Class: "Other"}}}

	var got desktop.App
	l := &Launcher{
		Server: server,
		Exe:    exeMap(map[uint32]string{100: "/usr/bin/other"}),
		Spawn:  func(a *desktop.App) error { got = *a; return nil },
	}

	res, err := l.Open(&app)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if res.Focused {
		t.Error("Focused = true, want false")
	}
	if got.Name != "Nothing" {
		t.Errorf("spawned %q, want Nothing", got.Name)
	}
	if len(server.Activated) != 0 {
		t.Errorf("activated %v, want nothing", server.Activated)
	}
}

// If X cannot be read we cannot know whether the app is running. Starting
// a second copy is a much better failure than silently doing nothing, so
// Open must spawn and still report the problem.
func TestOpenSpawnsWhenWindowListFails(t *testing.T) {
	app := desktop.App{Name: "Thing", Argv: []string{"thing"}}
	server := &xwin.Fake{Err: errors.New("display gone")}
	spawned := 0
	l := &Launcher{
		Server: server,
		Exe:    exeMap(nil),
		Spawn:  func(*desktop.App) error { spawned++; return nil },
	}

	_, err := l.Open(&app)
	if err == nil {
		t.Fatal("Open returned no error, want the display failure reported")
	}
	if !strings.Contains(err.Error(), "display gone") {
		t.Errorf("error = %v, want it to mention the display failure", err)
	}
	if spawned != 1 {
		t.Errorf("spawned %d, want 1", spawned)
	}
}

func TestOpenReportsActivateFailure(t *testing.T) {
	app := desktop.App{Name: "Ghostty", Binary: "/usr/bin/ghostty", Argv: []string{"ghostty"}}
	server := &xwin.Fake{
		List:        []xwin.Window{{ID: 42, PID: 200, Instance: "ghostty"}},
		ActivateErr: errors.New("bad window"),
	}
	l := &Launcher{
		Server: server,
		Exe:    exeMap(map[uint32]string{200: "/usr/bin/ghostty"}),
		Spawn:  func(*desktop.App) error { t.Error("must not spawn after finding a window"); return nil },
	}

	if _, err := l.Open(&app); err == nil {
		t.Fatal("Open succeeded, want the activate failure")
	}
}

func TestOpenRejectsAppWithNoCommand(t *testing.T) {
	l := &Launcher{Server: &xwin.Fake{}, Exe: exeMap(nil), Spawn: func(*desktop.App) error { return nil }}
	if _, err := l.Open(&desktop.App{Name: "Broken"}); err == nil {
		t.Fatal("Open succeeded on an app with no Argv, want an error")
	}
}

func TestOpenReportsSpawnFailure(t *testing.T) {
	l := &Launcher{
		Server: &xwin.Fake{},
		Exe:    exeMap(nil),
		Spawn:  func(*desktop.App) error { return errors.New("exec format error") },
	}
	_, err := l.Open(&desktop.App{Name: "Bad", Argv: []string{"bad"}})
	if err == nil {
		t.Fatal("Open succeeded, want the spawn failure")
	}
	if !strings.Contains(err.Error(), "Bad") {
		t.Errorf("error = %v, want it to name the app", err)
	}
}

func TestProcExeFindsOurOwnProcess(t *testing.T) {
	// The one case where the real /proc lookup can be tested hermetically:
	// this test binary is a process whose executable we can verify.
	self := uint32(os.Getpid()) //nolint:gosec // pids fit
	path, err := ProcExe(self)
	if err != nil {
		t.Fatalf("ProcExe(self): %v", err)
	}
	if path == "" {
		t.Error("ProcExe(self) returned an empty path")
	}
}

func TestProcExeRejectsZero(t *testing.T) {
	if _, err := ProcExe(0); err == nil {
		t.Error("ProcExe(0) succeeded, want an error")
	}
}

func TestConfidenceOrdering(t *testing.T) {
	// The whole matcher depends on this ordering; assert it rather than
	// trusting iota to stay put through an edit.
	if None >= ByBinaryName || ByBinaryName >= ByDeclaredClass || ByDeclaredClass >= ByExecutable {
		t.Error("Confidence values are not ordered weakest to strongest")
	}
	for _, c := range []Confidence{None, ByBinaryName, ByDeclaredClass, ByExecutable} {
		if c.String() == "unknown" {
			t.Errorf("Confidence(%d).String() = unknown", c)
		}
	}
}

// TestCandidateNames pins the wrapper-command rule. The jumpapp case is
// taken from a real entry on the machine this was developed on:
// "Exec=jumpapp Navigator firefox %u", where neither the binary nor
// argv[0] is the application and both WM_CLASS halves are arguments.
func TestCandidateNames(t *testing.T) {
	tests := []struct {
		name string
		app  desktop.App
		want []string
	}{
		{
			"plain binary",
			desktop.App{Binary: "/usr/bin/ghostty", Argv: []string{"ghostty"}},
			[]string{"ghostty"},
		},
		{
			"resolved differs from argv0",
			desktop.App{Binary: "/usr/lib/foo/foo-bin", Argv: []string{"foo"}},
			[]string{"foo-bin", "foo"},
		},
		{
			"jumpapp wrapper",
			desktop.App{Binary: "/home/mp/.local/bin/jumpapp", Argv: []string{"jumpapp", "Navigator", "firefox"}},
			[]string{"jumpapp", "Navigator", "firefox"},
		},
		{
			"flatpak wrapper",
			desktop.App{Binary: "/usr/bin/flatpak", Argv: []string{"flatpak", "run", "org.foo.Bar"}},
			[]string{"flatpak", "run", "org.foo.Bar"},
		},
		{
			"flags are not candidates",
			desktop.App{Binary: "/usr/bin/app", Argv: []string{"app", "--new-window", "-x"}},
			[]string{"app"},
		},
		{
			"env assignments are not candidates",
			desktop.App{Binary: "/usr/bin/env", Argv: []string{"env", "GDK_BACKEND=x11", "signal-desktop"}},
			[]string{"env", "signal-desktop"},
		},
		{
			"paths are reduced to basenames",
			desktop.App{Binary: "/usr/bin/sh", Argv: []string{"sh", "/opt/thing/run-thing"}},
			[]string{"sh", "run-thing"},
		},
		{
			"no argv at all",
			desktop.App{Binary: "/usr/bin/x"},
			[]string{"x"},
		},
		{
			"nothing to go on",
			desktop.App{},
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := candidateNames(&tc.app)
			if len(got) != len(tc.want) {
				t.Fatalf("candidateNames = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("candidateNames = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

func TestCandidateNamesDeduplicates(t *testing.T) {
	// "app app" must not yield the same candidate twice, and the check is
	// case-insensitive because WM_CLASS capitalisation is not consistent.
	app := desktop.App{Binary: "/usr/bin/app", Argv: []string{"app", "APP", "App"}}
	if got := candidateNames(&app); len(got) != 1 {
		t.Errorf("candidateNames = %v, want one entry", got)
	}
}

// The end-to-end version of the jumpapp case: a real Firefox window must
// match the wrapper entry, and only at the weakest confidence level so a
// genuine executable match elsewhere would still beat it.
func TestMatchJumpappWrappedEntry(t *testing.T) {
	app := desktop.App{
		Name:   "Firefox Web Browser",
		Binary: "/home/mp/.local/bin/jumpapp",
		Argv:   []string{"jumpapp", "Navigator", "firefox"},
	}
	wins := []xwin.Window{{ID: 9, PID: 30323, Instance: "Navigator", Class: "firefox"}}
	exe := exeMap(map[uint32]string{30323: "/usr/lib/firefox/firefox-bin"})

	win, conf := Match(&app, wins, exe)
	if conf != ByBinaryName || win.ID != 9 {
		t.Errorf("matched %d by %v, want 9 by binary name", win.ID, conf)
	}
}

// TestSpawnDetachedRunsTheCommand starts a real process. The assertion is
// its side effect on the filesystem, because the point of SpawnDetached is
// that the caller does not wait for it and so has nothing else to observe.
func TestSpawnDetachedRunsTheCommand(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	app := desktop.App{
		Name: "Marker",
		Argv: []string{sh, "-c", "printf ok > " + marker},
	}
	if err := SpawnDetached(&app); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker) //nolint:gosec // a path this test just made
		if err == nil && string(data) == "ok" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command did not run within 5s (last error: %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The child must be in its own session, or it dies with the launcher and
// competes for the terminal the launcher was started from.
func TestSpawnDetachedPutsChildInItsOwnSession(t *testing.T) {
	sh, lookErr := exec.LookPath("sh")
	if lookErr != nil {
		t.Skip("no sh available")
	}
	out := filepath.Join(t.TempDir(), "sid")
	app := desktop.App{
		Name: "Session",
		// ps reports the child's session id; our own is different.
		Argv: []string{sh, "-c", "ps -o sid= -p $$ > " + out},
	}
	if err := SpawnDetached(&app); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var got string
	for {
		data, err := os.ReadFile(out) //nolint:gosec // a path this test just made
		if err == nil && strings.TrimSpace(string(data)) != "" {
			got = strings.TrimSpace(string(data))
			break
		}
		if time.Now().After(deadline) {
			t.Skipf("could not read the child's session id: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	childSID, err := strconv.Atoi(got)
	if err != nil {
		t.Skipf("unexpected ps output %q", got)
	}
	ourSID, err := ownSessionID()
	if err != nil {
		t.Skipf("reading our own session id: %v", err)
	}
	if childSID == ourSID {
		t.Errorf("child session %d is our own; Setsid did not take effect", childSID)
	}
}

// ownSessionID reads this process's session id from /proc/self/stat.
// syscall.Getsid is not exposed on Linux in the standard library, and
// pulling in x/sys for one number in one test is not worth it.
//
// The comm field is parenthesised and may itself contain spaces and
// parentheses, so the fields are counted from the last ')' rather than by
// splitting the whole line.
func ownSessionID() (int, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return 0, fmt.Errorf("unexpected /proc/self/stat format")
	}
	// After comm come: state, ppid, pgrp, session.
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 4 {
		return 0, fmt.Errorf("unexpected /proc/self/stat format")
	}
	return strconv.Atoi(fields[3])
}

func TestSpawnDetachedRejectsEmptyArgv(t *testing.T) {
	if err := SpawnDetached(&desktop.App{Name: "Empty"}); err == nil {
		t.Error("SpawnDetached succeeded with no command")
	}
}

func TestSpawnDetachedReportsAMissingBinary(t *testing.T) {
	app := desktop.App{Name: "Absent", Argv: []string{"/nonexistent/definitely-not-here"}}
	if err := SpawnDetached(&app); err == nil {
		t.Error("SpawnDetached succeeded for a binary that does not exist")
	}
}

func TestWrapInTerminal(t *testing.T) {
	argv, err := wrapInTerminal([]string{"htop", "-d", "5"})
	if err != nil {
		t.Skipf("no terminal emulator on this machine: %v", err)
	}
	if len(argv) != 5 {
		t.Fatalf("wrapInTerminal = %v, want the terminal, -e, and the three original arguments", argv)
	}
	if argv[1] != "-e" {
		t.Errorf("argv[1] = %q, want -e", argv[1])
	}
	if argv[2] != "htop" || argv[4] != "5" {
		t.Errorf("wrapInTerminal = %v, want the original command preserved after -e", argv)
	}
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("argv[0] = %q, want an absolute path to the terminal", argv[0])
	}
}
