package main

// The checks. Each reads the setup tables and the system, and records one
// result per thing it looked at.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/scale"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xfconf"
)

// status is how a check came out, in order of badness.
type status int

const (
	statusOK status = iota
	statusSkip
	statusWarn
	statusFail
)

func (s status) String() string {
	switch s {
	case statusOK:
		return "ok"
	case statusSkip:
		return "skip"
	case statusWarn:
		return "WARN"
	case statusFail:
		return "FAIL"
	}
	return fmt.Sprintf("status(%d)", int(s))
}

// result is one thing checked.
type result struct {
	status status
	area   string
	what   string

	// fix says what to do about a warning or failure, or why a check was
	// skipped.
	fix string
}

// errNoBus means there is no session bus to ask: the doctor is running
// outside the desktop session, over SSH say.
var errNoBus = errors.New("no session bus")

// system is what the checks ask of the running desktop, apart from files.
type system interface {
	// Processes is the set of running processes' names, as the kernel
	// keeps them: cut to 15 bytes.
	Processes() (map[string]bool, error)

	// BusOwner is the process name of whoever owns a session bus name,
	// with owned false when nobody does.
	BusOwner(name string) (comm string, owned bool, err error)

	// XfconfAll reads an xfconf channel's properties under base.
	XfconfAll(channel, base string) (map[string]any, error)

	LookPath(name string) (string, error)
	Getenv(name string) string
}

// doctor runs the checks. home and root are where the user's files and
// the system's are; tests point both into a temporary directory.
type doctor struct {
	sys        system
	home, root string
	results    []result
}

func (d *doctor) add(s status, area, what, fix string) {
	d.results = append(d.results, result{status: s, area: area, what: what, fix: fix})
}

// sysPath is an absolute system path under the doctor's root.
func (d *doctor) sysPath(p string) string { return filepath.Join(d.root, p) }

// homePath expands a leading ~ to the doctor's home.
func (d *doctor) homePath(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(d.home, rest)
	}
	return p
}

// restart is how to start one of our tools by hand, the way autostart
// would, with its errors where the session's go.
func restart(tool string) string {
	return fmt.Sprintf(`setsid -f sh -c 'exec ~/.local/bin/%s >>"$HOME/.xsession-errors" 2>&1 </dev/null'`, tool)
}

func (d *doctor) run() {
	d.checkSessionType()
	d.checkInstalled()
	d.checkProcesses()
	d.checkBusNames()
	d.checkDBusOverrides()
	d.checkMasks()
	d.checkAutostart()
	d.checkXfceSession()
	d.checkShortcuts()
	d.checkCommands()
	d.checkDisplay()
	d.checkUnpack()
}

func (d *doctor) checkSessionType() {
	const area = "session"
	switch t := d.sys.Getenv("XDG_SESSION_TYPE"); t {
	case "x11":
		d.add(statusOK, area, "the session is X11", "")
	case "wayland":
		d.add(statusFail, area, "the session is Wayland; every osxflow tool draws with X11 and will not show",
			"log in to the X11 (Xfce) session, or port the tools (see the menubar README)")
	default:
		d.add(statusSkip, area, "session type", fmt.Sprintf("XDG_SESSION_TYPE is %q: not run from the desktop session", t))
	}
}

func (d *doctor) checkInstalled() {
	const area = "installed"
	for _, t := range ourTools {
		path := filepath.Join(d.home, ".local", "bin", t.name)
		if err := executable(path); err != nil {
			d.add(statusFail, area, fmt.Sprintf("%s: %v", t.name, err),
				fmt.Sprintf("make %s && install -m755 bin/%s ~/.local/bin/%s", t.name, t.name, t.name))
			continue
		}
		d.add(statusOK, area, t.name+" is installed", "")
	}
}

// executable says why path is not a runnable file, or nil.
func executable(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is missing", path)
	}
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

func (d *doctor) checkProcesses() {
	const area = "processes"
	procs, err := d.sys.Processes()
	if err != nil {
		d.add(statusSkip, area, "running programs", fmt.Sprintf("listing processes: %v", err))
		return
	}
	for _, r := range retired {
		if procs[r.comm] {
			d.add(statusFail, area, fmt.Sprintf("%s is running again; %s replaced it", r.comm, r.replacedBy), r.fix)
		} else {
			d.add(statusOK, area, r.comm+" is not running", "")
		}
	}
	for _, t := range ourTools {
		if !t.running {
			continue
		}
		if procs[t.name] {
			d.add(statusOK, area, t.name+" is running", "")
		} else {
			d.add(statusFail, area, t.name+" is not running",
				"look in ~/.xsession-errors for why it stopped, then: "+restart(t.name))
		}
	}
	for _, c := range companions {
		if procs[c.comm] {
			d.add(statusOK, area, c.comm+" is running", "")
		} else {
			d.add(statusWarn, area, c.comm+" is not running; it "+c.why, c.fix)
		}
	}
}

