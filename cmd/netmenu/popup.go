package main

// The menu window: made when the tray icon is clicked, destroyed when it
// closes. Nothing is kept around in between.

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log"
	"slices"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/netmgr"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xsurface"
)

type menu struct {
	win      xproto.Window
	colormap xproto.Colormap
	surf     *xsurface.Surface

	rows  []row
	tops  []float64
	hover int

	// pointerX and pointerY are where the pointer last was, so that the
	// highlight can follow a row that moves when the list changes under a
	// still pointer.
	pointerX, pointerY int

	x, y, w, h int

	grabbedPointer  bool
	grabbedKeyboard bool
}

// Grabs are retried for a moment: a tray may deliver the click while its
// own implicit grab from the button press is still in place.
const (
	grabAttempts = 50
	grabRetry    = 10 * time.Millisecond
)

// openMenu shows the menu, centred under x.
func (a *app) openMenu(centreX int) error {
	m := &menu{hover: -1, pointerX: -1, pointerY: -1, w: a.th.menuW}
	m.setRows(a, buildRows(&a.state))
	sw := int(a.screen.WidthInPixels)
	m.x = max(0, min(centreX-m.w/2, sw-m.w))
	m.y = a.menuTop() + int(a.th.gap)

	if err := m.create(a); err != nil {
		// Whatever create got as far as making would otherwise stay on the
		// server until netmenu exits.
		m.close(a)
		return err
	}
	a.menu = m
	if err := m.paint(a); err != nil {
		return err
	}
	a.scan()
	return nil
}

func (a *app) closeMenu() {
	if a.menu == nil {
		return
	}
	a.menu.close(a)
	a.menu = nil
}

func (m *menu) setRows(a *app, rows []row) {
	m.rows = rows
	var total float64
	m.tops, total = layout(rows, &a.th.heights, a.th.padY)
	m.h = int(total + 0.5)
}

