// Package dbustest runs a private D-Bus daemon for tests.
//
// Testing against the real session bus would mean fighting the desktop's
// own notification daemon for its name -- and, if the test won, taking
// the desktop's notifications away for as long as it ran. A private bus
// has nobody else on it.
package dbustest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		t.Skip("dbus-daemon not installed")
	}
	path := filepath.Join(t.TempDir(), "bus.conf")
	name := fmt.Sprintf("osxflow-dbustest-%d-%d", os.Getpid(), seq.Add(1))
	if writeErr := os.WriteFile(path, fmt.Appendf(nil, config, name), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	//nolint:gosec // bin is dbus-daemon from PATH and the config is one this function wrote
	cmd := exec.CommandContext(t.Context(), bin, "--config-file="+path, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill() //nolint:errcheck // it may already have gone with the context
		_ = cmd.Wait()         //nolint:errcheck // killed on purpose
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the bus address: %v", err)
	}
	return strings.TrimSpace(line)
}

// Conn connects to the bus at addr for the life of the test.
func Conn(t testing.TB, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connecting to %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() }) //nolint:errcheck // test teardown
	return conn
}
