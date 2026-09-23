# launcher

A keyboard launcher: type a few letters, get the application, and
if it is already running get the window you already have instead of a
second copy of it. Plus a calculator, because half of what a launcher gets
typed into it is arithmetic.

Pure Go, no cgo, one static binary, no runtime dependencies.

## Why

ulauncher does the search half well but always starts a new instance, and
it costs ~190 MB of resident memory to do it because it carries a Python
interpreter and PyGObject. This is the same two features in ~15 MB, and it
focuses windows you already have.

## Build

    make launcher          # from the repository root
    # or
    CGO_ENABLED=0 go build -o launcher ./cmd/launcher

## Use

Run `osxflow` with no arguments to show the launcher. Bind it to a key:

*Settings → Keyboard → Application Shortcuts → Add*, command
`launcher`.

Or from the command line:

    xfconf-query -c xfce4-keyboard-shortcuts \
      -p '/commands/custom/<Alt>F1' -n -t string -s ~/.local/bin/launcher

On this machine [gokeyd](https://github.com/mpdroog/gokeyd) already rewrites Cmd+Space to Alt+F1, so binding
Alt+F1 is what makes Cmd+Space open the launcher.

### Keys

| Key | Does |
| --- | --- |
| type | filter applications, or enter a sum |
| `Enter` | focus the application if running, else start it |
| `Up` / `Down`, `Tab` | move the selection |
| `PageUp` / `PageDown`, `Home` / `End` | move further |
| `Backspace`, `Ctrl+W`, `Ctrl+U` | delete a character, a word, everything |
| `Escape`, `Ctrl+C` | dismiss |
| click outside | dismiss |
| click a row | select it (does not launch) |
| scroll wheel | move the selection |

Choosing an application is also how the launcher learns; see Ranking.

### Inspecting what it would do

Every decision the launcher makes is visible without the window:

    launcher -list      # applications found, and the binary each resolves to
    launcher -windows   # open windows, and the executable behind each one
    launcher -match     # which application each open window matched
    launcher -where     # which monitor it would open on, and what gets the keyboard back
    launcher -search fi # how a query ranks
    launcher -eval '2^10'
    launcher -open Firefox

## How it finds an already-running window

In order of confidence, best first:

1. **Executable.** The window's `_NET_WM_PID`, resolved through
   `/proc/<pid>/exe`, against the binary the `.desktop` entry runs. This
   cannot collide, so it wins outright.
2. **StartupWMClass.** What the entry declares. Only 11 of 146 entries on
   the development machine declare one, so this is a tiebreaker rather
   than the main rule.
3. **Names in the command.** The binary's basename and the non-flag
   arguments, against both halves of `WM_CLASS`. The arguments matter
   because an `Exec` line is often a wrapper: `jumpapp Navigator firefox`,
   `flatpak run org.foo.Bar`, `env GDK_BACKEND=x11 signal-desktop`.

Focus is requested with `_NET_ACTIVE_WINDOW` and source indication 2
("pager"), which is what stops a window manager applying focus-stealing
prevention and merely flashing the taskbar.

## Which screen it opens on

On the monitor holding the window you were just typing in, centred there
and a quarter of the way down.

Not the monitor under the pointer, which is what it used to use and is a
bad guess for something a hotkey summons: the mouse sits wherever it was
last used, which on two monitors is regularly not the one being typed on.
Under a Wayland compositor it is worse than a guess, because X stops
updating the pointer position the moment the cursor leaves an X11 surface,
so the launcher opened on whichever screen the mouse last crossed one of
its own windows on -- and stayed there however much the mouse moved.

Which window has the keyboard is a question only the display server can
answer, so it is asked there: `_NET_ACTIVE_WINDOW` under X11, and under
Wayland the compositor's own window list, whose outputs carry the same
connector name (`DP-8`) that XWayland's RandR reports -- which is what
makes the answer usable for a window placed through X. When nothing can
say, the pointer is still the fallback.

`launcher -where` prints both answers without opening anything.

## The keyboard, afterwards

Dismissing the launcher puts the keyboard back where it was.

It has to be done explicitly, and this is the reason: the window is
override-redirect, so no window manager placed it and none of them has any
record of what was focused before it appeared. Under labwc nothing then
puts the focus back when it vanishes -- the launcher closes and the
keyboard belongs to nothing at all, so the next thing typed goes nowhere
and the window you were working in has to be clicked.

So the focused window is noted before the launcher opens -- before, because
opening it is what destroys the answer -- and activated again on the way
out. Only on the way out of a launcher that launched nothing: when
something was launched, it has the keyboard, and that is the point.

## Ranking

Matches are grouped into tiers -- exact name, prefix, word prefix,
executable name, substring, subsequence -- and a better tier always wins.
Within a tier, the order is by **frecency**: how often you have chosen
something, decayed with a 28-day half-life, so one number covers both "used
a lot" and "used lately".

That is what fixes the obvious complaint. `fi` matches `fish`,
`Fingerprints` and `Firefox` all as prefixes, so they were ordered by name
length; after you pick Firefox once it goes first, and stays there for
untaught queries like `f` and `fire` too.

Frecency deliberately **cannot cross a tier**. An application you open
hourly must not displace an exact-name match for something else, or typing
a full name would stop working: `fish` still returns fish first however
much Firefox is used.

The one exception is a **pin**. Choosing an application for an exact query
records that pairing, and a pin outranks everything -- shown as `*` in
`-search`. This exists for queries scoring cannot reach: `ff` matches
LibreOffice as a substring but Firefox only as a scattered subsequence, so
no amount of use would lift it. Pins are per exact query, so teaching `ff`
does not rearrange `f`.

Usage lives in `$XDG_STATE_HOME/osxflow/frecency.json` (default
`~/.local/state/osxflow/`), written atomically after a successful launch.
Losing the file costs the ranking, nothing else.

    launcher -no-learn              # do not record this launch
    launcher -forget firefox.desktop # drop an application from the memory

## Deliberately not done
- **No clipboard.** The calculator shows its answer; `Enter` on it does
  nothing. Copying would mean owning the X selection, which means staying
  alive after the window closes.
- **No icons.** Names and descriptions only.
- **Clicking a row does not launch it.** The pointer is grabbed while the
  window is open, and a stray click starting an application is a worse
  failure than one that does nothing. Clicking only moves the selection.
- **No daemon.** Startup is ~10 ms cold, so there is nothing to amortise.

## Layout

Library code lives in the repository's shared `internal/`, not under this
directory, so the next tool can use it:

    internal/xwin     listing windows and focusing one: X11, and Wayland
    internal/xmon     which monitor is which, and which one to open on
    internal/desktop  .desktop parsing and XDG directory scanning
    internal/calc     expression parser and evaluator
    internal/search   ranking applications against a query
    internal/launch   matching applications to windows, and starting them
    internal/ui       the window, drawing, and the event loop
    internal/frecency usage memory: decayed scoring and its state file

The first two are general desktop plumbing and are the ones a second tool
is most likely to want.
