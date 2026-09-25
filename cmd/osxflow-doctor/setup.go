package main

// What this desktop is supposed to look like: everything osxflow replaced,
// how each replacement was made to stick, and what osxflow's tools depend
// on. The checks read these tables; when the setup changes, this is the
// file to change with it.
//
// Every entry says what it guards against, because a check that fails in
// three years is only useful if it says why it mattered.

// ourTools are installed in ~/.local/bin, and the autostarted ones are
// expected to be running.
var ourTools = []struct {
	name      string
	autostart bool // started at login from ~/.config/autostart/<name>.desktop
	running   bool // expected to be running all session
}{
	{"menubar", true, true},
	{"dock", true, true},
	{"netmenu", true, true},
	{"powermenu", true, true},
	{"soundmenu", true, true},
	{"bluemenu", true, true},
	{"wallpaper", true, false}, // sets the background and exits
	{"notifyd", false, false},  // started by D-Bus on the first notification
	{"launcher", false, false}, // Cmd+Space, through gokeyd and the Alt+F1 shortcut
	{"macmenu", false, false},  // run by menubar when Tux is clicked
	{"unpack", false, false},   // run by double-clicking an archive
}

// retired programs must not be running: each is what an osxflow tool
// replaced, and running both means two of something -- two trays, two
// notification daemons, two Bluetooth agents.
var retired = []retiredProgram{
	{"xfce4-panel", "menubar", "xfce4-panel --quit, then see the XFCE session check"},
	{"xfce4-notifyd", "notifyd", "pkill -x xfce4-notifyd, then see the mask and D-Bus override checks"},
	{"blueman-applet", "bluemenu", "pkill -x blueman-applet, then see the mask and D-Bus override checks"},
	{"nm-applet", "netmenu", "pkill -x nm-applet, then see the autostart checks"},
	{"plank", "dock", "pkill -x plank, then see the autostart checks"},
	{"ulauncher", "launcher", "pkill -x ulauncher, then see the autostart checks"},
	{"xfdesktop", "wallpaper", "xfdesktop --quit, then see the XFCE session check"},
}

// retiredProgram is a program an osxflow tool replaced, by the name the
// kernel knows its process by, and how to get rid of it again.
type retiredProgram struct {
	comm, replacedBy, fix string
}

// companions are other programs this setup relies on being up.
var companions = []struct {
	comm, why, fix string
}{
	{"xfce4-power-man", "handles the lid, idle dimming, the brightness keys and critical battery (powermenu does not)",
		`setsid -f sh -c 'exec xfce4-power-manager >>"$HOME/.xsession-errors" 2>&1 </dev/null'`},
	{"gokeyd", "turns Cmd+Space into Alt+F1 for the launcher, and the other Mac key mappings",
		`setsid -f sh -c 'exec ~/.local/bin/gokeyd -mode=run -timeout=0 >>"$HOME/.xsession-errors" 2>&1 </dev/null'`},
}

// busNames are D-Bus names osxflow's tools must own, when they are owned
// at all.
var busNames = []struct {
	name, owner string
	onDemand    bool // may be unowned until first used
}{
	{"org.kde.StatusNotifierWatcher", "menubar", false},
	{"org.freedesktop.Notifications", "notifyd", true},
}

// dbusOverrides are session service files in ~/.local/share/dbus-1/services
// that win over the system's, so that D-Bus activation starts ours, or
// nothing, instead of what the system ships.
var dbusOverrides = []struct {
	name, exec, why string
}{
	{"org.freedesktop.Notifications", "~/.local/bin/notifyd", "notifications start notifyd, not xfce4-notifyd"},
	{"org.blueman.Applet", "/bin/false", "blueman-manager cannot start blueman's applet, a second pairing agent"},
	{"org.gnome.OnlineAccounts", "/bin/false", "GNOME Online Accounts stays off"},
	{"org.gnome.Identity", "/bin/false", "GNOME's identity service stays off"},
}