func (d *doctor) checkBusNames() {
	const area = "d-bus"
	for _, b := range busNames {
		comm, owned, err := d.sys.BusOwner(b.name)
		switch {
		case errors.Is(err, errNoBus):
			d.add(statusSkip, area, b.name, "no session bus: not run from the desktop session")
		case err != nil:
			d.add(statusFail, area, fmt.Sprintf("%s: %v", b.name, err), "")
		case !owned && b.onDemand:
			d.add(statusOK, area, b.name+" is free; "+b.owner+" takes it when first needed", "")
		case !owned:
			d.add(statusFail, area, "nobody owns "+b.name+"; "+b.owner+" should", restart(b.owner))
		case comm != b.owner:
			d.add(statusFail, area, fmt.Sprintf("%s is owned by %s, not %s", b.name, comm, b.owner),
				fmt.Sprintf("stop %s, then %s", comm, restart(b.owner)))
		default:
			d.add(statusOK, area, b.name+" is owned by "+b.owner, "")
		}
	}
}

// systemServiceDirs are where installed packages put D-Bus session
// services.
var systemServiceDirs = []string{"/usr/share/dbus-1/services", "/usr/local/share/dbus-1/services"}

func (d *doctor) checkDBusOverrides() {
	const area = "d-bus"
	dir := filepath.Join(d.home, ".local", "share", "dbus-1", "services")
	for _, o := range dbusOverrides {
		path := filepath.Join(dir, o.name+".service")
		want := d.homePath(o.exec)
		fix := fmt.Sprintf("printf '[D-BUS Service]\\nName=%s\\nExec=%s\\n' > %s", o.name, want, path)
		keys, err := readGroup(path, "D-BUS Service")
		switch {
		case errors.Is(err, fs.ErrNotExist):
			d.add(statusFail, area, fmt.Sprintf("override for %s is missing, so %s no longer holds", o.name, o.why), fix)
			continue
		case err != nil:
			d.add(statusFail, area, fmt.Sprintf("override for %s: %v", o.name, err), fix)
			continue
		case keys["Name"] != o.name || keys["Exec"] != want:
			d.add(statusFail, area, fmt.Sprintf("override for %s says Name=%s Exec=%s, want Exec=%s",
				o.name, keys["Name"], keys["Exec"], want), fix)
			continue
		}
		if want != "/bin/false" {
			if exeErr := executable(want); exeErr != nil {
				d.add(statusFail, area, fmt.Sprintf("override for %s runs %v", o.name, exeErr), "")
				continue
			}
		}
		d.add(statusOK, area, "override for "+o.name+" is in place", "")

		// The override only wins over a system file with the same Name.
		// When no system file declares it any more, the package either
		// went or renamed its service -- and a renamed one is not
		// overridden.
		found, err := d.systemServiceNamed(o.name)
		switch {
		case err != nil:
			d.add(statusWarn, area, fmt.Sprintf("looking for the system's %s: %v", o.name, err), "")
		case !found:
			d.add(statusWarn, area, "the system no longer provides "+o.name+", so its override guards nothing",
				"if a package renamed the service, override the new name the same way")
		}
	}
}

// systemServiceNamed reports whether a system service file declares name.
// A file that cannot be read does not stop the search, but when nothing is
// found the unreadable files are the error: one of them may have been it.
func (d *doctor) systemServiceNamed(name string) (bool, error) {
	var unreadable []error
	for _, dir := range systemServiceDirs {
		matches, err := filepath.Glob(filepath.Join(d.sysPath(dir), "*.service"))
		if err != nil {
			return false, err
		}
		for _, m := range matches {
			keys, err := readGroup(m, "D-BUS Service")
			if err != nil {
				unreadable = append(unreadable, err)
				continue
			}
			if keys["Name"] == name {
				return true, nil
			}
		}
	}
	return false, errors.Join(unreadable...)
}

// systemUnitDirs are where packages put user systemd units.
var systemUnitDirs = []string{"/usr/lib/systemd/user", "/etc/systemd/user", "/usr/share/systemd/user"}

