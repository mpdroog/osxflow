# menubar

The bar across the top of the screen, the way macOS has one:

- **Tux**, at the left, opens [macmenu](../macmenu/README.md).
- **The focused application's name** beside it: the name from its
  `.desktop` entry, or its `WM_CLASS` when it has none.
- **The tray**, at the right: osxflow's power, network, Bluetooth and sound
  menus next to the clock, in that order, and anything else further left
  in the order it appeared.
- **The clock**, which opens your calendar in Firefox when clicked:
  `http://ical.rootdev.nl`, or whatever `-calendar` says (`-calendar ''`
  turns the click off).

It replaces xfce4-panel. Running applications are not on it; they are the
dock's.

## The tray

menubar is the tray: it owns `org.kde.StatusNotifierWatcher`, so every
StatusNotifierItem registers with it. osxflow's menus, ezhours, and anything
using libappindicator, Qt or fyne's systray show up.

- Icons are drawn on the bar as they are, with no box behind them. The
  one under the pointer gets a faint highlight.
- A click goes to the item, which opens its own menu (osxflow's tools do).
- An item that publishes its menu over D-Bus (`com.canonical.dbusmenu`,
  as ezhours does) gets it drawn by menubar, in osxflow's menu style, not
  GTK's. Submenus are flattened under a heading, and checkmarks become
  switches.
- An item whose status is `Passive` is hidden until it changes.

Not supported:

- **Legacy XEmbed tray icons** (`_NET_SYSTEM_TRAY`) and **XApp status
  icons** (Mint's `org.x.StatusIcon`). Here that means mintreport-tray and
  the printer applet, which only show an icon when they have something to
  report.
- **Icons by theme name** (`IconName`), since the theme is SVG and can't be
  drawn without cgo. An item that sends only a name gets its initial.

## How it runs

It starts from XDG autostart and runs for the session. Its errors go to
`~/.xsession-errors`, prefixed `menubar:`.

It refuses to start while another tray owns the watcher name: stop
xfce4-panel first (`xfce4-panel --quit`), or waybar under labwc.

The bar is 29 logical pixels high, 58 at 2x, as the panel was, and reserves
that strip, so maximised windows stop below it. Menus open just below it.

## X11 and Wayland

The strip is the one part of menubar that the display server changes. The
tray is D-Bus, the menus are drawn through XWayland like every other
osxflow menu, and the fonts and the painting never knew what a display
server was.

| | X11 | Wayland |
| --- | --- | --- |
| The bar | an override-managed dock window | a `zwlr_layer_shell_v1` surface |
| Its strip | `_NET_WM_STRUT_PARTIAL` | the layer surface's exclusive zone |
| How many | one, across the whole X screen | one per monitor |
| Scale | the X screen's | the monitor's, `wp_fractional_scale_v1` |
| Pointer | X button and motion events | `wl_pointer` |
| Focused window | `_NET_ACTIVE_WINDOW` | `zwlr_foreign_toplevel`'s activated handle |

Which one it is is decided by `WAYLAND_DISPLAY`, at startup, and logged.

**Why not just keep the X11 bar under labwc.** Because the complaint that
first comes to mind is not the one that matters. labwc does honour a strut
from an XWayland dock -- with the X11 bar up, `_NET_WORKAREA` read
`0, 29, 6000, 1411`, which is the strip reserved. What it cannot do is give
that one window a sensible shape. The X screen is the union of every
monitor, 6000 logical pixels across a 3440-wide ultrawide at scale 1 and a
4K panel at 1.5, so the bar is one strip at one scale spanning both. In
practice it was drawn across the left-hand monitor only, ending at that
monitor's edge with the clock and the tray off the end of it, and the
focused application's name was blank because `_NET_CLIENT_LIST` does not
exist on that root.

A layer-shell surface is per monitor by construction, and the fractional
scale is exact: on the 4K panel at 1.5 a one-pixel rule drawn at the bottom
of the bar comes back as one pixel, not two grey ones.

**A bar on every monitor**, the way macOS puts one on every display. Each
is its own surface on its own output, laid out for that monitor's width and
drawn at that monitor's scale, and each reserves its own strip: a maximised
window on the 4K panel starts at the pixel row the bar ends on.

What that costs is that nothing measured in pixels can be shared. The fonts,
Tux and every tray icon exist once per scale in use -- twice on this desk,
at 1 and 1.5. They are cached by scale rather than held per bar, so two
monitors that agree share one set and a laptop with one screen pays nothing.

Monitors may come and go while it runs. A screen switched on gets a bar, one
switched off has its bar closed, and menubar only gives up when the last one
is gone.

## Installing

    make menubar
    install -m755 bin/menubar ~/.local/bin/menubar

`macmenu` must be installed in the same directory: Tux runs it from there.

Under labwc, **stop waybar first** -- it owns the tray name that menubar
needs, and two bars would reserve two strips:

    pkill -x waybar
    menubar &

To go back, start waybar again. Nothing has to be reconfigured either way.

**Autostart** with `~/.config/autostart/menubar.desktop`:

    [Desktop Entry]
    Type=Application
    Name=Menu Bar
    Exec=/home/mp/.local/bin/menubar
    X-GNOME-Autostart-enabled=true

**Retire xfce4-panel.** It is not an autostart entry but a client of XFCE's
Failsafe session (`/sessions/Failsafe/Client2_*` here). Take it out by
moving the clients after it down one and lowering `/sessions/Failsafe/Count`.
Put it back by reversing that, or for now with `xfce4-panel &`.
