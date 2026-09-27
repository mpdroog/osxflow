package main

// The running desktop, as the checks see it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/xfconf"
)

// liveSystem is the real one. bus is nil when there is no session bus.
type liveSystem struct {
	bus  *dbus.Conn
	proc string // /proc
}

var _ system = (*liveSystem)(nil)

func (s *liveSystem) Processes() (map[string]bool, error) {
	dirs, err := os.ReadDir(s.proc)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.proc, err)
	}
	procs := map[string]bool{}
	for _, d := range dirs {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue // not a process: /proc/self, /proc/cpuinfo and friends
		}
		comm, err := os.ReadFile(filepath.Join(s.proc, d.Name(), "comm"))
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH):
			// It exited between ReadDir and here.
			continue
		case err != nil:
			return nil, fmt.Errorf("naming process %s: %w", d.Name(), err)
		}
		procs[strings.TrimSpace(string(comm))] = true
	}
	return procs, nil
}

func (s *liveSystem) BusOwner(name string) (comm string, owned bool, err error) {
	if s.bus == nil {
		return "", false, errNoBus
	}
	var hasOwner bool
	if err = s.bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&hasOwner); err != nil {
		return "", false, fmt.Errorf("asking the bus about %s: %w", name, err)
	}
	if !hasOwner {
		return "", false, nil
	}
	var pid uint32
	if err = s.bus.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&pid); err != nil {
		return "", true, fmt.Errorf("finding the process owning %s: %w", name, err)
	}
	data, err := os.ReadFile(filepath.Join(s.proc, strconv.FormatUint(uint64(pid), 10), "comm"))
	if err != nil {
		return "", true, fmt.Errorf("naming process %d, which owns %s: %w", pid, name, err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func (s *liveSystem) XfconfAll(channel, base string) (map[string]any, error) {
	if s.bus == nil {
		return nil, errNoBus
	}
	props, err := xfconf.New(s.bus).GetAll(channel, base)
	if err != nil {
		return nil, err
	}
	return props, nil
}

func (s *liveSystem) LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("looking for %s: %w", name, err)
	}
	return path, nil
}

func (s *liveSystem) Getenv(name string) string { return os.Getenv(name) }

// connectBus connects to the session bus, or says why it did not. No bus is
// not a failure -- a login with no desktop session running, say -- and the
// checks needing one are skipped. Over SSH while the desktop is up, godbus
// finds the session's bus through XDG_RUNTIME_DIR, so everything is
// checked.
func connectBus() (*dbus.Conn, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, errors.Join(errNoBus, err)
	}
	return conn, nil
}