func (d *doctor) checkMasks() {
	const area = "systemd"
	dir := filepath.Join(d.home, ".config", "systemd", "user")
	for _, m := range masks {
		target, err := os.Readlink(filepath.Join(dir, m.unit))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			d.add(statusFail, area, m.unit+" is no longer masked: "+m.why, "systemctl --user mask "+m.unit)
			continue
		case err != nil:
			d.add(statusFail, area, fmt.Sprintf("%s: %v; it should be masked: %s", m.unit, err, m.why),
				"systemctl --user mask "+m.unit)
			continue
		case target != "/dev/null":
			d.add(statusFail, area, fmt.Sprintf("%s links to %s, not /dev/null: %s", m.unit, target, m.why),
				"systemctl --user mask "+m.unit)
			continue
		}
		d.add(statusOK, area, m.unit+" is masked", "")
		exists := slices.ContainsFunc(systemUnitDirs, func(dir string) bool {
			_, err := os.Stat(filepath.Join(d.sysPath(dir), m.unit))
			return err == nil
		})
		if !exists {
			d.add(statusWarn, area, "the system no longer has "+m.unit+", so its mask guards nothing",
				"if a package renamed the unit, mask the new name")
		}
	}
}

func (d *doctor) checkAutostart() {
	const area = "autostart"
	dir := filepath.Join(d.home, ".config", "autostart")
	sysDir := d.sysPath("/etc/xdg/autostart")

	for _, h := range hiddenAutostart {
		if !d.hidden(filepath.Join(dir, h.file), area) {
			d.add(statusFail, area, h.file+" is no longer hidden, so it starts at login",
				fmt.Sprintf("printf '[Desktop Entry]\\nHidden=true\\n' > %s", filepath.Join(dir, h.file)))
			continue
		}
		d.add(statusOK, area, h.file+" is hidden", "")
		if !h.system {
			continue
		}
		if _, err := os.Stat(filepath.Join(sysDir, h.file)); err != nil {
			d.add(statusWarn, area, "the system no longer has "+h.file+", so hiding it does nothing",
				"if a package renamed it, look for the new name in /etc/xdg/autostart and hide that")
		}
	}

	for _, t := range ourTools {
		if !t.autostart {
			continue
		}
		path := filepath.Join(dir, t.name+".desktop")
		keys, err := readGroup(path, "Desktop Entry")
		if err != nil {
			d.add(statusFail, area, fmt.Sprintf("%s does not start at login: %v", t.name, err),
				fmt.Sprintf("printf '[Desktop Entry]\\nType=Application\\nName=%s\\nExec=%s\\n' > %s",
					t.name, filepath.Join(d.home, ".local", "bin", t.name), path))
			continue
		}
		if strings.EqualFold(keys["Hidden"], "true") {
			d.add(statusFail, area, t.name+" is hidden from autostart", "remove Hidden=true from "+path)
			continue
		}
		exe, _, _ := strings.Cut(keys["Exec"], " ")
		if err := executable(exe); err != nil {
			d.add(statusFail, area, fmt.Sprintf("%s's autostart entry runs %v", t.name, err), "fix Exec= in "+path)
			continue
		}
		d.add(statusOK, area, t.name+" starts at login", "")
	}

	entries, err := os.ReadDir(sysDir)
	if err != nil {
		d.add(statusSkip, area, "new system autostart entries", err.Error())
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".desktop") || slices.Contains(knownAutostart, name) {
			continue
		}
		if d.hidden(filepath.Join(dir, name), area) {
			continue
		}
		label := name
		switch keys, err := readGroup(filepath.Join(sysDir, name), "Desktop Entry"); {
		case err != nil:
			label = fmt.Sprintf("%s (unreadable: %v)", name, err)
		case keys["Name"] != "":
			label = fmt.Sprintf("%s (%s)", name, keys["Name"])
		}
		d.add(statusWarn, area, "new in /etc/xdg/autostart: "+label+"; it starts at every login",
			"if it is not wanted, hide it: printf '[Desktop Entry]\\nHidden=true\\n' > "+filepath.Join(dir, name)+
				"; if it is, add it to knownAutostart in cmd/osxflow-doctor/setup.go")
	}
}

// hidden reports whether the desktop entry at path has Hidden=true. A
// missing file is not hidden; one that cannot be read is recorded, and
// counts as not hidden, which is what it is to XFCE.
func (d *doctor) hidden(path, area string) bool {
	keys, err := readGroup(path, "Desktop Entry")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false
	case err != nil:
		d.add(statusWarn, area, err.Error(), "")
		return false
	}
	return strings.EqualFold(keys["Hidden"], "true")
}

