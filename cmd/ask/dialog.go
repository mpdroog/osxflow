package main

// The dialog window: one managed X window, drawn once per keystroke.
//
// Managed, not override-redirect, and that is the whole reason this is a
// separate program from the launcher rather than another mode of it. A
// launcher is a transient overlay that takes the keyboard by grabbing it,
// which under labwc is a thicket -- an X grab does not see clicks on
// Wayland-native surfaces, and a stranded one breaks unrelated tools. A
// dialog is a real window: the compositor frames it, places it, gives it
// the keyboard and takes it back, and none of that machinery is needed.

import (
	"errors"
	"fmt"
	"image"
	"image/color"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/keybind"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xmon"
)

// errCancelled is a dialog the user dismissed. It is not a failure, and
// main turns it into the exit status that says so.
var errCancelled = errors.New("cancelled")

// The keysyms that are not characters, from keysymdef.h.
const (
	ksBackSpace = 0xff08
	ksReturn    = 0xff0d
	ksEscape    = 0xff1b
	ksUp        = 0xff52
	ksDown      = 0xff54
	ksKPEnter   = 0xff8d
	ksKP0       = 0xffb0
	ksKP9       = 0xffb9
)

// The palette mirrors internal/menu's, so a dialog looks like it belongs
// to the same desktop as the menus it is summoned beside.
var (
	colBg    = color.RGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xf4}
	colText  = color.RGBA{R: 0xea, G: 0xea, B: 0xf0, A: 0xff}
	colDim   = color.RGBA{R: 0x9a, G: 0x9a, B: 0xa8, A: 0xff}
	colField = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x14}
	colSel   = color.RGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0xcc}
	colCaret = color.RGBA{R: 0xea, G: 0xea, B: 0xf0, A: 0xff}
)

// Layout, in logical pixels before the display scale.
const (
	baseWidth   = 420
	basePad     = 16
	baseLineH   = 18
	baseFieldH  = 30
	baseRowH    = 26
	baseGap     = 8
	baseTitlePt = 13
	baseTextPt  = 12
	baseHintPt  = 10
	maxRows     = 8
)

type metrics struct {
	scale             float64
	width, pad, lineH int
	fieldH, rowH, gap int
	radius            float64
}

func newMetrics(scale float64) *metrics {
	px := func(v float64) int { return int(v*scale + 0.5) }
	return &metrics{
		scale: scale, width: px(baseWidth), pad: px(basePad), lineH: px(baseLineH),
		fieldH: px(baseFieldH), rowH: px(baseRowH), gap: px(baseGap), radius: 6 * scale,
	}
}

type faces struct{ regular, bold, hint font.Face }

func loadFaces(scale float64) (*faces, error) {
	reg, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, fmt.Errorf("loading a font: %w", err)
	}
	bold, _, err := text.Load(text.BoldCandidates)
	if err != nil {
		bold = reg
	}
	r, err := text.Face(reg, baseTextPt*scale)
	if err != nil {
		return nil, err
	}
	b, err := text.Face(bold, baseTitlePt*scale)
	if err != nil {
		return nil, err
	}
	h, err := text.Face(reg, baseHintPt*scale)
	if err != nil {
		return nil, err
	}
	return &faces{regular: r, bold: b, hint: h}, nil
}

type dialog struct {
	xu   *xgbutil.XUtil
	host *menu.Host
	win  *menu.Window
	m    *Model
	f    *faces
	mt   *metrics

	title  string
	prompt []string
	rows   int // rows of list drawn, fixed for the window's life
}

// show puts the dialog on screen and returns what was accepted.
func show(m *Model, title, prompt string, scaleOverride float64) (string, error) {
	xu, err := xgbutil.NewConn()
	if err != nil {
		return "", fmt.Errorf("connecting to the X server: %w", err)
	}
	defer xu.Conn().Close()
	keybind.Initialize(xu)

	host, err := menu.NewHost(xu, scaleOverride, baseWidth)
	if err != nil {
		return "", err
	}
	defer host.Release()

	d := &dialog{xu: xu, host: host, m: m, mt: newMetrics(host.Theme.Scale), title: title}
	if d.f, err = loadFaces(d.mt.scale); err != nil {
		return "", err
	}
	d.prompt = text.Wrap(d.f.regular, prompt, d.mt.width-2*d.mt.pad, 6)
	// Room for the add row too: it appears the moment anything is typed,
	// and a window that grew by a row on the first keystroke would jump
	// under the pointer.
	n := len(m.Shown())
	if m.Mode == ModeList && m.Editable {
		n++
	}
	d.rows = min(n, maxRows)

	if err := d.open(); err != nil {
		return "", err
	}
	defer d.win.Close()
	return d.loop()
}

