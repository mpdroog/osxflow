# powermenu

A battery menu in the panel's status tray, the way macOS does it: a battery
icon that opens a short menu.

- **Battery.** The charge, the time left or until full, whether it is
  running on battery or the adapter, and the battery's health (capacity
  against its design, and charge cycles).
- **Display and Keyboard.** A brightness slider for the screen and, where
  there is one, the keyboard backlight. Drag it, or scroll over it.
- **Presentation mode.** A switch that keeps the screen from blanking and
  the machine from sleeping.
- **Power Settings…** opens xfce4-power-manager-settings.

The icon is a battery filled to its charge: white normally, green while
charging, red at 20% and below. The tooltip gives the percentage and the
time left.

## What it replaces, and what it does not

It replaces the XFCE panel's **power manager plugin** -- only the plugin.
The **xfce4-power-manager** daemon keeps running, and has to: it handles
the lid, suspend, screen blanking and the brightness keys. Presentation
mode is the daemon's own setting, which powermenu reads and writes through
xfconf, so the switch and the daemon always agree.

The brightness keys keep going through xfce4-power-manager. While the menu
is open it re-reads the brightness every second, so the sliders follow the
keys; with it closed nothing is polled.

Battery state comes from UPower and brightness is set through logind, both
over D-Bus and neither needing root.

## Installing

**1. Build and install the binary:**

    make powermenu
    install -m755 bin/powermenu ~/.local/bin/powermenu

**2. Start it at login:**

    printf '[Desktop Entry]\nType=Application\nName=Battery Menu\nExec=%s/.local/bin/powermenu\nX-GNOME-Autostart-enabled=true\n' "$HOME" \
      > ~/.config/autostart/powermenu.desktop

**3. Take the power manager plugin off the panel.** Find its id, and the
panel's list of plugin ids:

    xfconf-query -c xfce4-panel -lv | grep power-manager-plugin   # /plugins/plugin-11 here
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids       # 1 7 8 10 11 12 13 here

Keep a copy of the list, then set it again without that id, and restart the
panel. The plugin's own settings stay under `/plugins/plugin-11`, so putting
the id back restores it as it was:

    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids > ~/.config/xfce4/panel-plugin-ids.bak
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 1 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 12 -t int -s 13
    xfce4-panel -r

**4. Start it now:**

    setsid -f sh -c 'exec "$HOME/.local/bin/powermenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

### Updating

    make powermenu && install -m755 bin/powermenu ~/.local/bin/powermenu
    pkill -x powermenu
    setsid -f sh -c 'exec "$HOME/.local/bin/powermenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

### Uninstalling

Put the plugin's id back where it was in the list, restart the panel, and
stop powermenu:

    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 1 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 11 -t int -s 12 -t int -s 13
    xfce4-panel -r
    rm ~/.config/autostart/powermenu.desktop
    pkill -x powermenu

## Flags

    -scale 2   display scale factor (0, the default, detects it)
    -v         log tray registrations, clicks and icon changes