func (m *menu) create(a *app) error {
	cmap, err := xproto.NewColormapId(a.conn)
	if err != nil {
		return fmt.Errorf("allocating a colormap id: %w", err)
	}
	// A 32-bit window in a 24-bit parent needs a colormap of its own.
	if cmapErr := xproto.CreateColormapChecked(a.conn, xproto.ColormapAllocNone,
		cmap, a.screen.Root, a.visual.id).Check(); cmapErr != nil {
		return fmt.Errorf("creating the colormap: %w", cmapErr)
	}
	m.colormap = cmap

	win, err := xproto.NewWindowId(a.conn)
	if err != nil {
		return fmt.Errorf("allocating a window id: %w", err)
	}
	// Override-redirect, like the dock's popup: a menu is placed and
	// dismissed by its owner, not decorated and focused by the window
	// manager. In mask-bit order: BackPixel, BorderPixel,
	// OverrideRedirect, EventMask, Colormap.
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	values := []uint32{
		0,
		0,
		1,
		uint32(xproto.EventMaskExposure | xproto.EventMaskButtonPress |
			xproto.EventMaskPointerMotion | xproto.EventMaskKeyPress),
		uint32(cmap),
	}
	if createErr := xproto.CreateWindowChecked(a.conn, a.visual.depth, win, a.screen.Root,
		geom.I16(m.x), geom.I16(m.y), geom.U16(m.w), geom.U16(m.h), 0,
		xproto.WindowClassInputOutput, a.visual.id, mask, values).Check(); createErr != nil {
		return fmt.Errorf("creating the menu window: %w", createErr)
	}
	// Only now: close destroys m.win, and a window that was never created
	// is not one to clean up.
	m.win = win
	if nameErr := nameWindow(a.conn, win, "netmenu"); nameErr != nil {
		log.Printf("warning: %v", nameErr)
	}

	if m.surf, err = xsurface.New(a.conn, win, a.visual.depth, m.w, m.h); err != nil {
		return err
	}
	if mapErr := xproto.MapWindowChecked(a.conn, win).Check(); mapErr != nil {
		return fmt.Errorf("mapping the menu: %w", mapErr)
	}
	if raiseErr := xproto.ConfigureWindowChecked(a.conn, win,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check(); raiseErr != nil {
		log.Printf("raising the menu: %v", raiseErr)
	}
	m.grab(a)
	return nil
}

// grab takes the pointer and the keyboard, so that a click anywhere
// arrives here and can dismiss the menu, and so can Escape.
//
// Failing costs only those two ways of dismissing it -- a second click on
// the icon still closes it -- which is why failure is logged rather than
// returned.
func (m *menu) grab(a *app) {
	status, err := retryGrab(func() (byte, error) {
		reply, grabErr := xproto.GrabPointer(a.conn, false, m.win,
			uint16(xproto.EventMaskButtonPress|xproto.EventMaskPointerMotion),
			xproto.GrabModeAsync, xproto.GrabModeAsync,
			xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
		if grabErr != nil {
			return 0, grabErr
		}
		if reply == nil {
			return 0, fmt.Errorf("no reply from the X server")
		}
		return reply.Status, nil
	})
	switch {
	case err != nil:
		log.Printf("grabbing the pointer for the menu: %v", err)
	case status != xproto.GrabStatusSuccess:
		log.Printf("grabbing the pointer for the menu: %s; clicking elsewhere will not close it", grabStatus(status))
	default:
		m.grabbedPointer = true
	}

	status, err = retryGrab(func() (byte, error) {
		reply, grabErr := xproto.GrabKeyboard(a.conn, false, m.win, xproto.TimeCurrentTime,
			xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
		if grabErr != nil {
			return 0, grabErr
		}
		if reply == nil {
			return 0, fmt.Errorf("no reply from the X server")
		}
		return reply.Status, nil
	})
	switch {
	case err != nil:
		log.Printf("grabbing the keyboard for the menu: %v", err)
	case status != xproto.GrabStatusSuccess:
		log.Printf("grabbing the keyboard for the menu: %s; Escape will not close it", grabStatus(status))
	default:
		m.grabbedKeyboard = true
	}
}

func retryGrab(grab func() (byte, error)) (byte, error) {
	for attempt := 1; ; attempt++ {
		status, err := grab()
		if err != nil {
			return 0, err
		}
		busy := status == xproto.GrabStatusAlreadyGrabbed || status == xproto.GrabStatusFrozen
		if !busy || attempt == grabAttempts {
			return status, nil
		}
		time.Sleep(grabRetry)
	}
}

// close releases what the menu holds on the server. It is safe on a menu
// that create only got part of the way through. Failures are logged: the
// menu is going away either way.
func (m *menu) close(a *app) {
	if m.grabbedKeyboard {
		if err := xproto.UngrabKeyboardChecked(a.conn, xproto.TimeCurrentTime).Check(); err != nil {
			log.Printf("releasing the keyboard grab: %v", err)
		}
		m.grabbedKeyboard = false
	}
	if m.grabbedPointer {
		if err := xproto.UngrabPointerChecked(a.conn, xproto.TimeCurrentTime).Check(); err != nil {
			log.Printf("releasing the pointer grab: %v", err)
		}
		m.grabbedPointer = false
	}
	if m.surf != nil {
		if err := m.surf.Close(); err != nil {
			log.Printf("releasing the menu surface: %v", err)
		}
		m.surf = nil
	}
	if m.win != 0 {
		if err := xproto.DestroyWindowChecked(a.conn, m.win).Check(); err != nil {
			log.Printf("destroying the menu: %v", err)
		}
		m.win = 0
	}
	if m.colormap != 0 {
		if err := xproto.FreeColormapChecked(a.conn, m.colormap).Check(); err != nil {
			log.Printf("freeing the menu colormap: %v", err)
		}
		m.colormap = 0
	}
}

// update redraws the open menu for a new state, resizing it when the rows
// no longer fit. On failure the menu cannot be drawn and should be closed.
func (m *menu) update(a *app) error {
	oldH := m.h
	m.setRows(a, buildRows(&a.state))
	m.hover = m.rowAt(a, m.pointerX, m.pointerY)
	if m.h != oldH {
		if err := xproto.ConfigureWindowChecked(a.conn, m.win,
			xproto.ConfigWindowHeight, []uint32{geom.U32(m.h)}).Check(); err != nil {
			return fmt.Errorf("resizing the menu: %w", err)
		}
		if err := m.surf.Close(); err != nil {
			log.Printf("releasing the old menu surface: %v", err)
		}
		m.surf = nil
		surf, err := xsurface.New(a.conn, m.win, a.visual.depth, m.w, m.h)
		if err != nil {
			return err
		}
		m.surf = surf
	}
	return m.paint(a)
}

func (m *menu) rowAt(a *app, x, y int) int {
	if x < 0 || x >= m.w || y < 0 || y >= m.h {
		return -1
	}
	return hit(m.rows, m.tops, &a.th.heights, float64(y))
}

func (m *menu) motion(a *app, x, y int) error {
	m.pointerX, m.pointerY = x, y
	row := m.rowAt(a, x, y)
	if row == m.hover {
		return nil
	}
	m.hover = row
	return m.paint(a)
}

// menuClick acts on a click. Coordinates are the menu window's, even for a
// click outside it, because the pointer grab is taken on the menu.
func (a *app) menuClick(e xproto.ButtonPressEvent) {
	m := a.menu
	x, y := int(e.EventX), int(e.EventY)
	if x < 0 || x >= m.w || y < 0 || y >= m.h {
		// Aimed at something else; the menu gets out of the way. The click
		// itself is not passed on, as with any menu.
		a.closeMenu()
		return
	}
	if e.Detail != xproto.ButtonIndex1 {
		return
	}
	i := m.rowAt(a, x, y)
	if i < 0 {
		return
	}
	r := m.rows[i]
	// A switch stays open to show itself flip, as on macOS; anything that
	// goes somewhere closes the menu.
	if r.act != actWifi && r.act != actVPN {
		a.closeMenu()
	}
	a.perform(&r)
}

// perform starts what a row does. NetworkManager's answer arrives as
// signals, which redraw the menu and the icon; the call itself only says
// whether the request was accepted.
func (a *app) perform(r *row) {
	switch r.act {
	case actNone:
	case actWifi:
		on := !r.on
		a.async(func(ctx context.Context) error { return a.nm.SetWifi(ctx, on) })
	case actJoin:
		profile, device := r.net.Saved, a.state.WifiDevice
		a.async(func(ctx context.Context) error { return a.nm.Activate(ctx, profile, device) })
	case actJoinOpen:
		ssid, device, ap := slices.Clone(r.net.Raw), a.state.WifiDevice, r.net.AP
		a.async(func(ctx context.Context) error { return a.nm.JoinOpen(ctx, ssid, device, ap) })
	case actVPN:
		v := r.vpn
		if v.On() {
			a.async(func(ctx context.Context) error { return a.nm.Deactivate(ctx, v.Active) })
		} else {
			a.async(func(ctx context.Context) error { return a.nm.Activate(ctx, v.Connection, "") })
		}
	case actSettings:
		app := &desktop.App{Name: "Network Connections", Argv: []string{"nm-connection-editor"}}
		if err := launch.SpawnDetached(app); err != nil {
			log.Printf("opening network settings: %v", err)
		}
	}
}

// scan asks for a fresh list of networks when the menu opens, no more than
// once every scanSpacing: NetworkManager refuses requests closer together
// than that anyway, and a refusal is only noise.
func (a *app) scan() {
	dev := a.state.WifiDevice
	if dev == "" || !a.state.WifiEnabled || time.Since(a.lastScan) < scanSpacing {
		return
	}
	a.lastScan = time.Now()
	a.async(func(ctx context.Context) error { return a.nm.RequestScan(ctx, dev) })
}

const scanSpacing = 30 * time.Second

func (m *menu) paint(a *app) error {
	th := a.th
	img := m.surf.Image()
	paint.Clear(img)
	paint.RoundRectOnClear(img, img.Bounds(), th.radius, colBg)
	paint.FillBlend(img, image.Rect(int(th.radius), 0, m.w-int(th.radius), max(int(th.scale), 1)), colEdge)
	for i := range m.rows {
		m.paintRow(a, img, i)
	}
	return m.surf.Flush()
}

func (m *menu) paintRow(a *app, img *image.RGBA, i int) {
	th, f := a.th, a.faces
	r := &m.rows[i]
	top := m.tops[i]
	h := r.kind.height(&th.heights)
	mid := top + h/2
	left, right := th.padX, float64(m.w)-th.padX

	if i == m.hover {
		inset := int(th.padX / 2)
		paint.RoundRect(img, image.Rect(inset, int(top), m.w-inset, int(top+h)), th.radius/2, colHover)
	}

	switch r.kind {
	case rowSeparator:
		y := int(mid)
		paint.FillBlend(img, image.Rect(int(left), y, int(right), y+max(int(th.scale), 1)), colSeparator)
	case rowSection:
		drawLabel(img, f.section, colDim, left, mid, r.label, right-left)
	case rowNote:
		drawLabel(img, f.text, colDim, left, mid, r.label, right-left)
	case rowAction:
		drawLabel(img, f.text, colText, left, mid, r.label, right-left)
	case rowToggle:
		drawSwitch(img, th, right-th.switchW, mid, r.on)
		drawLabel(img, f.bold, colText, left, mid, r.label, right-th.switchW-th.padX-left)
	case rowNetwork:
		glyphX, glyphY := drawBadge(img, th, left, mid, r.on)
		drawWifi(img, glyphX, glyphY, th.badgeGlyph, netmgr.Bars(r.net.Strength), colGlyph, colGlyphDim)
		end := right
		if r.net.Secured {
			fill(img, area(right-th.lock, mid-th.lock/2, th.lock, th.lock), colDim,
				lock(right-th.lock, mid-th.lock/2, th.lock))
			end = right - th.lock - th.padX/2
		}
		textX := left + th.badge + th.padX*0.7
		m.drawLabelDetail(a, img, textX, mid, r, end-textX)
	case rowVPN:
		glyphX, glyphY := drawBadge(img, th, left, mid, r.on)
		inset := th.badgeGlyph * 0.1
		s := th.badgeGlyph - 2*inset
		fill(img, area(glyphX+inset, glyphY+inset, s, s), colGlyph, lock(glyphX+inset, glyphY+inset, s))
		drawSwitch(img, th, right-th.switchW, mid, r.on)
		textX := left + th.badge + th.padX*0.7
		m.drawLabelDetail(a, img, textX, mid, r, right-th.switchW-th.padX-textX)
	}
}

func (m *menu) drawLabelDetail(a *app, img *image.RGBA, x, mid float64, r *row, width float64) {
	end := drawLabel(img, a.faces.text, colText, x, mid, r.label, width)
	if r.detail == "" || end == 0 {
		return
	}
	dx := float64(end) + a.th.padX/2
	drawLabel(img, a.faces.text, colDim, dx, mid, r.detail, x+width-dx)
}

// drawBadge paints the round badge in front of a network or VPN and
// returns where the glyph inside it goes.
func drawBadge(img *image.RGBA, th *theme, left, mid float64, on bool) (glyphX, glyphY float64) {
	col := colBadgeOff
	if on {
		col = colAccent
	}
	radius := th.badge / 2
	fill(img, area(left, mid-radius, th.badge, th.badge), col, circle(left+radius, mid, radius))
	return left + radius - th.badgeGlyph/2, mid - th.badgeGlyph/2
}

func drawSwitch(img *image.RGBA, th *theme, x0, mid float64, on bool) {
	w, h := th.switchW, th.switchH
	track := colSwitchOff
	if on {
		track = colAccent
	}
	fill(img, area(x0, mid-h/2, w, h), track, box(x0+w/2, mid, w/2, h/2, h/2))
	knob := x0 + h/2
	if on {
		knob = x0 + w - h/2
	}
	r := h/2 - th.scale*1.5
	fill(img, area(knob-r, mid-r, 2*r, 2*r), colKnob, circle(knob, mid, r))
}

// drawLabel draws one line of text vertically centred on mid, cut to
// width, and returns where it ended -- or 0 when there was no room.
func drawLabel(img *image.RGBA, face font.Face, col color.Color, x, mid float64, s string, width float64) int {
	if width < 1 || s == "" {
		return 0
	}
	return text.Draw(img, face, col, int(x), baseline(face, mid), s, int(width))
}

// baseline centres a face's ascent and descent on mid.
func baseline(face font.Face, mid float64) int {
	metrics := face.Metrics()
	return int(mid + float64(metrics.Ascent-metrics.Descent)/64/2 + 0.5)
}