// height is the window's, worked out from what it has to show. It is
// computed once: filtering changes how many rows *match*, not how many the
// window has room for, so the box does not jump about as you type.
func (d *dialog) height() int {
	h := 2 * d.mt.pad
	if d.title != "" {
		h += d.mt.lineH + d.mt.gap/2
	}
	h += len(d.prompt) * d.mt.lineH
	if d.m.Mode != ModeMessage {
		h += d.mt.gap + d.mt.fieldH
	}
	if d.rows > 0 {
		h += d.mt.gap + d.rows*d.mt.rowH
	}
	return h + d.mt.gap + d.mt.lineH
}

func (d *dialog) open() error {
	w, h := d.mt.width, d.height()
	mon := xmon.Active(d.host.Conn, d.host.Screen)
	x, y := mon.X+(mon.W-w)/2, mon.Y+(mon.H-h)/3

	win, err := d.host.NewManagedWindow(x, y, w, h, "ask",
		uint32(xproto.EventMaskKeyPress|xproto.EventMaskExposure|xproto.EventMaskStructureNotify))
	if err != nil {
		return err
	}
	d.win = win
	// DIALOG is deliberate: wlroots refuses the keyboard to an unmanaged
	// surface that declares one of the menu/notification types, and this
	// window is useless without it.
	if err := win.SetWindowType("_NET_WM_WINDOW_TYPE_DIALOG"); err != nil {
		return err
	}
	if err := win.Map(); err != nil {
		return err
	}
	return d.draw()
}

func (d *dialog) loop() (string, error) {
	for {
		ev, xerr := d.host.Conn.WaitForEvent()
		if xerr != nil {
			continue
		}
		if ev == nil {
			return "", errors.New("the connection to the X server closed")
		}
		switch e := ev.(type) {
		case xproto.ExposeEvent:
			if e.Count == 0 {
				if err := d.win.Surf.Copy(); err != nil {
					return "", err
				}
			}
		case xproto.KeyPressEvent:
			done, answer, err := d.key(e)
			if err != nil {
				return "", err
			}
			if done {
				return answer, nil
			}
			if err := d.draw(); err != nil {
				return "", err
			}
		case xproto.ClientMessageEvent:
			// The frame's close button.
			return "", errCancelled
		}
	}
}

// key applies one press. done says the dialog is finished.
func (d *dialog) key(e xproto.KeyPressEvent) (done bool, answer string, err error) {
	shift := e.State&xproto.ModMaskShift > 0
	ctrl := e.State&xproto.ModMaskControl > 0
	ks := keybind.KeysymGet(d.xu, e.Detail, 0)
	if shift {
		if s := keybind.KeysymGet(d.xu, e.Detail, 1); s != 0 {
			ks = s
		}
	}

	if ctrl {
		switch keybind.KeysymGet(d.xu, e.Detail, 0) {
		case 'u':
			d.m.Clear()
		case 'c', 'g':
			return true, "", errCancelled
		case 'p':
			d.m.Up()
		case 'n':
			d.m.Down()
		}
		return false, "", nil
	}

	switch ks {
	case ksEscape:
		return true, "", errCancelled
	case ksReturn, ksKPEnter:
		if a, ok := d.m.Answer(); ok {
			return true, a, nil
		}
		// Nothing to accept: an empty answer would be indistinguishable
		// from Cancel to the caller, so refuse rather than lie.
		return false, "", nil
	case ksBackSpace:
		d.m.Backspace()
	case ksUp:
		d.m.Up()
	case ksDown:
		d.m.Down()
	default:
		if r, ok := keysymRune(ks); ok {
			d.m.Insert(string(r))
		}
	}
	return false, "", nil
}

