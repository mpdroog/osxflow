# osxflow-doctor

Checks that the desktop is still set up the way osxflow left it, and says
what to do about anything that is not. A command-line tool: it prints text,
opens no window, and works over SSH.

    osxflow-doctor        problems only, and a summary
    osxflow-doctor -v     every check

It exits 1 when a check failed and 0 otherwise. Warnings are worth reading
but don't change the exit status.

## Why

osxflow replaced parts of XFCE and Mint by overriding them from your own
configuration: a masked systemd unit, a hidden autostart entry, a D-Bus
service file, a trimmed XFCE session. An upgrade that renames the thing
being overridden gets around the override without a word, and the first
sign is two trays or XFCE's notifications coming back. Run the doctor after
a big upgrade, or whenever the desktop looks wrong.

## What it checks

- **Session**: X11, not Wayland (every tool draws with X11).
- **Installed**: every osxflow tool is in `~/.local/bin` and executable.
- **Processes**: what osxflow replaced (xfce4-panel, xfce4-notifyd,
  blueman-applet, nm-applet, plank, ulauncher, xfdesktop) is not running.
  The tools that should run all session are running, and so are
  xfce4-power-manager and gokeyd.
- **D-Bus**: menubar owns the tray and notifyd owns notifications. The
  service overrides in `~/.local/share/dbus-1/services` are intact, and the
  system still ships a service of each overridden name.
- **systemd**: the masked user units are still masked, and still exist
  under that name.
- **Autostart**: the hidden entries are still hidden and still exist. Our
  tools start at login. Anything new in `/etc/xdg/autostart` is flagged.
- **XFCE session**: the Failsafe session does not start xfce4-panel or
  xfdesktop, and XFCE is not saving sessions (a saved session would bring
  back whatever was running).
- **Shortcuts**: Alt+F1 (Cmd+Space through gokeyd) runs the launcher. No
  shortcut runs something missing or retired.
- **Commands**: every program the tools run by name is installed:
  macmenu's logout and lock, Firefox for the calendar,
  nm-connection-editor, and the rest.
- **Display**: the 2x scale can be read, and the Ubuntu fonts are found.
- **unpack**: every archive type still opens with unpack.

What "as osxflow left it" means is written down in
[setup.go](setup.go). When the setup changes on purpose, change it there.
A new autostart entry you want to keep, for example, goes into
`knownAutostart`.

## Installing

    make osxflow-doctor
    install -m755 bin/osxflow-doctor ~/.local/bin/osxflow-doctor
