# notifyd

Desktop notifications the way macOS draws them: translucent banners that
slide in at the top right of the screen, stack newest-first, and fade away.

It replaces xfce4-notifyd. It is the server end of the freedesktop.org
[Desktop Notifications Specification][spec] (1.2) on the session bus, so
everything that notifies — through libnotify, GLib, `notify-send`, or D-Bus
directly — works with it unchanged.

    xfce4-notifyd   ~35 MB   GTK3, 79 shared libraries
    notifyd         ~12-19 MB   one static binary, no cgo, no shared libraries

Most of the difference is what xfce4-notifyd carries: GTK3, Cairo, Pango,
HarfBuzz, fontconfig, GLib and libxfce4ui, plus SQLite for its notification
log. `notifyd` draws its banners with the few routines in `internal/paint`
and `internal/text`, and keeps no log. About half of its resident memory
is its own working set (the Go runtime, two fonts, the list of installed
applications); the rest is pages of its own binary.

Both figures are resident memory as `ps` reports it, which counts shared
libraries in full even though other GTK programs on the desktop use the
same copies. Stopping xfce4-notifyd therefore frees less than the whole
35 MB.

[spec]: https://specifications.freedesktop.org/notification-spec/latest/

## What it does

- **Banners.** Up to five at once, below the panel at the top right; more
  wait their turn and appear as room is made. Each shows the app's icon, a
  bold title, and up to four lines of body, cut with an ellipsis beyond that.
