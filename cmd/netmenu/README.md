# netmenu

A network menu in the panel's status tray, the way macOS does it: a Wi-Fi
icon that opens a short menu of networks and VPNs.

It replaces nm-applet's tray icon and its GTK menu, and it does less on
purpose:

- **Wi-Fi switch.** On and off. A radio turned off by a hardware switch is
  shown as such, and the switch does nothing.
- **Networks.** Known networks (ones with a saved profile) first, then
  others, strongest first, at most six and eight. One click joins a known
  network, or an open one. A network that needs a password you have never
  entered opens nm-connection-editor, which asks for it and saves it; after
  that it is a known network.
- **VPNs.** Every saved VPN profile, WireGuard included, with a switch.
- **Network Settings…** opens nm-connection-editor for everything else.

The icon is the Wi-Fi symbol with unused arcs dimmed, a lit dot while
joining, the wired symbol when only Ethernet is up, and a padlock in the
corner while a VPN is connected. The tooltip says the same in words.

## What it is not

**It is not a secret agent.** nm-applet also answers NetworkManager when a
connection needs a secret it does not have, by prompting or by reading your
keyring. netmenu does not. A profile works with netmenu when NetworkManager
stores all of its secrets itself -- `*-flags` of `0` in `nmcli connection
show <name>` -- and fails otherwise, with NetworkManager's reason in the
log. Wi-Fi profiles saved by nm-connection-editor or nmcli are stored that
way by default; VPN profiles often are not. See *Installing*, step 2.

There are no connect and disconnect notifications either.

## Wayland

The two halves that talk to the system are D-Bus and would run unchanged:
`internal/netmgr` (NetworkManager) and `internal/sni` (the tray icon, which
waybar, KDE and GNOME's AppIndicator extension all host). The menu window
itself is X11 -- override-redirect with a pointer grab -- and a Wayland
build would need its own, on layer-shell.

## Installing

**1. Build and install the binary:**

    make netmenu
    install -m755 bin/netmenu ~/.local/bin/netmenu

**2. Store each VPN's secrets with NetworkManager.** Check the flags:

    nmcli -g vpn.data connection show <vpn> | tr ',' '\n' | grep flags

Any flag of `1` means the secret is kept by an agent -- nm-applet, reading
your keyring -- and has to move. For an L2TP/IPsec VPN whose pre-shared key
is in the GNOME keyring, with `secret-tool` (package `libsecret-tools`)
installed:

    uuid=$(nmcli -g connection.uuid connection show <vpn>)
    psk=$(secret-tool lookup connection-uuid "$uuid" setting-name vpn setting-key ipsec-psk)
    nmcli connection modify <vpn> +vpn.data ipsec-psk-flags=0 vpn.secrets "ipsec-psk=$psk"
    unset psk

The key then lives in `/etc/NetworkManager/system-connections/`, readable
by root only, instead of in your keyring. For the few milliseconds nmcli
runs it is also on nmcli's command line, visible to your own user and root.

Without `secret-tool`, the same move can be done over D-Bus, which is how
it was done on this machine: open a `plain` Secret Service session, find
the item with `SearchItems` on those three attributes (not by path -- item
paths change as the keyring rewrites itself), `GetSecret` it on the same
connection, then merge it into the profile's `GetSecrets("vpn")` result and
`Update` the profile with `ipsec-psk-flags` set to `0`. Leave the
deprecated `ipv4`/`ipv6` `addresses` and `routes` keys out of the update:
godbus cannot re-encode an empty one faithfully and NetworkManager rejects
the whole update, and it rebuilds them from `address-data` and
`route-data` anyway.

Then check it: with nm-applet stopped, take the VPN down and up again. It
should connect without anything asking for a secret.

**3. Start it at login, and stop nm-applet starting.** Both are autostart
entries; nm-applet's is hidden with a user copy, so the package stays
installed:

    printf '[Desktop Entry]\nType=Application\nName=Network Menu\nExec=%s/.local/bin/netmenu\nX-GNOME-Autostart-enabled=true\n' "$HOME" \
      > ~/.config/autostart/netmenu.desktop
    cp /etc/xdg/autostart/nm-applet.desktop ~/.config/autostart/nm-applet.desktop.bak
    cp /etc/xdg/autostart/nm-applet.desktop ~/.config/autostart/
    printf 'Hidden=true\nX-GNOME-Autostart-enabled=false\n' >> ~/.config/autostart/nm-applet.desktop

**4. Switch over now:**

    pkill -x nm-applet
    setsid ~/.local/bin/netmenu 2>>~/.xsession-errors &

### Updating

    make netmenu && install -m755 bin/netmenu ~/.local/bin/netmenu
    pkill -x netmenu; setsid ~/.local/bin/netmenu 2>>~/.xsession-errors &

### Uninstalling

    rm ~/.config/autostart/netmenu.desktop ~/.config/autostart/nm-applet.desktop
    rm ~/.config/autostart/nm-applet.desktop.bak
    pkill -x netmenu; setsid nm-applet >/dev/null 2>&1 &

A VPN secret moved in step 2 stays with NetworkManager, which nm-applet
handles too.

## Flags

    -scale 2   display scale factor (0, the default, detects it)
    -v         log tray registrations and icon changes
