package main

// The life of a banner: appearing, being changed, being hovered and
// clicked, and going away.

import (
	"errors"
	"fmt"
	"image"
	"io/fs"
	"log"
	"math"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	xdraw "golang.org/x/image/draw"

	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/notify"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/xsurface"
)

// banner is one notification on screen.
type banner struct {
	id  uint32
	rev uint64
	n   notify.Notification

	icon *image.RGBA
	lay  layout

	win  xproto.Window
	surf *xsurface.Surface

	// restX is where the window sits once it has arrived; x is the offset
	// from there, which is how a banner slides in from the edge of the
	// screen and drifts back out as it goes. y is absolute.
	restX int
	x, y  dock.Anim

	// curX and curY are where the window was last put, so a frame that
	// moves it less than a pixel sends nothing.
	curX, curY int

	// closing banners have left the queue and are fading out. snapshot is
	// what the banner looked like when it started to go, and every frame of
	// the fade is that, dimmed.
	closing  bool
	fade     dock.Anim
	snapshot *image.RGBA

	hover bool
	part  part
}

// Animation time constants, in seconds; see dock.Anim.
const (
	slideTau = 0.075
	moveTau  = 0.06
	fadeTau  = 0.09
)

// maxAttempts is how many times in a row a notification may fail to get a
// banner before it is closed instead. A failure can be passing -- the
// server short of memory for a moment -- so one is not enough; but a
// notification that can never be shown should not sit in the queue for
// good, holding a place and telling its sender it is up.
const maxAttempts = 3

// attempts counts, per notification, the failed attempts in a row to put
// it on screen.
type attempts map[uint32]int

// fail records one failure and reports whether that was the last allowed.
func (a attempts) fail(id uint32) (giveUp bool) {
	a[id]++
	if a[id] < maxAttempts {
		return false
	}
	delete(a, id)
	return true
}

// keep forgets every notification not in live, which is the ones that have
// closed or gone back to waiting.
func (a attempts) keep(live map[uint32]bool) {
	for id := range a {
		if !live[id] {
			delete(a, id)
		}
	}
}

// sync makes the banners on screen match the queue: new ones appear,
// changed ones are redrawn, closed ones fade, and everything slides into
// its place in the stack.
//
// A notification whose banner cannot be made (after maxAttempts tries) or
// cannot be redrawn is closed with reason "undefined", which tells its
// sender it is gone -- better than one that stays open and never appears.
func (d *daemon) sync() { d.syncPass(nil) }

// syncPass is one pass of sync. tried holds the notifications whose banner
// already failed to be created earlier in the same sync, which the pass
// that follows a closure must not try again: every attempt counts toward
// closing the notification, and one change to the queue is meant to cost
// one attempt, not two.
func (d *daemon) syncPass(tried map[uint32]bool) {
	shown := d.q.Shown()
	live := make(map[uint32]bool, len(shown))
	top := d.area.Min.Y + d.th.marginT
	restX := d.area.Max.X - d.th.marginR - d.th.width - d.th.winPadX
	var broken []uint32

	for i := range shown {
		s := &shown[i]
		live[s.ID] = true
		b := d.banners[s.ID]
		switch {
		case b == nil:
			if tried[s.ID] {
				continue
			}
			created, err := d.newBanner(s, restX, top)
			if err != nil {
				if tried == nil {
					tried = make(map[uint32]bool)
				}
				tried[s.ID] = true
				if d.failed.fail(s.ID) {
					log.Printf("%v; closing notification %d after %d attempts", err, s.ID, maxAttempts)
					broken = append(broken, s.ID)
				} else {
					// Retried at the next change to the queue, which may be
					// the next event; limited per notification for that.
					d.lim.Printf(fmt.Sprintf("banner:%d", s.ID), "%v", err)
				}
				continue
			}
			delete(d.failed, s.ID)
			b = created
		case b.rev != s.Rev:
			if err := d.update(b, s); err != nil {
				log.Printf("%v; closing notification %d", err, s.ID)
				broken = append(broken, s.ID)
				continue
			}
		}
		if b.restX != restX {
			b.restX = restX
			d.place(b)
		}
		b.y.Target = float64(top - d.th.winPadY)
		top += b.lay.plate.Dy() + d.th.gap
	}
	d.failed.keep(live)
	for id, b := range d.banners {
		if !live[id] && !b.closing {
			d.startClose(b)
		}
	}

	if len(broken) > 0 {
		now := time.Now()
		for _, id := range broken {
			d.emitClosed(d.q.Close(id, notify.ReasonUndefined, now))
		}
		// Closing made room for whatever was waiting. This goes no deeper:
		// what comes on screen now has no banner to fail redrawing, and
		// one failure to create is not enough to be closed -- the more so
		// as whatever already failed in this sync is not tried again.
		d.syncPass(tried)
	}
}

