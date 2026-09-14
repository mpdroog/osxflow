package menu

// The open menu: made when a tool opens it, destroyed when it closes.
// Nothing is kept around in between.

import (
	"errors"
	"image"
	"image/color"
	"log"
	"slices"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/glyph"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
)

type popup struct {
	win  *Window
	rows []Row
	tops []float64

	hover, hoverButton int

	// pointerX and pointerY are where the pointer last was, so that the
	// highlight can follow a row that moves when the list changes under a
	// still pointer.
	pointerX, pointerY int

	// drag is the slider being dragged, or -1, and dragValue where it is.
	// While dragging, the slider shows dragValue rather than its Row's
	// Value, which lags behind by however long the tool's state takes to
	// catch up.
	drag      int
	dragValue float64

	grabbedPointer, grabbedKeyboard bool
}

// IsOpen reports whether a menu is showing.
func (h *Host) IsOpen() bool { return h.popup != nil }

// Open shows a menu of rows, centred under x and just below the panel,
// replacing any menu already open.
func (h *Host) Open(centreX int, rows []Row) error {
	h.Close()
	p := &popup{hover: -1, hoverButton: -1, pointerX: -1, pointerY: -1, drag: -1, rows: rows}
	var total float64
	p.tops, total = layout(rows, h.Theme)

	w, height := h.Theme.Width, int(total+0.5)
	x := max(0, min(centreX-w/2, int(h.Screen.WidthInPixels)-w))
	y := h.menuTop() + int(h.Theme.gap)
	win, err := h.NewWindow(x, y, w, height, "osxflow-menu", uint32(xproto.EventMaskExposure|
		xproto.EventMaskButtonPress|xproto.EventMaskButtonRelease|
		xproto.EventMaskPointerMotion|xproto.EventMaskKeyPress))
	if err != nil {
		return err
	}
	p.win = win
	if err := win.Map(); err != nil {
		win.Close()
		return err
	}
	h.popup = p
	p.grab(h)
	return h.paint()
}

// Close closes the open menu, if there is one. A slider being dragged gets
// its final value first.
func (h *Host) Close() {
	p := h.popup
	if p == nil {
		return
	}
	// Cleared before anything else, so a callback below that asks whether
	// the menu is open, or tries to update it, finds it gone.
	h.popup = nil
	if p.drag >= 0 {
		if slide := p.rows[p.drag].Slide; slide != nil {
			slide(p.dragValue, true)
		}
	}
	if p.grabbedKeyboard {
		if err := xproto.UngrabKeyboardChecked(h.Conn, xproto.TimeCurrentTime).Check(); err != nil {
			log.Printf("releasing the keyboard grab: %v", err)
		}
	}
	if p.grabbedPointer {
		if err := xproto.UngrabPointerChecked(h.Conn, xproto.TimeCurrentTime).Check(); err != nil {
			log.Printf("releasing the pointer grab: %v", err)
		}
	}
	p.win.Close()
}

// Update redraws the open menu with new rows, resizing it when they no
// longer fit. It does nothing when no menu is open. When the menu cannot
// be redrawn it is closed, and the error says why.
func (h *Host) Update(rows []Row) error {
	p := h.popup
	if p == nil {
		return nil
	}
	p.rows = rows
	var total float64
	p.tops, total = layout(rows, h.Theme)
	if p.drag >= len(rows) || (p.drag >= 0 && rows[p.drag].Kind != Slider) {
		// The list changed shape under the drag; what was being dragged is
		// gone.
		p.drag = -1
	}
	p.hover, p.hoverButton = p.at(h, p.pointerX, p.pointerY)
	if height := int(total + 0.5); height != p.win.H {
		if err := p.win.Resize(p.win.W, height); err != nil {
			h.Close()
			return err
		}
	}
	if err := h.paint(); err != nil {
		h.Close()
		return err
	}
	return nil
}

