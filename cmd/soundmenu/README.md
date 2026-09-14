# soundmenu

A sound menu in the panel's status tray, the way macOS does it: a speaker
icon that opens a short menu, and the volume overlay when a volume key is
pressed.

- **Sound.** The volume, as a slider, and a mute switch.
- **Output.** When there is more than one -- the built-in speakers and a
  USB headset, say -- a list to pick the default from.
- **Input.** The microphone's level and a mute switch, and a list when
  there is more than one.
- **Now Playing.** The track in the player that is playing (or the first
  one that is paused), with previous, play/pause and next. Buttons the
  player says it cannot use are dimmed.
- **Sound Settings…** opens pavucontrol.

The icon is a speaker with one to three waves for the volume, or a cross
when muted. Scrolling over it turns the volume up and down.

## The volume keys

The panel's PulseAudio plugin did more than show an icon: it held the
volume keys. soundmenu takes them over -- volume up and down, mute and
microphone mute -- and shows macOS's overlay: a translucent square with a
speaker and sixteen level segments, gone a moment and a half after the last
press. A press is 5%, as it was with the plugin; turning the volume up
unmutes.

X lets only one program hold a key. While the plugin is still on the panel
it has them, and soundmenu logs that the keys are taken and carries on
without them. Take the plugin off the panel (below) and restart soundmenu.

## How it works

Sound goes through PipeWire's PulseAudio server, with a pure-Go client
(`internal/audio`, on `jfreymuth/pulse`), and media players through MPRIS
on the session bus (`internal/mpris`). If the sound server restarts,
soundmenu shows that there is none and connects again every two seconds.

The PulseAudio library prints messages it does not recognise to standard
output, so an odd line from it may turn up in `~/.xsession-errors`.

## Installing

**1. Build and install the binary:**

    make soundmenu
    install -m755 bin/soundmenu ~/.local/bin/soundmenu

**2. Start it at login:**

    printf '[Desktop Entry]\nType=Application\nName=Sound Menu\nExec=%s/.local/bin/soundmenu\nX-GNOME-Autostart-enabled=true\n' "$HOME" \
      > ~/.config/autostart/soundmenu.desktop

**3. Take the PulseAudio plugin off the panel,** which also frees the
volume keys. Find its id and the panel's list of plugin ids:

    xfconf-query -c xfce4-panel -lv | grep -E 'plugin-[0-9]+ +pulseaudio'   # /plugins/plugin-12 here
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids                # 1 7 8 10 11 12 13 here

Keep a copy of the list, set it again without that id, and restart the
panel. The plugin's settings stay under `/plugins/plugin-12`, so putting the
id back restores it:

    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids > ~/.config/xfce4/panel-plugin-ids.bak
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 1 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 11 -t int -s 13
    xfce4-panel -r

(If powermenu has already taken plugin 11 off, leave it out here too.)

**4. Start it now,** after the panel has restarted, so the keys are free:

    setsid -f sh -c 'exec "$HOME/.local/bin/soundmenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

Check that nothing says a key is taken:

    grep 'soundmenu:' ~/.xsession-errors | tail

### Updating

    make soundmenu && install -m755 bin/soundmenu ~/.local/bin/soundmenu
    pkill -x soundmenu
    setsid -f sh -c 'exec "$HOME/.local/bin/soundmenu" >>"$HOME/.xsession-errors" 2>&1 </dev/null'

### Uninstalling

Stop soundmenu first, so the plugin can take the keys back, then put the
plugin's id back where it was and restart the panel:

    pkill -x soundmenu
    rm ~/.config/autostart/soundmenu.desktop
    xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --force-array \
      -t int -s 1 -t int -s 7 -t int -s 8 -t int -s 10 -t int -s 11 -t int -s 12 -t int -s 13
    xfce4-panel -r

## Flags

    -scale 2   display scale factor (0, the default, detects it)
    -v         log tray registrations, clicks, scrolls and icon changes
