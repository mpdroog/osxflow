package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The Copy button of a -copy message.
//
// It shells out instead of owning the X selection itself: an X selection
// lives only as long as the window that owns it, and the whole point is
// that the text is still there to paste after the dialog is closed. The
// tools below hand it to a process that outlives us.

// copyTimeout bounds the helper. wl-copy forks a server for the selection
// and returns at once; anything slower has hung.
const copyTimeout = 5 * time.Second

var errNoClipboard = errors.New("no clipboard tool found (wl-copy, xclip or xsel)")

type clipTool struct {
	name string
	args []string
}

func copyToClipboard(s string) error {
	tool, err := clipboardTool()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool.name, tool.args...) //nolint:gosec // one of clipboardTool's fixed candidates
	cmd.Stdin = strings.NewReader(s)
	// No stdout/stderr pipes: wl-copy's forked server keeps them open for as
	// long as it owns the selection, and Run would wait on that.
	return cmd.Run()
}

// clipboardTool prefers the Wayland clipboard on a Wayland desktop: this
// window is on XWayland, but what gets pasted into is mostly native.
func clipboardTool() (clipTool, error) {
	candidates := []clipTool{
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		candidates = append([]clipTool{{name: "wl-copy"}}, candidates...)
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err == nil {
			return c, nil
		}
	}
	return clipTool{}, errNoClipboard
}
