# ask

One dialog, for the tools that need to ask something and have nowhere to
draw it.

    printf 'xsnews\nhitnews\nportal\n' |
        ask -mode=list -editable -title='Save this hour' -text='Which project?'

    ask -mode=entry -allow-empty -text='What did you work on?'

    some-long-message | ask -mode=message -title='Entry not saved'

The answer goes to stdout. **The exit status is the point:**

| status | meaning |
|--------|---------|
| 0 | accepted; the answer is on stdout |
| 1 | dismissed -- Escape, the close button, or an empty entry |
| 2 | no dialog could be shown at all |

1 and 2 must never be conflated, and that is why this exists. `ezhours`
shelled out to zenity, which is not installed here; zenity's "the user
cancelled" and "there is no zenity" were both a failed exec to the caller,
so every Stop Timer looked like a deliberate discard and the hour was
thrown away without a word on screen. `~/hours` had no commits at all.

## Why not zenity

Besides the above: zenity is GTK, and this desktop is a handful of static
Go binaries. Everything a save dialog needs was already here -- a window
(`internal/menu`), a font (`internal/text`), drawing (`internal/paint`) and
monitor geometry (`internal/xmon`).

## Modes

- `-mode=entry` asks for one line. `-default` prefills it. A blank answer
  is a dismissal unless `-allow-empty`.
- `-mode=list` offers the lines read from stdin. Typing filters them. With
  `-editable` a name that is not on the list gets an **Add "..."** row,
  placed *below* the matches and not preselected while anything matches --
  typing is nearly always narrowing towards something that exists, and an
  add row on top meant typing `xsn` highlighted `Add "xsn"` instead of
  `xsnews`.
- `-mode=message` shows the text and waits.

Keys: typing filters or fills, arrows choose, Enter accepts, Escape
cancels, Ctrl+U clears.

## The window is managed

Unlike the launcher, this is not an override-redirect overlay. A launcher
takes the keyboard by grabbing it, which under labwc is a thicket: an X
grab never sees clicks on Wayland-native surfaces, and one stranded grab
breaks unrelated tools. A dialog is a real window -- the compositor frames
it, places it, gives it the keyboard and takes it back. It declares
`_NET_WM_WINDOW_TYPE_DIALOG`, which is deliberate: wlroots refuses the
keyboard to a surface declaring one of the menu or notification types.
