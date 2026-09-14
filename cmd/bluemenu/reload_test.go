package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// The outcomes come from real exit statuses, so the test also checks how
// exec reports them, not only the switch on top.
func TestReloadOutcome(t *testing.T) {
	exitWith := func(code int) error {
		err := exec.CommandContext(t.Context(), "sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		if err == nil {
			t.Fatalf("sh exiting %d reported success", code)
		}
		return err
	}
	for _, tc := range []struct {
		name   string
		err    error
		out    string
		want   string
		failed bool
	}{
		{"success", nil, "", "Bluetooth driver reloaded", false},
		{"dialog dismissed", exitWith(pkexecDismissed), "", "reloading the Bluetooth driver: the password dialog was dismissed", false},
		{"not authorised", exitWith(pkexecNotAuthorized), "Error executing command as another user: Not authorized\n",
			"reloading the Bluetooth driver: not authorised: Error executing command as another user: Not authorized", true},
		{"modprobe failed", exitWith(1), "modprobe: FATAL: Module hci_uart is in use.\n",
			"reloading the Bluetooth driver: exit status 1: modprobe: FATAL: Module hci_uart is in use.", true},
		{"pkexec missing", errors.New(`exec: "pkexec": executable file not found in $PATH`), "",
			`reloading the Bluetooth driver: exec: "pkexec": executable file not found in $PATH`, true},
	} {
		msg, failed := reloadOutcome(tc.err, []byte(tc.out))
		if msg != tc.want || failed != tc.failed {
			t.Errorf("%s: reloadOutcome = %q, %t; want %q, %t", tc.name, msg, failed, tc.want, tc.failed)
		}
	}
}
