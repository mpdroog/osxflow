# tile

Every window on screen, side by side: each monitor's windows in a grid
that fills it without overlap, laid out again whenever a window opens,
closes, is restored or moves to another monitor.

- One window fills the monitor, two split it, three are three columns.
  Four to eight go in two rows, nine in three. When the grid is not full,
  the oldest windows get the leftover room: five is one full-height
  column and two columns of two.
- Windows keep their place by age, not focus, so clicking around never
  reshuffles the screen; a new window goes at the end.
- Left alone: minimised and fullscreen windows, dialogs, and any past the
  ninth on one monitor.
- Focus is given back to the window that had it.

Switch it on and off from the Tux menu: **Tile Windows**. It stays as you
left it across logins.

    tile -enable    remember on, and tile
    tile -disable   remember off, and stop the running tile
    tile -session   tile only if left on -- for labwc's autostart

The state is the file `$XDG_STATE_HOME/osxflow/tile`; the running tile owns
`org.osxflow.Tile` on the session bus, which is how macmenu tells whether
it is on.

## How it moves windows it cannot move

Under Wayland a client cannot place another client's window, and labwc has
no IPC: wlr-foreign-toplevel can focus, minimise and close a window but not
move it. What labwc can do is snap the focused window into a named
*region*, given in percent of the monitor's usable area, from a keybind.

So the layouts are fixed in advance. For 1 to 9 windows there are 45
regions, and each has a chord: Super+Ctrl+Alt+Shift or Super+Ctrl+Shift
with F13 to F35, which no real keyboard can press. tile creates a virtual
keyboard (`zwp_virtual_keyboard_v1`) whose keymap has exactly those keys,
then focuses each window in turn and presses its region's chord.

The regions and chords live in `~/.config/labwc/rc.xml`, between
`<!-- osxflow tile: BEGIN` and `END -->`. tile writes that block itself on
every start and runs `labwc --reconfigure` when it changed; everything else
in rc.xml is left as it was. Change the layouts in [layout.go](layout.go),
not in rc.xml.

Costs of doing it this way: cells are whole percents (a third is 33, 34
and 33), and each re-layout briefly focuses every window it places.

Only labwc. Under XFCE it says so and exits, and macmenu shows no row.

## Installing

    make tile macmenu
    install -m755 bin/tile bin/macmenu ~/.local/bin/

and in `~/.config/labwc/autostart`:

    "$HOME/.local/bin/tile" -session &

`tile` must be beside `macmenu`: the menu runs it from there.