func (d *doctor) checkXfceSession() {
	const area = "xfce session"
	props, err := d.sys.XfconfAll("xfce4-session", "/")
	if skipXfconf(d, area, "XFCE's Failsafe session", err) {
		return
	}
	count, ok := props["/sessions/Failsafe/Count"].(int32)
	if !ok {
		d.add(statusWarn, area, "the Failsafe session has no client count; XFCE may have changed how it stores sessions",
			"xfconf-query -c xfce4-session -lv | grep Failsafe")
		return
	}
	var clients []string
	for i := range count {
		cmd := strings.Join(strs(props[fmt.Sprintf("/sessions/Failsafe/Client%d_Command", i)]), " ")
		clients = append(clients, cmd)
		prog, _, _ := strings.Cut(cmd, " ")
		if slices.Contains(sessionRetired, prog) {
			d.add(statusFail, area, fmt.Sprintf("the session starts %s again (Client%d)", prog, i),
				"move it past the end of the list and lower /sessions/Failsafe/Count, as in the menubar README")
		}
	}
	d.add(statusOK, area, "the session starts: "+strings.Join(clients, ", "), "")

	if save, ok := props["/general/SaveOnExit"].(bool); ok && save {
		d.add(statusWarn, area, "XFCE saves the session on logout, and a saved session starts whatever was running instead of Failsafe",
			"xfconf-query -c xfce4-session -p /general/SaveOnExit -s false")
	}
	d.checkSavedSession(props)
}

// checkSavedSession looks for a session XFCE saved on some logout. While
// one exists, login restores it and never reads Failsafe at all, so a
// program retired from Failsafe comes back from there.
func (d *doctor) checkSavedSession(props map[string]any) {
	const area = "xfce session"
	name, ok := props["/general/SessionName"].(string)
	if !ok || name == "" {
		name = "Default"
	}
	files, err := filepath.Glob(filepath.Join(d.home, ".cache", "sessions", "xfce4-session-*"))
	if err != nil {
		d.add(statusSkip, area, "saved sessions", err.Error())
		return
	}
	for _, f := range files {
		keys, err := readGroup(f, "Session: "+name)
		if err != nil {
			d.add(statusWarn, area, "cannot read the saved session "+f, err.Error())
			continue
		}
		for k, prog := range keys {
			if strings.HasPrefix(k, "Client") && strings.HasSuffix(k, "_Program") && slices.Contains(sessionRetired, prog) {
				d.add(statusFail, area, fmt.Sprintf("a saved session starts %s again at login, instead of Failsafe", prog),
					"rm "+f)
			}
		}
	}
}

// skipXfconf records a skip when xfconf could not be read, and says so.
func skipXfconf(d *doctor, area, what string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNoBus):
		d.add(statusSkip, area, what, "no session bus: not run from the desktop session")
	case errors.Is(err, xfconf.ErrNoXfconf):
		d.add(statusSkip, area, what, "xfconfd is not running: not an XFCE session")
	default:
		d.add(statusFail, area, fmt.Sprintf("%s: %v", what, err), "")
	}
	return true
}

