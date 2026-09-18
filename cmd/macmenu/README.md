# macmenu

The system menu at the left end of the panel, the way macOS has one under
the Apple logo:

- **About This Linux** -- model, system, kernel, memory and uptime.
- **System Settings…** -- xfce4-settings-manager.
- **Task Manager…** -- xfce4-taskmanager.
- **Sleep**, **Restart**, **Shut Down** -- each one happens on the click.
- **Lock Screen** and **Log Out**.

Apps are not in it: finding and starting them is the launcher's job
(Cmd+Space).

## How it runs

Nothing stays running. A panel launcher starts macmenu when it is clicked;
it opens under the pointer, does what was chosen, and exits. Clicking the
launcher again while the menu is open closes it: the second macmenu finds
the first on the session bus (`org.osxflow.MacMenu`) and tells it to close.

Sleep, restart, shut down and log out go through `xfce4-session-logout`,
so they behave as the session's own dialog would -- the session is saved,
and the screen is locked before sleeping -- only without that dialog, and
without one of its own: a row here does what it says straight away.
Locking is `xflock4`, which uses whichever locker XFCE is set up with.

## Installing

**1. Build and install the binary:**

    make macmenu
    install -m755 bin/macmenu ~/.local/bin/macmenu

**2. Add a launcher for it at the left end of the panel.** Pick a plugin
id that is not taken (`xfconf-query -c xfce4-panel -l | grep plugin-`),
15 here (1-14 were taken), give it a launcher item, and put its id first in the panel's list:

    mkdir -p ~/.config/xfce4/panel/launcher-15
    printf '[Desktop Entry]\nType=Application\nName=macmenu\nExec=%s/.local/bin/macmenu\nIcon=linuxmint-logo-simple-symbolic\n' "$HOME" \
      > ~/.config/xfce4/panel/launcher-15/macmenu.desktop
    xfconf-query -c xfce4-panel -p /plugins/plugin-15 -n -t string -s launcher
    xfconf-query -c xfce4-panel -p /plugins/plugin-15/items -n --force-array -t string -s macmenu.desktop
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 15 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 13
    xfce4-panel -r

That list leaves out Whisker Menu (plugin 1); keep `-t int -s 1` in it to
have both while trying macmenu out.

### Uninstalling

Put Whisker Menu's id back in place of 15 and restart the panel:

    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 1 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 13
    xfce4-panel -r

## Flags

    -scale 2   display scale factor (0, the default, detects it)