// Handle acts on an X event for the open menu, and ignores the rest. The
// error is a failure to draw; the menu is still usable.
func (h *Host) Handle(ev xgb.Event) error {
	p := h.popup
	if p == nil {
		return nil
	}
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Window == p.win.ID {
			return p.win.Surf.Copy()
		}
	case xproto.MotionNotifyEvent:
		return h.motion(int(e.EventX), int(e.EventY))
	case xproto.ButtonPressEvent:
		return h.press(e)
	case xproto.ButtonReleaseEvent:
		return h.release(e)
	case xproto.KeyPressEvent:
		if slices.Contains(h.escape, e.Detail) {
			h.Close()
		}
	}
	return nil
}

func (p *popup) at(h *Host, x, y int) (row, button int) {
	if x < 0 || y < 0 || x >= p.win.W || y >= p.win.H {
		return -1, -1
	}
	return hit(p.rows, p.tops, h.Theme, float64(x), float64(y))
}

func (h *Host) motion(x, y int) error {
	p := h.popup
	p.pointerX, p.pointerY = x, y
	if p.drag >= 0 {
		v := sliderValue(h.Theme, float64(x))
		if v == p.dragValue {
			return nil
		}
		p.dragValue = v
		p.rows[p.drag].Slide(v, false)
		if h.popup != p {
			return nil
		}
		return h.paint()
	}
	row, button := p.at(h, x, y)
	if row == p.hover && button == p.hoverButton {
		return nil
	}
	p.hover, p.hoverButton = row, button
	return h.paint()
}

// press acts on a button press. Coordinates are the menu window's, even
// for a press outside it, because the pointer grab is taken on the menu.
func (h *Host) press(e xproto.ButtonPressEvent) error {
	p := h.popup
	x, y := int(e.EventX), int(e.EventY)
	if x < 0 || y < 0 || x >= p.win.W || y >= p.win.H {
		// Aimed at something else; the menu gets out of the way. The click
		// itself is not passed on, as with any menu.
		h.Close()
		return nil
	}
	row, button := p.at(h, x, y)
	if row < 0 {
		return nil
	}
	// A copy: the callbacks below may update or close the menu, replacing
	// or dropping p.rows.
	r := p.rows[row]

	switch e.Detail {
	case xproto.ButtonIndex1:
	case xproto.ButtonIndex4, xproto.ButtonIndex5:
		// The wheel moves a slider it is over, and nothing else.
		if r.Kind == Slider {
			step := sliderStep
			if e.Detail == xproto.ButtonIndex5 {
				step = -step
			}
			r.Slide(clamp01(r.Value+step), true)
		}
		return nil
	default:
		return nil
	}

	switch r.Kind {
	case Slider:
		p.drag, p.dragValue = row, sliderValue(h.Theme, float64(x))
		r.Slide(p.dragValue, false)
		if h.popup != p {
			return nil
		}
		return h.paint()
	case Transport:
		r.Buttons[button].Click()
		return nil
	case Toggle, Header, Section, Note, Item, Separator, Action, kindCount:
	}
	if !r.KeepOpen {
		h.Close()
	}
	r.Click()
	return nil
}

func (h *Host) release(e xproto.ButtonReleaseEvent) error {
	p := h.popup
	if p.drag < 0 || e.Detail != xproto.ButtonIndex1 {
		return nil
	}
	row, v := p.drag, p.dragValue
	p.drag = -1
	p.rows[row].Slide(v, true)
	if h.popup != p {
		return nil
	}
	return h.paint()
}

