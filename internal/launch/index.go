package launch

// The reverse of Match: given a window, which application owns it?
//
// The launcher only ever asks the forward question -- the user picked this
// app, find its window -- and answers it by scoring every window against
// one app. The dock asks the opposite question about every window on the
// desktop, several times a second, so scoring 155 applications against
// each of perhaps twenty windows is the wrong shape: it would mean
// thousands of /proc reads whenever a window opened.
//
// Index inverts the same three rules Match scores with, once, into maps.
// Answering then costs one /proc read and two map lookups per window.

import (
	"path/filepath"
	"strings"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/xwin"
)

// Index answers "which application is this window?".
type Index struct {
	// apps is owned by the Index: the maps below hold pointers into it, so
	// it must not be resliced or reordered after construction.
	apps []desktop.App

	byID     map[string]*desktop.App
	byBinary map[string]*desktop.App
	byClass  map[string]*desktop.App
	byName   map[string]*desktop.App
}

// NewIndex builds the lookup tables. It takes ownership of apps.
func NewIndex(apps []desktop.App) *Index {
	ix := &Index{
		apps:     apps,
		byID:     make(map[string]*desktop.App, len(apps)),
		byBinary: make(map[string]*desktop.App, len(apps)),
		byClass:  make(map[string]*desktop.App, len(apps)),
		byName:   make(map[string]*desktop.App, len(apps)),
	}
	for i := range ix.apps {
		app := &ix.apps[i]
		ix.byID[app.ID] = app
		ix.byID[strings.TrimSuffix(app.ID, ".desktop")] = app
		if app.Binary != "" {
			// First entry wins. Two entries can share a binary -- a
			// "Firefox" and a "Firefox (Private)" both run /usr/bin/firefox
			// -- and the first in XDG precedence order is the canonical one.
			putIfAbsent(ix.byBinary, app.Binary, app)
		}
		if app.StartupWMClass != "" {
			putIfAbsent(ix.byClass, strings.ToLower(app.StartupWMClass), app)
		}
		for _, name := range candidateNames(app) {
			putIfAbsent(ix.byName, strings.ToLower(name), app)
		}
	}
	return ix
}

func putIfAbsent(m map[string]*desktop.App, key string, app *desktop.App) {
	if _, ok := m[key]; !ok {
		m[key] = app
	}
}

// ByDesktopID finds an application by its desktop file id, with or without
// the ".desktop" suffix.
func (ix *Index) ByDesktopID(id string) *desktop.App {
	return ix.byID[id]
}

// Apps returns every indexed application.
func (ix *Index) Apps() []desktop.App { return ix.apps }

// Owner returns the application a window belongs to.
//
// The rules and their precedence are Match's, so the dock and the launcher
// agree about what belongs to what: the executable behind the window's pid
// is proof, a declared StartupWMClass is the entry author's word, and
// WM_CLASS against a binary name is the inference that covers everything
// else.
func (ix *Index) Owner(w *xwin.Window, exe ExeFunc) (*desktop.App, Confidence) {
	if w.PID != 0 && exe != nil {
		if path, ok := exePath(exe, w.PID); ok {
			if app, ok := ix.byBinary[path]; ok {
				return app, ByExecutable
			}
			// The pid's executable is not any entry's Exec, which is the
			// normal case for anything started through a wrapper. Its
			// basename is still a better class guess than WM_CLASS.
			if app, ok := ix.byName[strings.ToLower(filepath.Base(path))]; ok {
				return app, ByBinaryName
			}
		}
	}
	for _, class := range [...]string{w.Instance, w.Class} {
		if class == "" {
			continue
		}
		if app, ok := ix.byClass[strings.ToLower(class)]; ok {
			return app, ByDeclaredClass
		}
	}
	for _, class := range [...]string{w.Instance, w.Class} {
		if class == "" {
			continue
		}
		if app, ok := ix.byName[strings.ToLower(class)]; ok {
			return app, ByBinaryName
		}
	}
	return nil, None
}

// CachedExe wraps an ExeFunc with a cache for one sweep over the window
// list.
//
// Several windows usually belong to one process -- a browser with three
// windows is three entries in _NET_CLIENT_LIST with the same pid -- so
// without this the dock would read the same /proc link repeatedly every
// time a window opened or closed.
func CachedExe(exe ExeFunc) ExeFunc {
	type result struct {
		path string
		err  error
	}
	cache := map[uint32]result{}
	return func(pid uint32) (string, error) {
		if r, ok := cache[pid]; ok {
			return r.path, r.err
		}
		path, err := exe(pid)
		cache[pid] = result{path: path, err: err}
		return path, err
	}
}
