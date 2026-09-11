package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"syscall"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// SpawnFunc starts an application that is not already running.
type SpawnFunc func(app *desktop.App) error

// Launcher turns "the user picked this app" into either a focus request or
// a new process.
//
// Every external dependency is a field so that tests can supply their own:
// the window list, the pid-to-executable lookup, and process creation are
// the three things that need a real desktop session, and all three are
// replaceable.
type Launcher struct {
	Server xwin.Server
	Exe    ExeFunc
	Spawn  SpawnFunc
}

// New returns a Launcher wired to the real system.
func New(server xwin.Server) *Launcher {
	return &Launcher{Server: server, Exe: ProcExe, Spawn: SpawnDetached}
}

// Result says what Open did, which the caller needs in order to report it
// and tests need in order to assert on it.
type Result struct {
	Window     xwin.Window
	Confidence Confidence
	Focused    bool
}

// Open focuses app's existing window if it has one, and starts it
// otherwise.
//
// A failure to enumerate windows is not fatal: if X cannot be read we
// cannot know whether the app is running, and starting a second copy is a
// far better failure than doing nothing. When the start then succeeds, so
// does Open, and the window-list failure is logged here as a warning. It
// used to be returned alongside the successful start, and callers --
// reasonably -- took a non-nil error to mean nothing was started: the
// launcher stayed open, so a second Enter started a second copy.
func (l *Launcher) Open(app *desktop.App) (Result, error) {
	if len(app.Argv) == 0 {
		return Result{}, fmt.Errorf("%s has no command to run", app.Name)
	}

	wins, err := l.Server.Windows()
	if err != nil {
		if spawnErr := l.spawn(app); spawnErr != nil {
			return Result{}, errors.Join(err, spawnErr)
		}
		log.Printf("could not check whether %s was already running, started it anyway: %v", app.Name, err)
		return Result{}, nil
	}

	if win, conf := Match(app, wins, l.Exe); conf > None {
		if err := l.Server.Activate(win.ID); err != nil {
			return Result{}, fmt.Errorf("focusing %s: %w", app.Name, err)
		}
		return Result{Focused: true, Window: win, Confidence: conf}, nil
	}

	if err := l.spawn(app); err != nil {
		return Result{}, err
	}
	return Result{}, nil
}

func (l *Launcher) spawn(app *desktop.App) error {
	spawn := l.Spawn
	if spawn == nil {
		spawn = SpawnDetached
	}
	if err := spawn(app); err != nil {
		return fmt.Errorf("starting %s: %w", app.Name, err)
	}
	return nil
}

// SpawnDetached starts an application so that it outlives the launcher.
//
// Setsid puts the child in its own session, which is what stops it dying
// with us and stops it competing for the terminal we were started from.
// Standard streams go to /dev/null rather than being inherited: a GUI app
// writing warnings to a pipe nobody reads will eventually block on a full
// buffer, which presents as the app freezing minutes after launch.
func SpawnDetached(app *desktop.App) error {
	if len(app.Argv) == 0 {
		return errors.New("no command")
	}
	argv := app.Argv
	if app.Terminal {
		wrapped, err := wrapInTerminal(argv)
		if err != nil {
			return err
		}
		argv = wrapped
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}

	// exec.Command, not CommandContext: the whole point is a process that
	// outlives this one. Tying it to a context would kill the application
	// when the launcher exits, which is the opposite of the requirement.
	//nolint:noctx,gosec // detached by design; argv comes from the system's own .desktop entries
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Start in the user's home rather than wherever the launcher was
	// started, so a file dialog in the new app opens somewhere sensible.
	// Without one the app still starts, just from here.
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("no home directory, starting %s in the current directory: %v", app.Name, err)
	} else {
		cmd.Dir = home
	}

	// Our /dev/null is done with once Start returns: the child has its own
	// copies of the descriptor, or never got any.
	startErr := cmd.Start()
	closeErr := devNull.Close()
	if startErr != nil {
		return errors.Join(startErr, closeErr)
	}
	if closeErr != nil {
		// The app is running. Returning this would tell the caller the
		// start failed, and it would start a second copy.
		log.Printf("closing %s after starting %s: %v", os.DevNull, app.Name, closeErr)
	}

	// Reap the child when it exits. Without this every app launched in a
	// session leaves a zombie behind, since Setsid does not detach it from
	// us as a parent -- only from the terminal.
	//
	// Nothing can be done about a non-zero exit by the time it arrives,
	// but it is logged: in the dock, which outlives what it starts, that
	// line is the only trace of an app that crashed on startup. The
	// launcher exits long before and never gets to print it.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("%s exited: %v", app.Name, err)
		}
	}()
	return nil
}

// terminalCandidates is tried in order for Terminal=true entries.
// x-terminal-emulator is Debian's alternatives symlink and is the right
// answer when it exists, because it is what the user chose.
var terminalCandidates = []string{
	"x-terminal-emulator",
	"xfce4-terminal",
	"gnome-terminal",
	"konsole",
	"alacritty",
	"ghostty",
	"kitty",
	"xterm",
}

// wrapInTerminal builds the argv for an entry that must run inside a
// terminal emulator. Every candidate accepts -e followed by the command,
// which is the one piece of terminal command-line syntax that is actually
// portable.
//
// A candidate that is not installed is skipped quietly; most of them are
// not, on any one machine. Any other lookup failure (one that is installed
// but not executable, say) is kept, and all of them are in the error if no
// candidate works, since one of them is probably the terminal the user has.
func wrapInTerminal(argv []string) ([]string, error) {
	var errs []error
	for _, term := range terminalCandidates {
		path, err := exec.LookPath(term)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
				continue // not installed
			}
			errs = append(errs, err)
			continue
		}
		return append([]string{path, "-e"}, argv...), nil
	}
	return nil, errors.Join(append([]error{fmt.Errorf("no terminal emulator found (tried %v)", terminalCandidates)}, errs...)...)
}