func keysymRune(ks xproto.Keysym) (rune, bool) {
	switch {
	case ks >= 0x20 && ks <= 0x7e, ks >= 0xa0 && ks <= 0xff:
		return rune(ks), true
	case ks >= ksKP0 && ks <= ksKP9:
		return rune('0' + (ks - ksKP0)), true
	case ks >= 0x01000100 && ks <= 0x0110ffff:
		return rune(ks - 0x01000000), true
	}
	return 0, false
}

func (d *dialog) draw() error {
	img := d.win.Surf.Image()
	mt, f := d.mt, d.f
	paint.Clear(img)
	paint.RoundRectOnClear(img, img.Bounds(), mt.radius, colBg)

	x := mt.pad
	inner := mt.width - 2*mt.pad
	y := mt.pad

	if d.title != "" {
		y += baseline(f.bold, mt.lineH)
		text.Draw(img, f.bold, colText, x, y, d.title, inner)
		y += mt.lineH - baseline(f.bold, mt.lineH) + mt.gap/2
	}
	for _, line := range d.prompt {
		y += baseline(f.regular, mt.lineH)
		text.Draw(img, f.regular, colDim, x, y, line, inner)
		y += mt.lineH - baseline(f.regular, mt.lineH)
	}

	if d.m.Mode != ModeMessage {
		y += mt.gap
		field := image.Rect(x, y, x+inner, y+mt.fieldH)
		paint.RoundRect(img, field, mt.radius/1.5, colField)
		typed := d.m.Text()
		tx := x + mt.pad/2
		tb := y + (mt.fieldH+capHeight(f.regular))/2
		w := text.Draw(img, f.regular, colText, tx, tb, typed, inner-mt.pad)
		// The caret is always drawn. Typing always goes here, whichever
		// row is highlighted, and hiding the caret while a row was
		// selected made the field read as a decoration you could not
		// reach -- which is exactly how it was reported.
		cw := max(int(mt.scale), 1)
		paint.FillBlend(img, image.Rect(w+2, y+mt.pad/3, w+2+cw, y+mt.fieldH-mt.pad/3), colCaret)
		if typed == "" {
			text.Draw(img, f.regular, colDim, w+2+cw+mt.pad/3, tb, d.placeholder(), inner-mt.pad)
		}
		y += mt.fieldH
	}

	if d.rows > 0 {
		y += mt.gap
		rows := d.m.Rows()
		for i := 0; i < d.rows && i < len(rows); i++ {
			top := y + i*mt.rowH
			if i == d.m.Selected() {
				paint.RoundRect(img, image.Rect(x, top, x+inner, top+mt.rowH), mt.radius/1.5, colSel)
			}
			col := colText
			if rows[i].Add {
				// Dim unless it is the one that would be accepted, so the
				// offer to create something reads as an offer.
				if i != d.m.Selected() {
					col = colDim
				}
			}
			rb := top + (mt.rowH+capHeight(f.regular))/2
			text.Draw(img, f.regular, col, x+mt.pad/2, rb, rows[i].Label, inner-mt.pad)
		}
		y += d.rows * mt.rowH
	}

	y += mt.gap + baseline(f.hint, mt.lineH)
	text.Draw(img, f.hint, colDim, x, y, d.hint(), inner)
	return d.win.Surf.Flush()
}

// placeholder says what the empty field is for, which is the other half
// of the same report: a grey bar with nothing in it explains nothing.
func (d *dialog) placeholder() string {
	if d.m.Mode == ModeList && d.m.Editable {
		return "type to filter, or name a new one"
	}
	if d.m.Mode == ModeList {
		return "type to filter"
	}
	return ""
}

func (d *dialog) hint() string {
	if d.m.Mode == ModeMessage {
		return "Enter or Esc to close"
	}
	if d.rows > 0 {
		return "↑↓ choose  ·  Enter accept  ·  Esc cancel"
	}
	return "Enter accept  ·  Esc cancel"
}

// baseline is where a line of this face sits inside a row of height h.
func baseline(f font.Face, h int) int {
	m := f.Metrics()
	return (h + (m.Ascent - m.Descent).Round()) / 2
}

func capHeight(f font.Face) int {
	m := f.Metrics()
	return (m.Ascent - m.Descent).Round()
}