// newBanner creates the window for a notification that has just come on
// screen, just off the right edge so that it slides in.
func (d *daemon) newBanner(s *notify.Shown, restX, plateTop int) (*banner, error) {
	b := &banner{id: s.ID, rev: s.Rev, n: s.Notification, restX: restX}
	b.icon = d.iconFor(&b.n)
	b.lay = measure(d.th, d.faces, &b.n)

	y := plateTop - d.th.winPadY
	b.x = dock.Anim{Value: float64(b.lay.size.X), Tau: slideTau}
	b.y = dock.Anim{Value: float64(y), Target: float64(y), Tau: moveTau}
	b.curX, b.curY = restX+b.lay.size.X, y

	win, err := d.createWindow(b.curX, b.curY, b.lay.size.X, b.lay.size.Y)
	if err != nil {
		return nil, fmt.Errorf("banner %d: %w", b.id, err)
	}
	b.win = win
	b.surf, err = xsurface.New(d.conn, win, d.visual.depth, b.lay.size.X, b.lay.size.Y)
	if err != nil {
		err = fmt.Errorf("banner %d surface: %w", b.id, err)
		if destroyErr := xproto.DestroyWindowChecked(d.conn, win).Check(); destroyErr != nil {
			err = errors.Join(err, fmt.Errorf("destroying banner %d window: %w", b.id, destroyErr))
		}
		return nil, err
	}
	d.banners[b.id] = b
	d.byWin[win] = b

	// Drawn before mapping, so the first Expose has a finished frame to
	// copy rather than an empty window to show.
	d.render(b)
	if mapErr := xproto.MapWindowChecked(d.conn, win).Check(); mapErr != nil {
		err = fmt.Errorf("showing banner %d: %w", b.id, mapErr)
		return nil, errors.Join(err, d.destroy(b))
	}
	d.raise(win)
	return b, nil
}

// update redraws a banner whose notification was replaced.
//
// When the banner cannot take its new size it is destroyed, and the error
// says so: drawing the new layout onto a surface of the old size would put
// it past the surface's edges, and the caller closes the notification.
func (d *daemon) update(b *banner, s *notify.Shown) error {
	b.rev, b.n = s.Rev, s.Notification
	b.icon = d.iconFor(&b.n)
	old := b.lay.size
	b.lay = measure(d.th, d.faces, &b.n)
	if b.lay.size != old {
		if err := d.resize(b); err != nil {
			return errors.Join(err, d.destroy(b))
		}
	}
	d.render(b)
	return nil
}

// resize fits a banner's window and surface to a new layout.
//
// The new surface is made before the old one is freed, so that on failure
// the banner still holds a surface that exists -- one freed first would
// leave every later frame and Expose drawing to a pixmap that is gone.
func (d *daemon) resize(b *banner) error {
	if err := d.resizeWindow(b.win, b.lay.size.X, b.lay.size.Y); err != nil {
		return fmt.Errorf("resizing banner %d: %w", b.id, err)
	}
	surf, err := xsurface.New(d.conn, b.win, d.visual.depth, b.lay.size.X, b.lay.size.Y)
	if err != nil {
		return fmt.Errorf("resizing banner %d: %w", b.id, err)
	}
	old := b.surf
	b.surf = surf
	if closeErr := old.Close(); closeErr != nil {
		// The banner has what it needs; at worst the old pixmap stays
		// allocated until the connection closes.
		log.Printf("banner %d: freeing the surface it outgrew: %v", b.id, closeErr)
	}
	return nil
}

// startClose begins a banner's exit: it fades while drifting right, and
// the banners below close the gap at the same time.
func (d *daemon) startClose(b *banner) {
	b.closing = true
	img := b.surf.Image()
	b.snapshot = image.NewRGBA(img.Bounds())
	copy(b.snapshot.Pix, img.Pix)
	b.fade = dock.Anim{Target: 1, Tau: fadeTau}
	b.x.Target, b.x.Tau = d.th.closeSlide, fadeTau
	if b.hover {
		b.hover = false
		d.updatePause()
	}
}

