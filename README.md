# osxflow

Small, self-contained desktop tools for X11, written in Go. One static
binary each, no cgo, no runtime dependencies.

The name is the theme: this machine runs XFCE with macOS-shaped habits
(see [gokeyd](../gokeyd) for the keyboard half), and these are the pieces
that fill in what that arrangement is missing.

## Tools

| Tool | What it does |
| --- | --- |
| [`cmd/launcher`](cmd/launcher) | Keyboard launcher: fuzzy app search that learns what you use, focuses a window you already have instead of starting a second copy, and a calculator. Replaces ulauncher at ~15 MB instead of ~190 MB. |
| [`cmd/dock`](cmd/dock) | Magnifying, auto-hiding dock with running-app indicators and Downloads/Trash stacks. Replaces plank at ~19 MB instead of ~51 MB, and at the right size on a HiDPI screen. |

## Build

    make            # every tool, into bin/
    make launcher   # just one
    make test       # tests, with coverage
    make lint       # golangci-lint
    make fuzz       # the fuzz targets, briefly
    make icons      # re-rasterise the icon theme for cmd/dock
    make install    # into ~/.local/bin

Every binary is built with `CGO_ENABLED=0` and is statically linked.

## Adding a tool

Put it in `cmd/<name>/`. The Makefile picks it up with no changes, and
`go build ./...` builds it.

Anything reusable goes in `internal/` at the root, where every tool can
reach it. Most of what is there is shared rather than tool-specific:

- `internal/xwin` — listing X11 windows and focusing one, behind an
  interface with a fake for tests.
- `internal/desktop` — parsing `.desktop` entries and scanning the XDG
  application directories.
- `internal/launch` — deciding whether to focus an existing window or start
  a new process, and the reverse question the dock asks: which application
  does this window belong to?
- `internal/scale` — recovering the display scale factor X11 does not have.
- `internal/text` — loading a font and drawing strings, without fontconfig.
- `internal/paint` — rectangles, anti-aliased rounded rectangles and alpha
  compositing, from a signed distance field.
- `internal/xsurface` — getting an RGBA buffer onto a window.
- `internal/geom` — clamping Go's `int` into X11's 16-bit geometry.
- `internal/icons` — the icon set compiled into the dock.

## House rules

- `CGO_ENABLED=0`, always. It rules out GTK, fontconfig and GIO, which
  means some wheels get reinvented — deliberately.
- `golangci-lint` on the strict set in `.golangci.yml`, clean.
- Tests for anything that does not need a display, and fuzz targets for
  anything parsing live input.
- Display-server-specific code sits behind an interface with a fake, so
  the logic above it stays testable without X.
- Anything that has to be right but cannot be seen — a magnification curve,
  an easing, a layout that must not overlap — is pure and tested. The parts
  that need a display are kept thin enough to check by looking at them.
