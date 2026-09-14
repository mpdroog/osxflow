# bluemenu

A Bluetooth menu in the panel's status tray, the way macOS does it: a
Bluetooth icon that opens a short menu, and a small prompt when a device
asks to pair.

- **Bluetooth.** A switch for the radio. When it cannot be switched it
  says why: a hardware switch, bluetoothd not running, or an adapter that
  is not responding.
- **Devices.** The paired devices, connected ones first, with their battery
  where they report one. Click one to connect or disconnect it.
- **Bluetooth Settings…** opens blueman-manager, which is where pairing a
  new device starts.

The icon is the Bluetooth rune: white when on, faint when off, with a dot
while a device is connected.

## Pairing

bluemenu is BlueZ's **pairing agent**: when a device being paired needs a
confirmation, the question arrives here as a prompt under the panel.

- *Pair if this code matches the device* -- the code a phone or a headset
  shows. **Pair** or **Cancel**.
- *Type this code on the device* -- for keyboards. **Dismiss** once done.
- *wants to use audio / input / calls…* -- a device asking for a service
  before it is trusted. **Allow** or **Deny**.

Clicking away from a prompt answers no. A device that needs a code typed
in on this computer -- old devices asking for a PIN -- is refused, and the
prompt says to pair it with `bluetoothctl` instead.

## Power

The switch uses the kernel's radio kill switch (`/dev/rfkill`, which the
logged-in user may write to), so off really is off, and it survives a
restart of bluetoothd. Turning it back on lets BlueZ power the adapter
again (`AutoEnable=true` in `/etc/bluetooth/main.conf`).

On this MacBook the controller sometimes fails to start at boot
(`hci0: BCM: Reset failed (-110)` in the kernel log). The menu then says the
adapter is not responding, and offers **Reload Bluetooth Driver…**, which
runs this through pkexec -- polkit asks for your password each time:

    modprobe -r hci_uart && modprobe hci_uart

Dismissing the password dialog just leaves things as they were.

## Installing

**1. Build and install the binary:**

    make bluemenu
    install -m755 bin/bluemenu ~/.local/bin/bluemenu

**2. Start it at login:**

    printf '[Desktop Entry]\nType=Application\nName=Bluetooth Menu\nExec=%s/.local/bin/bluemenu\nX-GNOME-Autostart-enabled=true\n' "$HOME" \
      > ~/.config/autostart/bluemenu.desktop

**3. Retire blueman.** Its applet is the other pairing agent, so it has to
stay off, and three things can start it: autostart, D-Bus activation (which
blueman-manager uses) and a systemd user unit. The package stays
installed:

    cp /etc/xdg/autostart/blueman.desktop ~/.config/autostart/blueman.desktop.bak
    cp /etc/xdg/autostart/blueman.desktop ~/.config/autostart/
    printf 'Hidden=true\nX-GNOME-Autostart-enabled=false\n' >> ~/.config/autostart/blueman.desktop
    mkdir -p ~/.local/share/dbus-1/services
    printf '[D-BUS Service]\nName=org.blueman.Applet\nExec=/bin/false\n' \
      > ~/.local/share/dbus-1/services/org.blueman.Applet.service
    systemctl --user mask blueman-applet.service
    pkill -f bin/blueman-applet; pkill -f bin/blueman-tray

**4. Start it now:**

    setsid -f sh -c 'exec "$HOME/.local/bin/bluemenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

### Updating

    make bluemenu && install -m755 bin/bluemenu ~/.local/bin/bluemenu
    pkill -x bluemenu
    setsid -f sh -c 'exec "$HOME/.local/bin/bluemenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

### Uninstalling

    pkill -x bluemenu
    rm ~/.config/autostart/bluemenu.desktop ~/.config/autostart/blueman.desktop
    rm ~/.local/share/dbus-1/services/org.blueman.Applet.service
    systemctl --user unmask blueman-applet.service
    setsid -f blueman-applet >/dev/null 2>&1

## Flags

    -scale 2   display scale factor (0, the default, detects it)
    -v         log tray registrations, clicks, agent requests and icon changes
