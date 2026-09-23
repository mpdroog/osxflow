# macmenu

The system menu at the left end of the panel, the way macOS has one under
the Apple logo:

- **About This Linux** -- model, system, kernel, memory and uptime.
- **Task Manager…** -- `top` in a foot window.
- **Sleep**, **Restart**, **Shut Down** -- each one happens on the click.
- **Lock Screen** and **Log Out**.

Apps are not in it: finding and starting them is the launcher's job
(Cmd+Space).

## How it runs

Nothing stays running. [menubar](../menubar/README.md) starts macmenu when
Tux is clicked (under waybar, a custom module does; under xfce4-panel, a
panel launcher did); it opens under the pointer, does what was chosen, and
exits. Clicking the Tux again while the menu is open closes it: the second
macmenu finds the first on the session bus (`org.osxflow.MacMenu`) and
tells it to close.

It is **not** a tray item and must never be started from the session's
autostart: backgrounded at login it opens its menu once, against nobody,
and exits.

## What the rows run

On Alpine there is no XFCE and no elogind, so none of what this menu was
first written against exists here -- `xfce4-session-logout`, `xflock4`,
`xfce4-settings-manager`, `xfce4-taskmanager`. A row whose program is
missing is the worst kind of dead: the menu closes and nothing happens,
with the error going to a stderr nobody reads. So the rows are:

| Row | Command |
| --- | --- |
| Task Manager… | `foot -T "Task Manager" top` |
| Sleep | `swaylock -f -c 000000 && doas /usr/local/sbin/osxflow-suspend` |
| Restart | `doas /sbin/reboot` |
| Shut Down | `doas /sbin/poweroff` |
| Lock Screen | `swaylock -f -c 000000` |
| Log Out | `labwc --exit` |

Sleep locks before suspending, the way the XFCE dialog did; `swaylock -f`
returns once the screen is actually covered, so the lock is up before the
machine goes down. There is no System Settings row: nothing on Alpine
answers to that.

## Installing

**1. Build and install the binary:**

    make macmenu
    install -m755 bin/macmenu ~/.local/bin/macmenu

**2. Let the three power rows work.** They need root, and this machine has
no elogind to ask, so they go through doas. As root:

    install -m644 files/doas/30-osxflow-power.conf /etc/doas.d/30-osxflow-power.conf
    mkdir -p /usr/local/sbin
    install -m755 files/doas/osxflow-suspend /usr/local/sbin/osxflow-suspend

Alpine's `/usr/local` holds only `bin`, so `/usr/local/sbin` has to be made
first or the second `install` fails with "No such file or directory". Keep
that path: the rule below names the helper in full, and installing it
somewhere else leaves Sleep quietly doing nothing.

The rule names all three commands in full, so it grants those and nothing
else -- check it before and after installing with:

    doas -C files/doas/30-osxflow-power.conf -u mp /sbin/poweroff   # permit nopass
    doas -C files/doas/30-osxflow-power.conf -u mp /bin/sh          # deny

Without this the other rows still work; Sleep, Restart and Shut Down do
nothing.

**3. Add the button to the bar.** With
[menubar](../menubar/README.md) there is nothing to do: Tux runs macmenu
from beside menubar's own binary. Under waybar, put it first in
`modules-left`, where macOS keeps the Apple menu:

    "modules-left": ["custom/macmenu", "wlr/taskbar"],

    "custom/macmenu": {
        "format": "",
        "tooltip": false,
        "on-click": "/home/mp/.local/bin/macmenu"
    },

The icon is Tux's head at U+F17C, a Nerd Font glyph, not [macmenu.svg](macmenu.svg):
waybar draws images through gdk-pixbuf, and this machine has only the xpm
loader -- no librsvg -- so an SVG on the bar draws nothing at all, silently.
The bar's own font (FontAwesome, which resolves to Noto Sans here) has no
glyph there either, so `~/.config/waybar/style.css` names one that does:

    #custom-macmenu {
        font-family: "JetBrainsMono Nerd Font", "JetBrainsMonoNL Nerd Font", monospace;
        font-size: 15px;
        padding: 0 10px;
    }

waybar must be started with `OSXFLOW_PANEL_HEIGHT` set to its real height
(the session's autostart does this), because macmenu inherits waybar's
environment and needs it to drop the menu below the bar rather than a few
pixels inside it.

### Uninstalling

Take `"custom/macmenu"` back out of `modules-left`, and remove
`/etc/doas.d/30-osxflow-power.conf` and `/usr/local/sbin/osxflow-suspend`.

## Flags

    -scale 2   display scale factor (0, the default, detects it)