- **Timeouts** follow the spec: the sender's own, or the server default
  (xfce4-notifyd's *expire-timeout* setting, 5 s unless changed), and a
  critical notification with no timeout of its own stays until dismissed.
  A timeout only runs while its banner is on screen, and **every timeout
  pauses while the pointer is over a banner**, so nothing disappears while
  it is being read — or moves the stack out from under the pointer.
- **Clicking.** A click on a banner invokes its default action (Thunderbird
  opens the message, Firefox the tab) or, when it has none, dismisses it.
  Other actions are buttons along the bottom. The close button appears
  under the pointer, at the top-left corner; right-click anywhere also
  dismisses.
- **Volume and brightness popups** replace each other in place instead of
  stacking one per keypress (the `x-canonical-private-synchronous` hint), and
  their level is drawn as a bar.
- **Do not disturb** is the xfce4-notifyd setting, read live over xfconf, so
  the panel's toggle works unchanged. Only critical notifications get
  through while it is on.
- **Critical** banners carry a red hairline along the top edge.

## Icons

In order: the sender's inline image (`image-data`), an image file it names
(PNG, JPEG or GIF), the application's icon from the set compiled into the
binary (see `internal/icons` — the same one the dock uses), and finally a
tinted tile with the app's initial. The application is recognised by its
`desktop-entry` hint, its icon name, or its display name, whichever it sent.

SVG files are not drawn: the pure-Go rasterisers get this desktop's icon
theme visibly wrong (see the dock's README), so an SVG falls through to the
embedded icon.

## What it does not do

No notification log or history, no sounds, and no per-application settings.
Styling in the body (`<b>`, `<i>`) is removed rather than drawn.

## Installing

Only one notification daemon can run at a time, so installing `notifyd`
means building it and then taking over from xfce4-notifyd. Nothing is
uninstalled, and every step can be undone (see *Uninstalling* below).

`notifyd` has no autostart entry of its own and needs none. The session
bus starts it the first time an application sends a notification, at
login or later, and starts it again if it ever exits.

**1. Build and install the binary** into `~/.local/bin`:

    make notifyd
    install -m755 bin/notifyd ~/.local/bin/notifyd

(`make install` does the same for every tool in the repository.)

**2. Register it with the session bus.** A service file in the user
directory outranks the system one, so from now on the bus starts `notifyd`
instead of xfce4-notifyd:

    mkdir -p ~/.local/share/dbus-1/services
    printf '[D-BUS Service]\nName=org.freedesktop.Notifications\nExec=%s/.local/bin/notifyd\n' "$HOME" \
      > ~/.local/share/dbus-1/services/org.freedesktop.Notifications.service

**3. Stop XFCE starting xfce4-notifyd at login.** It has two ways in: an
autostart entry, which is hidden with a user copy, and a systemd unit. The
unit matters because the panel's notification plugin triggers it at login,
and xfce4-notifyd would then claim the notification name before `notifyd`
got the chance. Masking it stops that:

    cp /etc/xdg/autostart/xfce4-notifyd.desktop ~/.config/autostart/
    printf 'Hidden=true\n' >> ~/.config/autostart/xfce4-notifyd.desktop
    systemctl --user mask xfce4-notifyd.service

**4. Switch over now,** without logging out: stop xfce4-notifyd, and have
the bus pick up the service file from step 2. It only notices new service
directories when told to, or at the next login.

    systemctl --user stop xfce4-notifyd.service
    dbus-send --session --dest=org.freedesktop.DBus --type=method_call \
      /org/freedesktop/DBus org.freedesktop.DBus.ReloadConfig

**5. Check it.** Send a notification, then ask the bus who answered:

    notify-send "Hello" "notifyd is running"
    busctl --user status org.freedesktop.Notifications | grep Exe

The second line should print `Exe=/home/<you>/.local/bin/notifyd`. Run the
same check after your next login to confirm the takeover held.

### Updating

Rebuild, copy the binary over, and stop the running copy. The next
notification starts the new one:

    make notifyd && install -m755 bin/notifyd ~/.local/bin/notifyd
    pkill -x notifyd

### Uninstalling

    rm ~/.local/share/dbus-1/services/org.freedesktop.Notifications.service
    rm ~/.config/autostart/xfce4-notifyd.desktop
    systemctl --user unmask xfce4-notifyd.service
    pkill -x notifyd

The next notification starts xfce4-notifyd again, as does the next login.

## Trying it

`notify-send` covers every feature:

    # A plain notification
    notify-send "Hello" "This is the body text"

    # With an application name and icon
    notify-send -a Firefox -i firefox "Download complete" "report.pdf (2.4 MB)"

    # Critical: red top edge, and stays until dismissed
    notify-send -u critical "Backup failed" "The backup disk is not mounted"

    # A timeout of its own, in milliseconds
    notify-send -t 10000 "Ten seconds" "Then it fades away"

    # Action buttons: notify-send waits, and prints the one clicked
    notify-send -A open=Open -A later=Later "Meeting in 5 minutes" "Standup, room 2"

    # A volume-style popup: the second replaces the first in place
    notify-send -h int:value:65 -h string:x-canonical-private-synchronous:volume "Volume"
    notify-send -h int:value:80 -h string:x-canonical-private-synchronous:volume "Volume"

Hover over a banner to keep it on screen. Click it to run its default
action, or right-click to dismiss it.

## Debugging

`notifyd -v` logs every notification, action and closure, and why an image
could not be loaded. When started by the bus its stderr goes to the journal:
`journalctl --user -t dbus-daemon`. `-replace` takes the bus name from a
running daemon that allows it.

## Notes on the implementation

Everything that is not drawing — the D-Bus server, parsing what senders
send, and the queue with its timeouts, replacement and pausing — lives in
`internal/notify`, where it is tested against a private `dbus-daemon`
without a display. The parsers that read another process's input (body
markup and raw `image-data` pixels) have fuzz targets.

godbus answers every incoming call on a goroutine of its own. Rather than
lock the daemon's state, each call is handed to the main loop over a
channel and answered from there, so the queue and the X connection are only
ever touched from one goroutine.

Each banner is its own override-redirect window with a 32-bit visual,
like the dock. They slide and restack by moving windows, not by redrawing
them; a closing banner fades by scaling a snapshot of its pixels, which is a
multiply per byte rather than a repaint per frame.
