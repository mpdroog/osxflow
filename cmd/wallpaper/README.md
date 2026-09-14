# wallpaper

Sets the desktop background and exits.

    wallpaper [-style zoom|fit|stretch|center] picture.jpg

It replaces xfdesktop when all that is wanted from it is the picture: no
desktop icons, and nothing left running afterwards.

- **zoom** (the default, xfdesktop's "Zoomed") fills the screen and crops
  what overflows, evenly on both sides.
- **fit** ("Scaled") shows the whole picture, with black bars where it does
  not reach.
- **stretch** fills the screen, ignoring the picture's shape.
- **center** shows it at actual size in the middle.

JPEG and PNG are read, and scaling uses Catmull-Rom: the picture is drawn
once and looked at all day.

## How it works

The picture is drawn into a pixmap that becomes the root window's
background, and X is told to keep it after the program exits. The pixmap
is named in `_XROOTPMAP_ID` -- which xfwm4's compositor reads to draw the
background -- and `ESETROOT_PMAP_ID`, as feh and Esetroot do. The next run
frees the previous picture, so running it again does not pile screen-sized
pixmaps up in the X server.

## Installing

**1. Build and install the binary:**

    make wallpaper
    install -m755 bin/wallpaper ~/.local/bin/wallpaper

**2. Set the picture at login:**

    printf '[Desktop Entry]\nType=Application\nName=Wallpaper\nExec=%s/.local/bin/wallpaper -style zoom /usr/share/xfce4/backdrops/linuxmint.jpg\nX-GNOME-Autostart-enabled=true\n' "$HOME" \
      > ~/.config/autostart/wallpaper.desktop

**3. Stop XFCE starting xfdesktop.** It is not an autostart entry: it is the
last of the five clients in XFCE's failsafe session (xfwm4, xfsettingsd,
xfce4-panel, Thunar, xfdesktop), which is what starts when there is no saved
session. Check that it is the last, then shorten the list by one:

    xfconf-query -c xfce4-session -p /sessions/Failsafe/Client4_Command   # xfdesktop
    xfconf-query -c xfce4-session -p /sessions/Failsafe/Count > ~/.config/xfce4/xfce4-session-failsafe-count.bak
    xfconf-query -c xfce4-session -p /sessions/Failsafe/Count -s 4

**4. Switch over now,** quitting xfdesktop first -- its desktop window
would cover the new background:

    xfdesktop --quit
    ~/.local/bin/wallpaper -style zoom /usr/share/xfce4/backdrops/linuxmint.jpg

### Changing the picture

Run it again with another picture, and change the path in
`~/.config/autostart/wallpaper.desktop` to keep it. XFCE's Desktop settings
no longer apply: they configure xfdesktop.

### Uninstalling

    xfconf-query -c xfce4-session -p /sessions/Failsafe/Count -s 5
    rm ~/.config/autostart/wallpaper.desktop
    setsid -f xfdesktop >/dev/null 2>&1
