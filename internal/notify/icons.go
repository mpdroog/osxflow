package notify

import (
	"path/filepath"
	"strings"

	"github.com/mpdroog/osxflow/internal/desktop"
)

// IconIndex finds the embedded icon for the application behind a
// notification.
//
// Senders name themselves inconsistently. Some send an app_icon naming
// their theme icon ("thunderbird"), GLib applications send a
// desktop-entry hint ("org.gnome.Calendar"), and plenty send only a
// display name ("Firefox") or nothing but the name of the tool that sent
// it ("notify-send"). The index maps every one of those forms that an
// installed application answers to onto the key of its icon in
// internal/icons.
type IconIndex struct {
	byName map[string]string
}

// NewIconIndex indexes the applications that have an embedded icon; has
// reports whether one exists for a key.
func NewIconIndex(apps []desktop.App, has func(key string) bool) *IconIndex {
	ix := &IconIndex{byName: make(map[string]string, len(apps)*4)}
	for i := range apps {
		app := &apps[i]
		key := strings.TrimSuffix(app.ID, ".desktop")
		if !has(key) {
			continue
		}
		// First writer wins, and the key itself goes in first, so an
		// application's own id always resolves to its own icon.
		for _, name := range [...]string{key, app.Name, app.Icon, app.StartupWMClass, filepath.Base(app.Binary)} {
			ix.add(name, key)
		}
	}
	return ix
}

func (ix *IconIndex) add(name, key string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "." || strings.Contains(name, "/") {
		return
	}
	if _, ok := ix.byName[name]; !ok {
		ix.byName[name] = key
	}
}

// Lookup returns the key of the embedded icon for n, or "" when no
// installed application matches.
func (ix *IconIndex) Lookup(n *Notification) string {
	for _, name := range [...]string{n.Icon.Name, n.DesktopEntry, n.AppName} {
		if key := ix.lookup(name); key != "" {
			return key
		}
	}
	return ""
}

func (ix *IconIndex) lookup(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".desktop")
	if name == "" {
		return ""
	}
	if key, ok := ix.byName[name]; ok {
		return key
	}
	// A reverse-DNS id whose owner installed under a plain name:
	// "org.mozilla.thunderbird" is thunderbird.desktop here.
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i < len(name)-1 {
		if key, ok := ix.byName[name[i+1:]]; ok {
			return key
		}
	}
	return ""
}