// masks are user systemd units linked to /dev/null.
var masks = []struct {
	unit, why string
}{
	{"xfce4-notifyd.service", "the panel's notification plugin used to start it at login, racing notifyd"},
	{"blueman-applet.service", "blueman-manager would start it, a second pairing agent beside bluemenu"},
	{"obex.service", "Bluetooth file transfer, not used"},
	{"gvfs-afc-volume-monitor.service", "iPhone mounting, not used"},
	{"gvfs-goa-volume-monitor.service", "GNOME Online Accounts mounting, not used"},
	{"speech-dispatcher.service", "text to speech, not used"},
	{"speech-dispatcher.socket", "text to speech, not used"},
}

// hiddenAutostart are ~/.config/autostart copies with Hidden=true, which
// stop the entry of the same name from starting. system says whether the
// entry being hidden is in /etc/xdg/autostart, where a package upgrade
// could rename it out from under the copy; the others are the programs'
// own, written into ~/.config/autostart when they were set up.
var hiddenAutostart = []struct {
	file   string
	system bool
}{
	{"blueman.desktop", true},
	{"nm-applet.desktop", true},
	{"xfce4-notifyd.desktop", true},
	{"mintupdate.desktop", true},
	{"org.gnome.Evolution-alarm-notify.desktop", true},
	{"plank.desktop", false},
}

// knownAutostart is what /etc/xdg/autostart held when this setup was last
// checked (2026-09-24). Anything else there is new -- installed by an
// upgrade or a new package -- and starts at every login until looked at.
var knownAutostart = []string{
	"at-spi-dbus-bus.desktop", "blueman.desktop", "geoclue-demo-agent.desktop",
	"gnome-keyring-pkcs11.desktop", "gnome-keyring-secrets.desktop", "gnome-keyring-ssh.desktop",
	"im-launch.desktop", "input-remapper-autoload.desktop", "light-locker.desktop",
	"mintreport.desktop", "mintupdate.desktop", "mintwelcome.desktop", "nm-applet.desktop",
	"nvidia-prime.desktop", "onboard-autostart.desktop", "orca-autostart.desktop",
	"org.gnome.Evolution-alarm-notify.desktop", "org.gnome.SettingsDaemon.DiskUtilityNotify.desktop",
	"polkit-gnome-authentication-agent-1.desktop", "print-applet.desktop",
	"snap-userd-autostart.desktop", "sticky.desktop", "user-dirs-update-gtk.desktop",
	"warpinator-autostart.desktop", "xapp-sn-watcher.desktop", "xbindkeys.desktop",
	"xdg-user-dirs.desktop", "xfce4-notifyd.desktop", "xfce4-power-manager.desktop",
	"xfsettingsd.desktop", "xiccd.desktop", "xscreensaver.desktop",
}

// sessionRetired must not be among the XFCE Failsafe session's clients:
// the session starts them at every login, autostart or not.
var sessionRetired = []string{"xfce4-panel", "xfdesktop"}

// commands are programs osxflow's tools run by name. One missing makes a
// menu row or a click fail, with the reason only in ~/.xsession-errors.
// Alternatives are separated by "|": any one will do.
var commands = []struct {
	names, usedFor string
	required       bool
}{
	{"xfce4-session-logout", "macmenu: Sleep, Restart, Shut Down, Log Out", true},
	{"xflock4", "macmenu: Lock Screen", true},
	{"xfce4-settings-manager", "macmenu: System Settings…", true},
	{"xfce4-taskmanager", "macmenu: Task Manager…", false},
	{"firefox", "menubar: clicking the date opens the calendar", true},
	{"nm-connection-editor", "netmenu: Network Settings…, joining new networks", true},
	{"pavucontrol", "soundmenu: Sound Settings…", false},
	{"xfce4-power-manager-settings", "powermenu: Power Settings…", false},
	{"blueman-manager", "bluemenu: Bluetooth Settings…", false},
	{"pkexec", "bluemenu: Reload Bluetooth Driver…", false},
	{"xdg-open", "dock: opening files from the Downloads and Trash stacks", true},
	{"7z|7zz|7za", "unpack: .7z and .rar archives", false},
}

// launcherShortcut is the XFCE shortcut gokeyd's Cmd+Space arrives as.
const launcherShortcut = "<Alt>F1"

// unpackDesktop is the entry archives open with.
const unpackDesktop = "unpack.desktop"
