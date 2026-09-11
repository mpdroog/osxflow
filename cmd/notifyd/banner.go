package main

// The life of a banner: appearing, being changed, being hovered and
// clicked, and going away.

import (
	"fmt"
	"image"
	"math"
	"os"
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

// sync makes the banners on screen match the queue: new ones appear,
// changed ones are redrawn, closed ones fade, and everything slides into
// its place in the stack.
func (d *daemon) sync() {
	shown := d.q.Shown()
	live := make(map[uint32]bool, len(shown))
	top := d.area.Min.Y + d.th.marginT
	restX := d.area.Max.X - d.th.marginR - d.th.width - d.th.winPadX

	for i := range shown {
		s := &shown[i]
		live[s.ID] = true
		b := d.banners[s.ID]
		switch {
		case b == nil:
			created, err := d.newBanner(s, restX, top)
			if err != nil {
				fmt.Fprintln(os.Stderr, "notifyd:", err)
				continue
			}
			b = created
		case b.rev != s.Rev:
			d.update(b, s)
		}
		if b.restX != restX {
			b.restX = restX
			d.place(b)
		}
		b.y.Target = float64(top - d.th.winPadY)
		top += b.lay.plate.Dy() + d.th.gap
	}
	for id, b := range d.banners {
		if !live[id] && !b.closing {
			d.startClose(b)
		}
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
		return nil, err
	}
	b.win = win
	b.surf, err = xsurface.New(d.conn, win, d.visual.depth, b.lay.size.X, b.lay.size.Y)
	if err != nil {
		xproto.DestroyWindow(d.conn, win)
		return nil, err
	}
	d.banners[b.id] = b
	d.byWin[win] = b

	// Drawn before mapping, so the first Expose has a finished frame to
	// copy rather than an empty window to show.
	d.render(b)
	xproto.MapWindow(d.conn, win)
	d.raise(win)
	return b, nil
}

// update redraws a banner whose notification was replaced.
func (d *daemon) update(b *banner, s *notify.Shown) {
	b.rev, b.n = s.Rev, s.Notification
	b.icon = d.iconFor(&b.n)
	old := b.lay.size
	b.lay = measure(d.th, d.faces, &b.n)
	if b.lay.size != old {
		if err := d.resize(b); err != nil {
			fmt.Fprintln(os.Stderr, "notifyd:", err)
			return
		}
	}
	d.render(b)
}

// resize fits a banner's window and surface to a new layout.
func (d *daemon) resize(b *banner) error {
	b.surf.Close()
	d.resizeWindow(b.win, b.lay.size.X, b.lay.size.Y)
	surf, err := xsurface.New(d.conn, b.win, d.visual.depth, b.lay.size.X, b.lay.size.Y)
	if err != nil {
		return err
	}
	b.surf = surf
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

func (d *daemon) destroy(b *banner) {
	if b.surf != nil {
		b.surf.Close()
	}
	xproto.DestroyWindow(d.conn, b.win)
	delete(d.banners, b.id)
	delete(d.byWin, b.win)
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
			d.destroy(b) // deleting during range is allowed
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
		d.logf("drawing banner %d: %v", b.id, err)
	}
}

// handle applies one X event.
func (d *daemon) handle(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if b := d.byWin[e.Window]; b != nil && e.Count == 0 {
			if err := b.surf.Copy(); err != nil {
				d.logf("%v", err)
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
		if b := d.byWin[e.Window]; b != nil && e.State != xproto.VisibilityUnobscured && d.coveredByOther(b) {
			d.raise(b.win)
		}

	case xproto.PropertyNotifyEvent:
		if e.Window == d.screen.Root && e.Atom == d.atoms.workarea {
			d.area = d.workArea()
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
	if ok {
		d.logf("action %q on %d", key, b.id)
		token := fmt.Sprintf("osxflow-notifyd-%d_TIME%d", b.id, t)
		if err := d.srv.EmitActivationToken(b.id, token); err != nil {
			d.logf("%v", err)
		}
		if err := d.srv.EmitAction(b.id, key); err != nil {
			d.logf("%v", err)
		}
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
		if err == nil {
			return fit(img, size)
		}
		d.logf("icon: %v", err)
	}
	if key := d.index.Lookup(n); key != "" {
		if img := d.set.At(key, size); img != nil {
			return img
		}
	}
	return d.fallback(n.Title(), size)
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
