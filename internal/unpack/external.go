package unpack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// extractExternal hands 7z and rar archives to 7-Zip, which has no Go
// equivalent worth depending on. 7-Zip strips absolute paths and ".."
// from entry names itself.
func extractExternal(ctx context.Context, path, scratch string) error {
	bin, err := sevenZip()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	// -p with no password makes an encrypted archive fail rather than
	// wait for a password on a terminal nobody is looking at.
	cmd := exec.CommandContext(ctx, bin, "x", "-y", "-p", "-bso0", "-bsp0", "-o"+scratch, "--", path) //nolint:gosec // fixed program; the archive is a single argument after --
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "Wrong password") || strings.Contains(msg, "encrypted") {
			return ErrEncrypted
		}
		if msg == "" {
			return fmt.Errorf("%s: %w", bin, err)
		}
		return fmt.Errorf("%s: %w: %s", bin, err, lastLine(msg))
	}
	return nil
}

func sevenZip() (string, error) {
	for _, name := range []string{"7z", "7zz", "7za"} {
		p, err := exec.LookPath(name)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("looking for %s: %w", name, err)
		}
	}
	return "", errors.New("7z and rar archives need 7-Zip (apt install 7zip)")
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