// grab takes the pointer and the keyboard, so that a click anywhere
// arrives here and can dismiss the menu, and so can Escape.
//
// Failing costs only those two ways of dismissing it -- a second click on
// the icon still closes it -- which is why failure is logged rather than
// returned.
func (p *popup) grab(h *Host) {
	status, err := retryGrab(func() (byte, error) {
		reply, grabErr := xproto.GrabPointer(h.Conn, false, p.win.ID,
			uint16(xproto.EventMaskButtonPress|xproto.EventMaskButtonRelease|xproto.EventMaskPointerMotion),
			xproto.GrabModeAsync, xproto.GrabModeAsync,
			xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
		if grabErr != nil {
			return 0, grabErr
		}
		if reply == nil {
			return 0, errors.New("no reply from the X server")
		}
		return reply.Status, nil
	})
	switch {
	case err != nil:
		log.Printf("grabbing the pointer for a menu: %v", err)
	case status != xproto.GrabStatusSuccess:
		log.Printf("grabbing the pointer for a menu: %s; clicking elsewhere will not close it", grabStatus(status))
	default:
		p.grabbedPointer = true
	}

	status, err = retryGrab(func() (byte, error) {
		reply, grabErr := xproto.GrabKeyboard(h.Conn, false, p.win.ID, xproto.TimeCurrentTime,
			xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
		if grabErr != nil {
			return 0, grabErr
		}
		if reply == nil {
			return 0, errors.New("no reply from the X server")
		}
		return reply.Status, nil
	})
	switch {
	case err != nil:
		log.Printf("grabbing the keyboard for a menu: %v", err)
	case status != xproto.GrabStatusSuccess:
		log.Printf("grabbing the keyboard for a menu: %s; Escape will not close it", grabStatus(status))
	default:
		p.grabbedKeyboard = true
	}
}

func (h *Host) paint() error {
	p := h.popup
	t := h.Theme
	img := p.win.Surf.Image()
	paint.Clear(img)
	paint.RoundRectOnClear(img, img.Bounds(), t.radius, colBg)
	paint.FillBlend(img, image.Rect(int(t.radius), 0, p.win.W-int(t.radius), max(int(t.Scale), 1)), colEdge)
	for i := range p.rows {
		h.paintRow(img, i)
	}
	return p.win.Surf.Flush()
}

func (h *Host) paintRow(img *image.RGBA, i int) {
	p, t, f := h.popup, h.Theme, h.faces
	r := &p.rows[i]
	top := p.tops[i]
	height := t.height(r.Kind)
	mid := top + height/2
	left, right := t.padX, float64(p.win.W)-t.padX

	if i == p.hover && r.hovers() {
		inset := int(t.padX / 2)
		paint.RoundRect(img, image.Rect(inset, int(top), p.win.W-inset, int(top+height)), t.radius/2, colHover)
	}

	switch r.Kind {
	case Separator:
		y := int(mid)
		paint.FillBlend(img, image.Rect(int(left), y, int(right), y+max(int(t.Scale), 1)), colSeparator)
	case Section:
		drawLabel(img, f.section, colDim, left, mid, r.Label, right-left)
	case Note:
		drawLabel(img, f.text, colDim, left, mid, r.Label, right-left)
	case Action:
		drawLabel(img, f.text, colText, left, mid, r.Label, right-left)
	case Header:
		detailW := 0.0
		if r.Detail != "" {
			detailW = float64(text.Width(f.text, r.Detail))
			drawLabel(img, f.text, colDim, right-detailW, mid, r.Detail, detailW+1)
		}
		drawLabel(img, f.bold, colText, left, mid, r.Label, right-left-detailW-t.padX/2)
	case Toggle:
		drawSwitch(img, t, right-t.switchW, mid, r.On)
		drawLabel(img, f.bold, colText, left, mid, r.Label, right-t.switchW-t.padX-left)
	case Item:
		h.paintItem(img, r, left, right, mid)
	case Slider:
		v := r.Value
		if i == p.drag {
			v = p.dragValue
		}
		drawSlider(img, t, r.Glyph, left, mid, clamp01(v))
	case Transport:
		for b, cx := range buttonCentres(t, len(r.Buttons)) {
			btn := &r.Buttons[b]
			if i == p.hover && b == p.hoverButton {
				radius := t.button / 2
				glyph.Fill(img, glyph.Area(cx-radius, mid-radius, t.button, t.button), colHover, glyph.Circle(cx, mid, radius))
			}
			col := colText
			if btn.Click == nil {
				col = colDim
			}
			if btn.Glyph != nil {
				btn.Glyph(img, cx-t.buttonGlyph/2, mid-t.buttonGlyph/2, t.buttonGlyph, col)
			}
		}
	case kindCount:
	}
}

func (h *Host) paintItem(img *image.RGBA, r *Row, left, right, mid float64) {
	t, f := h.Theme, h.faces
	radius := t.badge / 2
	badge := colBadgeOff
	if r.On {
		badge = colAccent
	}
	glyph.Fill(img, glyph.Area(left, mid-radius, t.badge, t.badge), badge, glyph.Circle(left+radius, mid, radius))
	if r.Glyph != nil {
		r.Glyph(img, left+radius-t.badgeGlyph/2, mid-t.badgeGlyph/2, t.badgeGlyph, colGlyph)
	}

	end := right
	switch r.Trailing {
	case TrailingLock:
		x0, y0 := right-t.lock, mid-t.lock/2
		glyph.Fill(img, glyph.Area(x0, y0, t.lock, t.lock), colDim, glyph.Lock(x0, y0, t.lock))
		end = x0 - t.padX/2
	case TrailingSwitch:
		drawSwitch(img, t, right-t.switchW, mid, r.On)
		end = right - t.switchW - t.padX
	case TrailingNone:
	}

	x := left + t.badge + t.padX*0.7
	width := end - x
	labelEnd := drawLabel(img, f.text, colText, x, mid, r.Label, width)
	if r.Detail == "" || labelEnd == 0 {
		return
	}
	dx := float64(labelEnd) + t.padX/2
	drawLabel(img, f.text, colDim, dx, mid, r.Detail, x+width-dx)
}

func drawSwitch(img *image.RGBA, t *Theme, x0, mid float64, on bool) {
	w, h := t.switchW, t.switchH
	track := colSwitchOff
	if on {
		track = colAccent
	}
	glyph.Fill(img, glyph.Area(x0, mid-h/2, w, h), track, glyph.Box(x0+w/2, mid, w/2, h/2, h/2))
	knob := x0 + h/2
	if on {
		knob = x0 + w - h/2
	}
	r := h/2 - t.Scale*1.5
	glyph.Fill(img, glyph.Area(knob-r, mid-r, 2*r, 2*r), colKnob, glyph.Circle(knob, mid, r))
}

func drawSlider(img *image.RGBA, t *Theme, symbol Glyph, left, mid, value float64) {
	if symbol != nil {
		symbol(img, left, mid-t.sliderGlyph/2, t.sliderGlyph, colText)
	}
	lo, hi := sliderTrack(t)
	knobR := t.knob / 2
	start, end := lo-knobR, hi+knobR
	th := t.trackH
	glyph.Fill(img, glyph.Area(start, mid-th/2, end-start, th), colTrack,
		glyph.Box((start+end)/2, mid, (end-start)/2, th/2, th/2))

	knobX := lo + (hi-lo)*value
	// The filled part is drawn only once it is longer than it is tall: a
	// shorter pill would bulge past where it should end, and the knob
	// covers it anyway.
	if knobX-start > th {
		glyph.Fill(img, glyph.Area(start, mid-th/2, knobX-start, th), colTrackFill,
			glyph.Box((start+knobX)/2, mid, (knobX-start)/2, th/2, th/2))
	}
	glyph.Fill(img, glyph.Area(knobX-knobR, mid-knobR, 2*knobR, 2*knobR), colKnobEdge, glyph.Circle(knobX, mid, knobR))
	glyph.Fill(img, glyph.Area(knobX-knobR, mid-knobR, 2*knobR, 2*knobR), colKnob, glyph.Circle(knobX, mid, knobR-t.Scale))
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
