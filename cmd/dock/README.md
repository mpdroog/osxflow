# dock

A macOS-style dock for XFCE: a magnifying, auto-hiding row of applications
and stacks along the bottom of the screen.

It replaces plank, which is a Vala/GTK3 program carrying GObject, Cairo,
libgee and dbusmenu behind it, uses about 51 MB, and draws this machine's
dock at roughly 26 physical pixels because it never applies the desktop's
2x scale factor.

    plank   51 MB   icons at ~26px
    dock    19 MB   icons at 96px, magnifying to 149px

## What it does

- **Magnifies** under the cursor, macOS-style: the icon beneath the pointer
  grows to 1.55x and its neighbours swell by distance along a raised
  cosine, so the effect has no visible edge where it starts and stops.
- **Auto-hides.** The pointer reaching the bottom edge of the screen brings
  it up; it slides away 350 ms after the pointer leaves. It reserves no
  screen space, so maximised windows keep the full height.
- **Shows what is running.** The five pinned applications are always in the
  same order in the same place; anything else that is running is appended
  after them and disappears when it exits. A dot below an icon means it has
  windows open.
- **Run-or-raise.** Clicking an icon focuses a window the application
  already has instead of starting a second copy, using the same matching
  the launcher does.
- **Stacks.** Downloads and Trash sit past a separator on the right.
  Clicking one opens the five most recent items and a row that opens the
  folder. The trash icon shows whether it is empty.

## Configuration

There is none, deliberately. The contents are `pinnedIDs` in `theme.go` and
every size is a constant in the same file. This is a dock for one person on
one machine; a settings file would be more code than the dock.

## Icons

The dock reads no icons at runtime. `tools/mkicons` rasterises the whole
icon theme at build time and the PNGs are compiled in, which is 2.2 MB of
binary and the reason a theme upgrade, a renamed directory or a malformed
SVG cannot break a dock that is already running.

That is not only durability, it is also the only way to get these icons at
all without cgo. The active theme (WhiteSur-dark) is 13,099 SVG files and
no PNGs, and they are not simple path-and-fill drawings: they layer a
base64-embedded raster shadow under vector plates under, sometimes, a
second embedded raster logo. The pure-Go SVG rasterisers get them visibly
wrong — oksvg drops every `<image>` element and mishandles transform lists
written without separators, which renders Firefox as a blank white square.
So rendering happens once, at build time, through librsvg, which is what
GTK itself draws with and therefore matches the rest of the desktop
exactly.

Run `make icons` after installing an application or changing icon theme.
An application with no embedded icon gets a tinted tile with its initial,
so a fresh install looks unfamiliar rather than broken.

## Installing

**1. Build and install the binary** into `~/.local/bin`:

    make dock
    install -m755 bin/dock ~/.local/bin/dock

**2. Start it at login** with an autostart entry:

    cat > ~/.config/autostart/dock.desktop <<EOF
    [Desktop Entry]
    Name=Dock
    GenericName=Dock
    Comment=macOS-style application dock (osxflow)
    Exec=$HOME/.local/bin/dock
    Terminal=false
    Type=Application
    X-GNOME-Autostart-enabled=true
    EOF

**3. Stop plank starting at login.** Keep a copy of its entry, then hide it:

    cp ~/.config/autostart/plank.desktop ~/.config/autostart/plank.desktop.bak
    printf 'Hidden=true\n' >> ~/.config/autostart/plank.desktop

If plank has no entry in `~/.config/autostart/`, copy the system one there
first, from `/etc/xdg/autostart/` or `/usr/share/applications/`.

**4. Switch over now**, without logging out:

    pkill -x plank
    ~/.local/bin/dock &

To go back, restore `plank.desktop.bak`, delete `dock.desktop`, and log out
and back in.

## Debugging

`go run ./tools/dockstate` prints every window, which application the dock
matched it to and why, and the item list it would build. That answers the
question this design actually raises in practice: why is something showing
up in the dock, or not?

## Requirements

A compositor, for the translucent panel: the dock draws on a 32-bit ARGB
visual and says so rather than starting if there is not one. On XFCE that
is Settings → Window Manager Tweaks → Compositor.

## Notes on the implementation

The dock is two override-redirect windows: the panel itself, and an
invisible `InputOnly` strip three pixels tall across the bottom of the
screen whose only job is to notice the pointer arriving.

Sliding in and out moves the window rather than redrawing it — the panel's
pixels do not change as it appears, only where they are — so the animation
costs one `ConfigureWindow` per frame instead of a megabyte of pixels.