// destroy takes a banner off the screen and frees what it holds on the
// server. The banner is forgotten whatever the result: an error means the
// server had already lost track of the window or the surface, and there is
// nothing left worth retrying.
func (d *daemon) destroy(b *banner) error {
	delete(d.banners, b.id)
	delete(d.byWin, b.win)
	var errs []error
	if b.surf != nil {
		if err := b.surf.Close(); err != nil {
			errs = append(errs, fmt.Errorf("banner %d surface: %w", b.id, err))
		}
		b.surf = nil
	}
	if err := xproto.DestroyWindowChecked(d.conn, b.win).Check(); err != nil {
		errs = append(errs, fmt.Errorf("destroying banner %d: %w", b.id, err))
	}
	return errors.Join(errs...)
}

// step advances every animation by one frame.
func (d *daemon) step(now time.Time) {
	dt := now.Sub(d.lastTick).Seconds()
	d.lastTick = now
	for _, b := range d.banners {
		movedX := b.x.Step(dt)
		movedY := b.y.Step(dt)
		if movedX || movedY {
			d.place(b)
		}
		if !b.closing {
			continue
		}
		if b.fade.Done() {
			// Deleting during range is allowed.
			if err := d.destroy(b); err != nil {
				d.lim.Printf("destroy", "%v", err)
			}
			continue
		}
		if b.fade.Step(dt) {
			d.present(b)
		}
	}
}

// settled reports whether nothing is moving, which is when the frame
// ticker can stop.
func (d *daemon) settled() bool {
	for _, b := range d.banners {
		if b.closing || !b.x.Done() || !b.y.Done() {
			return false
		}
	}
	return true
}

// place moves a banner's window to where its animations say it is.
func (d *daemon) place(b *banner) {
	x := b.restX + int(math.Round(b.x.Value))
	y := int(math.Round(b.y.Value))
	if x == b.curX && y == b.curY {
		return
	}
	b.curX, b.curY = x, y
	d.moveWindow(b.win, x, y)
}

// present shows a closing banner's current frame of fade.
func (d *daemon) present(b *banner) {
	paint.Fade(b.surf.Image(), b.snapshot, 1-b.fade.Value)
	d.flush(b)
}

func (d *daemon) flush(b *banner) {
	if err := b.surf.Flush(); err != nil {
		// Every frame of a fade comes through here, so a failure that sticks
		// would otherwise be logged at frame rate.
		d.lim.Printf("flush", "drawing banner %d: %v", b.id, err)
	}
}

// handle applies one X event.
func (d *daemon) handle(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if b := d.byWin[e.Window]; b != nil && e.Count == 0 {
			if err := b.surf.Copy(); err != nil {
				d.lim.Printf("copy", "repainting banner %d: %v", b.id, err)
			}
		}

	case xproto.EnterNotifyEvent:
		if b := d.live(e.Event); b != nil {
			b.hover = true
			b.part = b.lay.hit(int(e.EventX), int(e.EventY))
			d.updatePause()
			d.render(b)
		}

	case xproto.LeaveNotifyEvent:
		if b := d.live(e.Event); b != nil {
			b.hover, b.part = false, part{}
			d.updatePause()
			d.render(b)
		}

	case xproto.MotionNotifyEvent:
		if b := d.live(e.Event); b != nil {
			if p := b.lay.hit(int(e.EventX), int(e.EventY)); p != b.part {
				b.part = p
				d.render(b)
			}
		}

	case xproto.ButtonPressEvent:
		if b := d.live(e.Event); b != nil {
			d.click(b, e)
		}

	case xproto.VisibilityNotifyEvent:
		// Nothing keeps an override-redirect window on top. Only a window
		// that is not one of ours counts as covering a banner, though:
		// banners overlap each other while they slide, and raising one
		// over another would cover that one, which would raise itself, and
		// so on for as long as they overlapped.
		if b := d.byWin[e.Window]; b != nil && e.State != xproto.VisibilityUnobscured {
			covered, err := d.coveredByOther(b)
			if err != nil {
				// The answer it gives is still the best available.
				d.lim.Printf("covered", "checking what covers banner %d: %v", b.id, err)
			}
			if covered {
				d.raise(b.win)
			}
		}

	case xproto.PropertyNotifyEvent:
		if e.Window == d.screen.Root && e.Atom == d.atoms.workarea {
			d.updateWorkArea()
			d.sync()
		}
	}
}

// live returns the banner a window belongs to, unless it is on its way
// out: a fading banner has already been closed and answers to nothing.
func (d *daemon) live(win xproto.Window) *banner {
	if b := d.byWin[win]; b != nil && !b.closing {
		return b
	}
	return nil
}

