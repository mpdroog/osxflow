package main

// The tray: the items registered with the watcher, what each looks like,
// and what clicking one does.
//
// Everything that talks to an item runs on a goroutine of its own and
// reports back to the main loop through a channel: an item is another
// process, and one that hangs must not freeze the bar.

import (
	"context"
	"image"
	"log"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb/xproto"
	xdraw "golang.org/x/image/draw"

	"github.com/mpdroog/osxflow/internal/dbusmenu"
	"github.com/mpdroog/osxflow/internal/sni"
	"github.com/mpdroog/osxflow/internal/text"
)

// callTimeout bounds one call to an item.
const callTimeout = 5 * time.Second

// The buttons X reports for tilting the wheel, which xproto has no names
// for.
const (
	buttonScrollLeft  xproto.Button = 6
	buttonScrollRight xproto.Button = 7
)

// trayItem is one registered item as the bar knows it.
type trayItem struct {
	remote *sni.Remote

	// owner is the unique name signals from the item arrive from, once
	// known.
	owner string

	state sni.State
	icon  *image.RGBA

	// ready is set once the first fetch has come back: an item is not
	// drawn before there is anything to draw.
	ready bool

	// gen counts fetches, so that a slow reply that arrives after a newer
	// one does not overwrite it.
	gen uint64
}

// fetched is the result of reading an item's properties.
type fetched struct {
	ref      sni.Ref
	gen      uint64
	owner    string
	state    sni.State
	problems []error
}

// openMenu is an item's menu, read and ready to show.
type openMenu struct {
	client  *dbusmenu.Client
	root    dbusmenu.Entry
	centreX int
	err     error
}

// syncItems brings the bar's items in line with the watcher's list.
func (a *app) syncItems() {
	refs := a.watcher.Items()
	for ref := range a.items {
		if !slices.Contains(refs, ref) {
			delete(a.items, ref)
		}
	}
	for _, ref := range refs {
		if _, ok := a.items[ref]; ok {
			continue
		}
		it := &trayItem{remote: sni.NewRemote(a.bus, ref)}
		a.items[ref] = it
		a.refs = append(a.refs, ref)
		a.fetch(it)
	}
	a.refs = slices.DeleteFunc(a.refs, func(r sni.Ref) bool { _, ok := a.items[r]; return !ok })
	a.relayout()
}

// fetch reads an item's properties again.
func (a *app) fetch(it *trayItem) {
	it.gen++
	gen, owner, remote := it.gen, it.owner, it.remote
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		res := fetched{ref: remote.Ref, gen: gen, owner: owner}
		if res.owner == "" {
			var err error
			if res.owner, err = remote.Owner(ctx); err != nil {
				// Its changes will go unnoticed until it registers again,
				// but it can still be drawn and clicked.
				res.problems = append(res.problems, err)
			}
		}
		state, problems := remote.Fetch(ctx)
		res.state = state
		res.problems = append(res.problems, problems...)
		a.fetches <- res
	}()
}

// applyFetch takes a fetch's result in, unless it is stale or the item has
// gone since.
func (a *app) applyFetch(res *fetched) {
	it, ok := a.items[res.ref]
	if !ok || res.gen != it.gen {
		return
	}
	for _, p := range res.problems {
		log.Printf("tray item %s: %v", res.ref, p)
	}
	if res.owner != "" {
		it.owner = res.owner
	}
	it.state = res.state
	it.ready = true
	it.icon = a.itemIcon(&it.state)
	a.relayout()
}

// itemChanged refetches the item a signal came from.
func (a *app) itemChanged(sender, path string) {
	for _, it := range a.items {
		if it.owner == sender && string(it.remote.Ref.Path) == path {
			a.fetch(it)
		}
	}
}

