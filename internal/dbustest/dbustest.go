// Package dbustest runs a private D-Bus daemon for tests.
//
// Testing against the real session bus would mean fighting the desktop's
// own notification daemon for its name -- and, if the test won, taking
// the desktop's notifications away for as long as it ran. A private bus
// has nobody else on it.
package dbustest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/godbus/dbus/v5"
)

var seq atomic.Uint64

// config is the least a bus needs: somewhere to listen, and a policy
// allowing everything, which on a bus only this test can reach is fine.
//
// The socket is abstract rather than a file because go test's temporary
// directories easily exceed the 108 bytes a socket path may have.
const config = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:abstract=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// Start runs a bus for the life of the test and returns its address. The
// test is skipped when dbus-daemon is not installed.
func Start(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("dbus-daemon not installed: %v", err)
		}
		t.Fatalf("looking for dbus-daemon: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bus.conf")
	name := fmt.Sprintf("osxflow-dbustest-%d-%d", os.Getpid(), seq.Add(1))
	if writeErr := os.WriteFile(path, fmt.Appendf(nil, config, name), 0o600); writeErr != nil {
		t.Fatalf("writing the bus configuration: %v", writeErr)
	}

	// Both outputs go to writers rather than pipes. exec then copies them
	// itself and Wait waits for that, so nothing is left blocked on a full
	// pipe after the first line, and the daemon's complaints are there to
	// show when it fails to start.
	addr := newLineWriter()
	var stderr syncBuffer
	// Not CommandContext with the test's context: that is cancelled before
	// any cleanup runs, which would kill the bus while connections to it
	// were still being closed -- and turn every test's tidy shutdown into a
	// "connection reset". The cleanup registered below kills it instead,
	// and cleanups run last-registered first, so after every connection.
	//nolint:gosec,noctx // bin is dbus-daemon from PATH and the config is one this function wrote; the lifetime is the cleanup's
	cmd := exec.Command(bin, "--config-file="+path, "--nofork", "--print-address=1")
	cmd.Stdout, cmd.Stderr = addr, &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("starting dbus-daemon: %v", startErr)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case line := <-addr.line:
		t.Cleanup(func() { stop(t, cmd, waited) })
		return strings.TrimSpace(line)
	case waitErr := <-waited:
		t.Fatalf("dbus-daemon exited before printing its address (%v); its stderr: %q", waitErr, stderr.String())
	}
	return "" // not reached: Fatalf does not return
}

// stop kills the daemon and reaps it. Being killed is how it is meant to
// end, so the exit status that reports it is expected; anything else is
// not.
func stop(t testing.TB, cmd *exec.Cmd, waited <-chan error) {
	t.Helper()
	// ErrProcessDone means it had already exited, which the wait below
	// reports on.
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("killing dbus-daemon: %v", err)
	}
	err := <-waited
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		t.Logf("dbus-daemon stopped: %v", err)
	default:
		t.Errorf("waiting for dbus-daemon: %v", err)
	}
}

// Conn connects to the bus at addr for the life of the test.
func Conn(t testing.TB, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connecting to %s: %v", addr, err)
	}
	t.Cleanup(func() {
		// Logged rather than failed: a test may have closed the connection
		// itself, and closing it twice is an error that proves nothing.
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("closing the connection to %s: %v", addr, closeErr)
		}
	})
	return conn
}

// lineWriter hands over the first line written to it and discards the
// rest.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	done bool
	line chan string
}

func newLineWriter() *lineWriter { return &lineWriter{line: make(chan string, 1)} }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if i := bytes.IndexByte(w.buf, '\n'); i >= 0 {
		w.done = true
		w.line <- string(w.buf[:i])
		w.buf = nil
	}
	return len(p), nil
}

// syncBuffer is a bytes.Buffer that exec's copying goroutine can write
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
