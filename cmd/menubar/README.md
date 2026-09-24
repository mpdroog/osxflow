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
xfce4-panel first (`xfce4-panel --quit`).

The bar is 29 logical pixels high, 58 at 2x, as the panel was, and
reserves that strip with `_NET_WM_STRUT_PARTIAL`, so maximised windows stop
below it. Menus open just below it.

## Installing

    make menubar
    install -m755 bin/menubar ~/.local/bin/menubar

`macmenu` must be installed in the same directory: Tux runs it from there.

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