// itemIcon draws an item's icon at the bar's icon size: its own pixels
// when it sent any, or else its initial. Icons by theme name are not
// looked up -- the theme is SVG, which cannot be drawn without cgo -- and
// no item here uses one.
func (a *app) itemIcon(s *sni.State) *image.RGBA {
	size := a.m.icon
	src, problems := sni.Best(s.Icon, size)
	if src != nil {
		for _, p := range problems {
			log.Printf("tray item %s: skipping a pixmap: %v", s.ID, p)
		}
		return fit(src, size)
	}
	if s.IconName != "" {
		log.Printf("tray item %s: icon %q is by theme name, which is not supported; drawing its initial", s.ID, s.IconName)
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	label := s.Title
	if label == "" {
		label = s.ID
	}
	if r, _ := utf8.DecodeRuneInString(label); r != utf8.RuneError {
		m := a.bold.Metrics()
		baseline := size/2 + (m.Ascent-m.Descent).Round()/2
		text.DrawCentred(img, a.bold, colText, size/2, baseline, string(r), size)
	}
	return img
}

// fit scales an icon to fit a square of side size, keeping its shape.
func fit(src *image.RGBA, size int) *image.RGBA {
	b := src.Bounds()
	if b.Dx() == size && b.Dy() == size {
		return src
	}
	w, h := size, size
	if b.Dx() > b.Dy() {
		h = max(1, size*b.Dy()/b.Dx())
	} else if b.Dy() > b.Dx() {
		w = max(1, size*b.Dx()/b.Dy())
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	off := image.Pt((size-w)/2, (size-h)/2)
	xdraw.CatmullRom.Scale(dst, image.Rectangle{Min: off, Max: off.Add(image.Pt(w, h))}, src, b, xdraw.Src, nil)
	return dst
}

// clickItem acts on a press on an item. centreX is the middle of its
// slot, where anything it opens goes.
func (a *app) clickItem(it *trayItem, button xproto.Button, centreX int) {
	s := &it.state
	hasMenu := s.Menu != ""
	var c sni.Click
	switch button {
	case xproto.ButtonIndex1:
		if hasMenu && s.ItemIsMenu {
			a.readMenu(it, centreX)
			return
		}
		c = sni.Click{Kind: sni.Activate}
	case xproto.ButtonIndex2:
		c = sni.Click{Kind: sni.SecondaryActivate}
	case xproto.ButtonIndex3:
		if hasMenu {
			a.readMenu(it, centreX)
			return
		}
		c = sni.Click{Kind: sni.ContextMenu}
	case xproto.ButtonIndex4:
		c = sni.Click{Kind: sni.Scroll, Delta: -1}
	case xproto.ButtonIndex5:
		c = sni.Click{Kind: sni.Scroll, Delta: 1}
	case buttonScrollLeft:
		c = sni.Click{Kind: sni.Scroll, Delta: -1, Horizontal: true}
	case buttonScrollRight:
		c = sni.Click{Kind: sni.Scroll, Delta: 1, Horizontal: true}
	default:
		return
	}
	c.X, c.Y = centreX, a.m.height
	remote, menuPath := it.remote, s.Menu
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		err := remote.Click(ctx, c)
		switch {
		case err == nil:
		case sni.Unsupported(err) && hasMenu && c.Kind == sni.Activate:
			// An item with only a menu to offer, whatever ItemIsMenu says.
			a.menuFor(remote, menuPath, centreX)
		default:
			a.errs <- err
		}
	}()
}

// readMenu reads an item's menu and hands it to the main loop to open.
func (a *app) readMenu(it *trayItem, centreX int) {
	go a.menuFor(it.remote, it.state.Menu, centreX)
}

// menuFor reads the menu at path on an item's connection, on the calling
// goroutine, and hands it to the main loop.
func (a *app) menuFor(remote *sni.Remote, path dbus.ObjectPath, centreX int) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	client := dbusmenu.New(a.bus, remote.Ref.Service, path)
	if err := client.AboutToShow(ctx); err != nil {
		// The menu may be stale, but there is one to show.
		a.errs <- err
	}
	root, err := client.Layout(ctx)
	a.menus <- openMenu{client: client, root: root, centreX: centreX, err: err}
}

// showMenu opens a menu read by menuFor.
func (a *app) showMenu(m *openMenu) {
	if m.err != nil {
		log.Print(m.err)
		return
	}
	client := m.client
	rows := menuRows(&m.root, func(id int32) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			if err := client.Clicked(ctx, id); err != nil {
				a.errs <- err
			}
		}()
	})
	if len(rows) == 0 {
		log.Print("a tray item's menu has nothing in it to show")
		return
	}
	if err := a.host.Open(m.centreX, rows); err != nil {
		log.Printf("opening a tray item's menu: %v", err)
	}
}
