// Package launch decides what pressing Enter on an app should do: focus a
// window it already has, or start it.
//
// The matching is the interesting half, and it is pure: it takes a list of
// windows and a function from pid to executable path, so all of it is
// testable without a display server or a process table.
package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/errlog"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// Confidence describes how a window was matched to an app. The values are
// ordered: a higher one always wins, and a match is only used at all if it
// is above None.
type Confidence int

const (
	// None means no evidence at all.
	None Confidence = iota

	// ByBinaryName means the window's WM_CLASS looks like the name of the
	// app's binary. This is a guess, and it is the one that fires most
	// often: on this machine only 11 of 146 entries declare a
	// StartupWMClass, so without this rule most apps would never match.
	ByBinaryName

	// ByDeclaredClass means the entry's StartupWMClass matched WM_CLASS.
	// The entry author said so explicitly, which beats inference.
	ByDeclaredClass

	// ByExecutable means /proc/<pid>/exe of the window's process is the
	// app's binary. This is not a heuristic and cannot collide, so it wins
	// outright.
	ByExecutable
)

func (c Confidence) String() string {
	switch c {
	case ByExecutable:
		return "executable"
	case ByDeclaredClass:
		return "StartupWMClass"
	case ByBinaryName:
		return "binary name"
	case None:
		return "no match"
	}
	return "unknown"
}

// ExeFunc resolves a pid to the absolute path of its running executable.
type ExeFunc func(pid uint32) (string, error)

// ErrNoPID is ProcExe's answer for pid 0: a window that never set
// _NET_WM_PID, so there is no process to ask about.
var ErrNoPID = errors.New("window has no pid")

// ProcExe is the real ExeFunc: /proc/<pid>/exe is a symlink to the binary,
// already fully resolved by the kernel.
//
// It is worth knowing where this fails, because the fallbacks exist for
// exactly these cases: a .desktop Exec that names a shell wrapper reports
// the wrapped binary here (so the paths differ and the match must come
// from WM_CLASS instead), and a window belonging to another user's process
// reports a permission error. A process that has exited, or is a zombie
// whose window has not gone yet, reports fs.ErrNotExist.
func ProcExe(pid uint32) (string, error) {
	if pid == 0 {
		return "", ErrNoPID
	}
	path, err := os.Readlink("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/exe")
	if err != nil {
		return "", fmt.Errorf("reading /proc/%d/exe: %w", pid, err)
	}
	return path, nil
}

// exeLog reports ExeFunc failures that are not one of the expected
// absences. It is limited per pid because Index.Owner, which reports
// through it, runs in the dock several times a second: one stuck process
// must not fill the log, and must not be forgotten either.
var exeLog = &errlog.Limiter{Burst: 1, Per: time.Minute}

// exePath asks exe for pid's executable, for matching.
//
// Three failures are not failures here but answers, and stay quiet: no
// pid, a process that has already exited, and a process belonging to
// someone else. In each the executable rule simply cannot apply and the
// window is matched by class instead, which is what the fallbacks are for.
// Anything else means /proc is not behaving the way the matcher relies on,
// and is logged; the match still falls back to class.
func exePath(exe ExeFunc, pid uint32) (string, bool) {
	path, err := exe(pid)
	if err != nil {
		if !errors.Is(err, ErrNoPID) && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
			exeLog.Printf("exe:"+strconv.FormatUint(uint64(pid), 10),
				"matching windows by executable: %v", err)
		}
		return "", false
	}
	return path, true
}

// Match finds the best window belonging to app.
//
// Ties are broken by stacking order: wins is ordered bottom-to-top, so the
// later of two equally good matches is the one the user looked at more
// recently, and that is the one to focus.
func Match(app *desktop.App, wins []xwin.Window, exe ExeFunc) (win xwin.Window, conf Confidence) {
	for i := range wins {
		w := &wins[i]
		c := score(app, w, exe)
		// >= rather than > so that among equal scores the last (topmost)
		// window wins.
		if c > None && c >= conf {
			win, conf = *w, c
		}
	}
	return win, conf
}

func score(app *desktop.App, w *xwin.Window, exe ExeFunc) Confidence {
	if app.Binary != "" && w.PID != 0 && exe != nil {
		if path, ok := exePath(exe, w.PID); ok && path == app.Binary {
			return ByExecutable
		}
	}
	if app.StartupWMClass != "" && classMatches(app.StartupWMClass, w) {
		return ByDeclaredClass
	}
	// The binary's basename against both halves of WM_CLASS. Ghostty is
	// the worked example: binary "ghostty", instance "ghostty", class
	// "com.mitchellh.ghostty" -- the instance is what matches, and a
	// class-only comparison would miss it.
	for _, name := range candidateNames(app) {
		if classMatches(name, w) {
			return ByBinaryName
		}
	}
	return None
}

// classMatches compares a name against both halves of WM_CLASS.
//
// The comparison is case-insensitive because the convention is not
// followed consistently: the entry for Konsole declares "konsole" while
// the window reports Class "konsole" and Instance "konsole", but plenty of
// Java and Electron apps capitalise one and not the other.
func classMatches(name string, w *xwin.Window) bool {
	return strings.EqualFold(name, w.Instance) || strings.EqualFold(name, w.Class)
}

// candidateNames lists the names worth comparing against WM_CLASS.
//
// The obvious candidates are the resolved binary and argv[0]: both are
// needed because either can be the one the window agrees with, since
// /usr/bin/foo symlinked to /usr/lib/foo/foo-bin resolves to "foo-bin"
// while the window still calls itself "foo".
//
// The non-obvious candidates are the remaining arguments, and they are
// here because of wrapper commands. An Exec line is not always the
// application: "jumpapp Navigator firefox %u", "flatpak run org.foo.Bar",
// "env GDK_BACKEND=x11 signal-desktop" all name the real program in an
// argument rather than in argv[0]. In the jumpapp case the arguments are
// literally the two halves of WM_CLASS.
//
// Flags and environment assignments are skipped, which is what keeps this
// from degenerating into "match anything": a candidate has to look like a
// program name, and it only ever produces the weakest confidence level, so
// a real executable or StartupWMClass match still beats it.
func candidateNames(app *desktop.App) []string {
	var out []string
	add := func(name string) {
		if name == "" || name == "." || name == "/" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, name) {
				return
			}
		}
		out = append(out, name)
	}

	if app.Binary != "" {
		add(filepath.Base(app.Binary))
	}
	if len(app.Argv) > 0 {
		add(filepath.Base(app.Argv[0]))
	}
	for _, arg := range argvTail(app.Argv) {
		if strings.HasPrefix(arg, "-") || strings.ContainsRune(arg, '=') {
			continue
		}
		add(filepath.Base(arg))
	}
	return out
}

func argvTail(argv []string) []string {
	if len(argv) < 2 {
		return nil
	}
	return argv[1:]
}
