# osxflow

Small, self-contained desktop tools for X11, written in Go. One static
binary each, no cgo, no runtime dependencies.

The name is the theme: this machine runs XFCE with macOS-shaped habits
(see [gokeyd](https://github.com/mpdroog/gokeyd) for the keyboard half), and these are the pieces
that fill in what that arrangement is missing.

## Tools

| Tool | What it does |
| --- | --- |
| [`cmd/launcher`](cmd/launcher) | Keyboard launcher: fuzzy app search that learns what you use, focuses a window you already have instead of starting a second copy, and a calculator. Replaces ulauncher at ~15 MB instead of ~190 MB. |
| [`cmd/dock`](cmd/dock) | Magnifying, auto-hiding dock with running-app indicators and Downloads/Trash stacks. Replaces plank at ~19 MB instead of ~51 MB, and at the right size on a HiDPI screen. |
| [`cmd/notifyd`](cmd/notifyd) | macOS-style notification banners: slide in top right, pause while hovered, actions as buttons, honours the panel's do-not-disturb. Replaces xfce4-notifyd at ~19 MB instead of ~35 MB. |
| [`cmd/netmenu`](cmd/netmenu) | Network menu in the status tray: Wi-Fi switch, nearby networks, one-click join for known and open ones, VPN switches, and nm-connection-editor for everything else. Replaces nm-applet's icon and GTK menu, and deliberately not its secret agent. |
| [`cmd/powermenu`](cmd/powermenu) | Battery menu in the status tray: charge, time left, battery health, screen and keyboard brightness sliders, presentation mode. Replaces the panel's power manager plugin; the power manager itself keeps running. |
| [`cmd/macmenu`](cmd/macmenu) | The system menu at the left of the panel, as under macOS's Apple logo: About This Mac, settings, task manager, sleep, restart, shut down, lock, log out. Runs only while open. Replaces Whisker Menu; apps stay with the launcher. |
| [`cmd/bluemenu`](cmd/bluemenu) | Bluetooth menu in the status tray: radio switch, paired devices with connect/disconnect and battery, and the pairing agent's prompts. Replaces blueman's tray icon and applet. |
| [`cmd/soundmenu`](cmd/soundmenu) | Sound menu in the status tray: volume, output and input devices, microphone, the media that is playing -- plus the volume keys and macOS's volume overlay. Replaces the panel's PulseAudio plugin. |

## Build

    make            # every tool, into bin/
    make launcher   # just one
    make test       # tests, with coverage
    make lint       # golangci-lint
    make fuzz       # the fuzz targets, briefly
    make icons      # re-rasterise the icon theme for cmd/dock
    make install    # into ~/.local/bin

Every binary is built with `CGO_ENABLED=0` and is statically linked.

`make install` only copies the binaries. Each tool replaces something
XFCE already runs, and its own README says how to switch over, and back:

- `launcher` is bound to a keyboard shortcut and replaces ulauncher — see
  [Use](cmd/launcher/README.md#use).
- `dock` is started at login and replaces plank — see
  [Installing](cmd/dock/README.md#installing).
- `notifyd` is started by the session bus and replaces xfce4-notifyd —
  see [Installing](cmd/notifyd/README.md#installing).
- `netmenu` is started at login and replaces nm-applet, once every VPN's
  secrets are stored with NetworkManager — see
  [Installing](cmd/netmenu/README.md#installing).
- `powermenu` and `soundmenu` are started at login and replace two panel
  plugins, which come off the panel — see
  [powermenu](cmd/powermenu/README.md#installing) and
  [soundmenu](cmd/soundmenu/README.md#installing).

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
- `internal/menu` — the tray tools' dropdown: a list of rows (switches,
  sliders, device lists, media buttons) in a translucent X11 window, with
  hovering, dragging and dismissal handled.
- `internal/glyph` — small anti-aliased symbols from signed distance
  functions: Wi-Fi, battery, speaker, microphone, media buttons.
- `internal/sni` — an icon in the status tray via StatusNotifierItem,
  which is D-Bus and so works under Wayland panels too.
- `internal/netmgr` — NetworkManager over the system bus: a snapshot of
  Wi-Fi, networks and VPNs, and the few calls that change them.
- `internal/upower` and `internal/backlight` — the battery from UPower,
  and screen and keyboard brightness from sysfs, set through logind.
- `internal/audio` and `internal/mpris` — PipeWire's PulseAudio server in
  pure Go, and media players on the session bus.
- `internal/dbustest` — a private D-Bus daemon for tests, so nothing
  touches the desktop's own session or system bus.

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
