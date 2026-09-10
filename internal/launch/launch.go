package launch

import (
	"errors"
	"fmt"
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
// far better failure than doing nothing. The error is returned alongside
// the result so the caller can log it.
func (l *Launcher) Open(app *desktop.App) (Result, error) {
	if len(app.Argv) == 0 {
		return Result{}, fmt.Errorf("%s has no command to run", app.Name)
	}

	wins, err := l.Server.Windows()
	if err != nil {
		if spawnErr := l.spawn(app); spawnErr != nil {
			return Result{}, errors.Join(err, spawnErr)
		}
		return Result{}, fmt.Errorf("could not check for an existing window, started a new one: %w", err)
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
	defer devNull.Close() //nolint:errcheck // the child holds its own dup

	// exec.Command, not CommandContext: the whole point is a process that
	// outlives this one. Tying it to a context would kill the application
	// when the launcher exits, which is the opposite of the requirement.
	//nolint:noctx,gosec // detached by design; argv comes from the system's own .desktop entries
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Start in the user's home rather than wherever the launcher was
	// started, so a file dialog in the new app opens somewhere sensible.
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child when it exits. Without this every app launched in a
	// session leaves a zombie behind, since Setsid does not detach it from
	// us as a parent -- only from the terminal.
	//
	// The exit status is genuinely uninteresting: by the time it arrives
	// the user has moved on, and a GUI app exiting non-zero hours later is
	// not something the launcher can act on.
	go func() {
		//nolint:errcheck // see above: nothing can be done with the status
		_ = cmd.Wait()
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
func wrapInTerminal(argv []string) ([]string, error) {
	for _, term := range terminalCandidates {
		path, err := exec.LookPath(term)
		if err != nil {
			continue
		}
		return append([]string{path, "-e"}, argv...), nil
	}
	return nil, fmt.Errorf("no terminal emulator found (tried %v)", terminalCandidates)
}