// strs reads an xfconf string array, which arrives as variants, or a
// lone string.
func strs(v any) []string {
	switch vs := v.(type) {
	case string:
		return []string{vs}
	case []dbus.Variant:
		out := make([]string, 0, len(vs))
		for _, x := range vs {
			if s, ok := x.Value().(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (d *doctor) checkShortcuts() {
	const area = "shortcuts"
	const base = "/commands/custom/"
	props, err := d.sys.XfconfAll("xfce4-keyboard-shortcuts", strings.TrimSuffix(base, "/"))
	if skipXfconf(d, area, "keyboard shortcuts", err) {
		return
	}
	launcher := d.homePath("~/.local/bin/launcher")
	// Not a string -- unset, or something odd -- reads as "", which is
	// not the launcher either.
	got, isString := props[base+launcherShortcut].(string)
	if !isString || got != launcher {
		d.add(statusFail, area, fmt.Sprintf("%s runs %q, not the launcher, so Cmd+Space does nothing", launcherShortcut, got),
			fmt.Sprintf("xfconf-query -c xfce4-keyboard-shortcuts -p '%s%s' -n -t string -s %s", base, launcherShortcut, launcher))
	} else {
		d.add(statusOK, area, "Cmd+Space ("+launcherShortcut+") opens the launcher", "")
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	broken := 0
	for _, k := range keys {
		cmd, ok := props[k].(string)
		key := strings.TrimPrefix(k, base)
		if !ok || strings.Contains(key, "/") || key == "override" || key == launcherShortcut {
			continue
		}
		prog, _, _ := strings.Cut(strings.TrimSpace(cmd), " ")
		remove := fmt.Sprintf("xfconf-query -c xfce4-keyboard-shortcuts -p '%s' -r -R", k)
		// Installed is not enough: xfdesktop --menu does nothing without
		// xfdesktop running, which is how Ctrl+Escape went dead.
		if i := slices.IndexFunc(retired, func(r retiredProgram) bool {
			return r.comm == filepath.Base(prog)
		}); i >= 0 {
			broken++
			d.add(statusWarn, area, fmt.Sprintf("%s runs %s, which %s replaced", key, prog, retired[i].replacedBy), remove)
			continue
		}
		if d.found(prog) {
			continue
		}
		broken++
		d.add(statusWarn, area, fmt.Sprintf("%s runs %s, which is not installed", key, prog), remove)
	}
	if broken == 0 {
		d.add(statusOK, area, "every shortcut runs something installed", "")
	}
}

// found reports whether a program can be run: an absolute path that is
// executable, or a name on PATH.
func (d *doctor) found(prog string) bool {
	if prog == "" {
		return false
	}
	if filepath.IsAbs(prog) {
		return executable(prog) == nil
	}
	_, err := d.sys.LookPath(prog)
	return err == nil
}

func (d *doctor) checkCommands() {
	const area = "commands"
	for _, c := range commands {
		names := strings.Split(c.names, "|")
		if i := slices.IndexFunc(names, d.found); i >= 0 {
			d.add(statusOK, area, names[i]+" is installed", "")
			continue
		}
		s := statusWarn
		if c.required {
			s = statusFail
		}
		d.add(s, area, fmt.Sprintf("%s is not installed, which breaks %s", strings.Join(names, " or "), c.usedFor),
			"install it again, or change the tool to use what replaced it")
	}
}

func (d *doctor) checkDisplay() {
	const area = "display"
	path := filepath.Join(d.home, ".config", "xfce4", "xfconf", "xfce-perchannel-xml", "xsettings.xml")
	switch factor, err := scale.FromXfconfFile(path); {
	case err != nil:
		d.add(statusWarn, area, fmt.Sprintf("the display scale cannot be read (%v); the tools will draw at 1x", err),
			"set Settings > Appearance > Settings > Window Scaling to 2x")
	default:
		d.add(statusOK, area, fmt.Sprintf("the display scale is %gx", factor), "")
	}

	for _, set := range []struct {
		name       string
		candidates []string
	}{{"regular", text.Candidates}, {"bold", text.BoldCandidates}} {
		paths := make([]string, len(set.candidates))
		for i, c := range set.candidates {
			paths[i] = d.sysPath(c)
		}
		_, got, err := text.Load(paths)
		switch {
		case err != nil:
			d.add(statusFail, area, fmt.Sprintf("no %s font: %v", set.name, err), "sudo apt install fonts-ubuntu fonts-dejavu-core")
		case got != paths[0]:
			d.add(statusWarn, area, fmt.Sprintf("the %s font is %s, not %s; the tools look different", set.name, got, set.candidates[0]),
				"sudo apt install fonts-ubuntu")
		default:
			d.add(statusOK, area, "the "+set.name+" font is "+set.candidates[0], "")
		}
	}
}

func (d *doctor) checkUnpack() {
	const area = "unpack"
	entry := filepath.Join(d.home, ".local", "share", "applications", unpackDesktop)
	keys, err := readGroup(entry, "Desktop Entry")
	if err != nil {
		d.add(statusWarn, area, fmt.Sprintf("%s: %v; archives do not open with unpack", unpackDesktop, err), "see cmd/unpack/README.md")
		return
	}
	defaults, err := readGroup(filepath.Join(d.home, ".config", "mimeapps.list"), "Default Applications")
	if err != nil {
		d.add(statusWarn, area, fmt.Sprintf("reading mimeapps.list: %v", err), "see cmd/unpack/README.md")
		return
	}
	taken := 0
	for _, mime := range strings.Split(keys["MimeType"], ";") {
		if mime == "" {
			continue
		}
		first, _, _ := strings.Cut(defaults[mime], ";")
		if first == unpackDesktop {
			continue
		}
		taken++
		d.add(statusWarn, area, fmt.Sprintf("%s opens with %q, not unpack", mime, first),
			fmt.Sprintf("xdg-mime default %s %s", unpackDesktop, mime))
	}
	if taken == 0 {
		d.add(statusOK, area, "every archive type opens with unpack", "")
	}
}