// click acts on a button press.
//
// The left button does what the pointer is over: the close button closes,
// an action button invokes its action, and the body invokes the default
// action or, when there is none, dismisses the notification. The right
// button always dismisses, which is xfce4-notifyd's habit and saves aiming
// for a small close button.
func (d *daemon) click(b *banner, e xproto.ButtonPressEvent) {
	const (
		leftButton  = 1
		rightButton = 3
	)
	now := time.Now()
	switch e.Detail {
	case rightButton:
		d.emitClosed(d.q.Close(b.id, notify.ReasonDismissed, now))
	case leftButton:
		switch p := b.lay.hit(int(e.EventX), int(e.EventY)); p.kind {
		case partClose:
			d.emitClosed(d.q.Close(b.id, notify.ReasonDismissed, now))
		case partButton:
			d.invoke(b, b.lay.buttons[p.index].key, e.Time, now)
		case partBody:
			if b.n.HasAction(notify.DefaultAction) {
				d.invoke(b, notify.DefaultAction, e.Time, now)
			} else {
				d.emitClosed(d.q.Close(b.id, notify.ReasonDismissed, now))
			}
		case partNone:
		}
	}
	d.sync()
}

// invoke tells the sender an action was chosen.
//
// The activation token goes first, as the specification orders. Its
// _TIME suffix carries the click's server timestamp, which is what lets
// the application raise a window in response past xfwm4's focus-stealing
// prevention: to the window manager, the request then dates from a moment
// the user was interacting with the desktop.
func (d *daemon) invoke(b *banner, key string, t xproto.Timestamp, now time.Time) {
	ok, closed := d.q.Invoke(b.id, key, now)
	if !ok {
		// The banner showed the action, but a replacement since has taken
		// it away; the click does nothing.
		d.debugf("action %q on %d is no longer offered", key, b.id)
		return
	}
	d.debugf("action %q on %d", key, b.id)
	token := fmt.Sprintf("osxflow-notifyd-%d_TIME%d", b.id, t)
	if err := d.srv.EmitActivationToken(b.id, token); err != nil {
		d.lim.Printf("emit", "action %q on %d: %v", key, b.id, err)
	}
	if err := d.srv.EmitAction(b.id, key); err != nil {
		d.lim.Printf("emit", "action %q on %d: %v", key, b.id, err)
	}
	d.emitClosed(closed)
}

// updatePause holds every timeout while the pointer is over a banner.
func (d *daemon) updatePause() {
	now := time.Now()
	for _, b := range d.banners {
		if b.hover && !b.closing {
			d.q.Pause(now)
			return
		}
	}
	d.q.Resume(now)
}

// iconFor picks the image a banner shows, trying the sender's own image
// first, then the embedded icon of the application it came from, and
// finally a placeholder tile.
func (d *daemon) iconFor(n *notify.Notification) *image.RGBA {
	size := d.th.icon
	if n.Icon.Data != nil {
		return fit(n.Icon.Data, size)
	}
	if n.Icon.Path != "" {
		img, err := notify.LoadImageFile(n.Icon.Path)
		switch {
		case err == nil:
			return fit(img, size)
		case iconFallbackExpected(err):
			d.debugf("icon: %v", err)
		default:
			// Limited per path: a replaced notification loads it again each
			// time, and a volume popup is replaced once per keypress.
			d.lim.Printf("icon:"+n.Icon.Path, "icon: %v", err)
		}
	}
	if key := d.index.Lookup(n); key != "" {
		if img := d.set.At(key, size); img != nil {
			return img
		}
	}
	return d.fallback(n.Title(), size)
}

// iconFallbackExpected reports whether an image file failed to load for one
// of the everyday reasons -- it is not there, or it is an SVG, which is
// what most icon themes are made of -- after which falling back to another
// icon is simply the plan.
func iconFallbackExpected(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, image.ErrFormat)
}

// maxFallbacks bounds the placeholder cache. Every distinct sender name
// makes one, and a client inventing names would otherwise grow it without
// limit.
const maxFallbacks = 64

func (d *daemon) fallback(label string, size int) *image.RGBA {
	if img, ok := d.fallbacks[label]; ok {
		return img
	}
	if len(d.fallbacks) >= maxFallbacks {
		clear(d.fallbacks)
	}
	img := fit(icons.Fallback(label, icons.MasterSize, d.faces.fallback), size)
	d.fallbacks[label] = img
	return img
}

// fit scales an image into a square, keeping its proportions.
func fit(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	if b.Empty() || size <= 0 {
		return nil
	}
	w, h := size, size
	switch {
	case b.Dx() > b.Dy():
		h = max(size*b.Dy()/b.Dx(), 1)
	case b.Dy() > b.Dx():
		w = max(size*b.Dx()/b.Dy(), 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	at := image.Pt((size-w)/2, (size-h)/2)
	xdraw.CatmullRom.Scale(dst, image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}, src, b, xdraw.Src, nil)
	return dst
}
