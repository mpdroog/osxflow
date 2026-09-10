package main

// Drawing the dock.
//
// The whole window is repainted on every frame rather than tracking
// damaged rectangles. It is under a megabyte, the frame takes well under a
// millisecond, and invalidation bugs are the most tedious class of UI bug
// there is.

import (
	"image"
	"image/color"

	"github.com/jezek/xgb/xproto"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/icons"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
)

// faces holds the two text sizes the dock draws at, plus the oversized one
// used for placeholder tiles.
type faces struct {
	tooltip  font.Face
	popup    font.Face
	fallback font.Face
}

func loadFaces(th *theme) (*faces, error) {
	parsed, _, err := text.Load(text.Candidates)
	if err != nil {
		return nil, err
	}
	out := &faces{}
	for _, spec := range []struct {
		dst  *font.Face
		size float64
	}{
		{&out.tooltip, th.tooltipTextPt},
		{&out.popup, th.popupTextPt},
		// Sized for a letter on a full-resolution placeholder tile, which
		// is always drawn at the icon master size and scaled down with
		// everything else.
		{&out.fallback, 58},
	} {
		face, err := text.Face(parsed, spec.size)
		if err != nil {
			out.close()
			return nil, err
		}
		*spec.dst = face
	}
	return out, nil
}

func (f *faces) close() {
	for _, face := range []font.Face{f.tooltip, f.popup, f.fallback} {
		if face == nil {
			continue
		}
		// Closing a face frees a cache; there is nothing to do about a
		// failure and nothing that depends on it having worked.
		if err := face.Close(); err != nil {
			_ = err
		}
	}
}

// paint draws one frame of the dock.
func (d *dockApp) paint() error {
	img := d.surf.Image()
	paint.Clear(img)

	centre := float64(d.winW) / 2
	d.places = dock.Layout(d.items, &d.th.m, centre, d.cursorX, d.zoom.Value)

	width := dock.Width(d.items, d.places, &d.th.m)
	left := centre - width/2
	top, bottom := d.th.panelTop(), d.th.panelBottom()

	panel := image.Rect(int(left+0.5), int(top+0.5), int(left+width+0.5), int(bottom+0.5))
	// Straight after Clear, so the plate is drawn onto transparency and
	// needs no blend.
	paint.RoundRectOnClear(img, panel, d.th.m.Radius, colPanel)

	// A hairline of light along the top edge. Without it the plate reads as
	// a flat rectangle lying on the wallpaper rather than a pane above it.
	edge := image.Rect(panel.Min.X+int(d.th.m.Radius), panel.Min.Y,
		panel.Max.X-int(d.th.m.Radius), panel.Min.Y+max(int(d.th.scale), 1))
	paint.FillBlend(img, edge, colPanelEdge)

	baseline := d.th.baselineY()
	for i := range d.items {
		d.drawItem(img, i, baseline)
	}
	if d.hover >= 0 && d.hover < len(d.items) && d.items[d.hover].Kind != dock.KindSeparator {
		d.drawTooltip(img, d.hover, baseline)
	}
	return d.surf.Flush()
}

func (d *dockApp) drawItem(img *image.RGBA, i int, baseline float64) {
	item := &d.items[i]
	place := d.places[i]

	if item.Kind == dock.KindSeparator {
		// A short vertical hairline, centred on the icon band: the macOS
		// divider between applications and stacks.
		h := d.th.m.Icon * 0.62
		x := int(place.CentreX)
		w := max(int(d.th.scale), 1)
		paint.FillBlend(img, image.Rect(x, int(baseline-d.th.m.Icon/2-h/2), x+w, int(baseline-d.th.m.Icon/2+h/2)),
			colSeparator)
		return
	}

	// The drawn size is rounded onto the magnification ladder rather than to
	// the nearest pixel, because every size drawn is a size that gets
	// cached; see Metrics.Quantise.
	size := d.th.m.Quantise(place.Size)
	if size <= 0 {
		return
	}
	resting := size == int(d.th.m.Icon+0.5)
	if icon := d.iconAt(item, size, resting); icon != nil {
		paint.Over(img, icon, int(place.CentreX-float64(size)/2+0.5), int(baseline)-size)
	}

	if item.Windows > 0 {
		cy := baseline + d.th.m.DotGap + d.th.m.DotRadius
		paint.Circle(img, int(place.CentreX), int(cy), d.th.m.DotRadius, colDot)
	}
}

// drawTooltip floats the hovered item's name above its icon.
func (d *dockApp) drawTooltip(img *image.RGBA, i int, baseline float64) {
	item := &d.items[i]
	place := d.places[i]

	label := item.Name
	if label == "" {
		return
	}
	textW := float64(text.Width(d.faces.tooltip, label))
	boxW := textW + 2*d.th.tooltipPadX
	boxBottom := baseline - place.Size - d.th.tooltipGap
	boxTop := boxBottom - d.th.tooltipH

	// Centred on the icon, but never off the edge of the window.
	left := place.CentreX - boxW/2
	if left < 0 {
		left = 0
	}
	if left+boxW > float64(d.winW) {
		left = float64(d.winW) - boxW
	}

	box := image.Rect(int(left+0.5), int(boxTop+0.5), int(left+boxW+0.5), int(boxBottom+0.5))
	paint.RoundRect(img, box, d.th.tooltipRadius, colTooltipBg)
	paint.FillBlend(img, image.Rect(box.Min.X+int(d.th.tooltipRadius), box.Min.Y,
		box.Max.X-int(d.th.tooltipRadius), box.Min.Y+max(int(d.th.scale), 1)), colTooltipEdge)

	text.DrawCentred(img, d.faces.tooltip, colTooltipText,
		box.Min.X+box.Dx()/2, int(boxBottom-d.th.tooltipBaseSub), label, int(boxW))
}

// iconAt returns an item's artwork at a size.
//
// resting says whether this is the size the icon sits at when nothing is
// magnified, which is true for every icon outside the cursor's influence
// and therefore for most of them, most of the time. Those get the better
// filter, because they are what the dock looks like when it is standing
// still; the handful being magnified get the faster one.
func (d *dockApp) iconAt(item *dock.Item, size int, resting bool) *image.RGBA {
	if resting {
		if img := d.set.At(item.Icon, size); img != nil {
			return img
		}
	} else if img := d.set.Magnified(item.Icon, size); img != nil {
		return img
	}
	return d.fallbackAt(item.Name, size)
}

func (d *dockApp) fallbackAt(name string, size int) *image.RGBA {
	if img, ok := d.fallbacks[fallbackKey{name: name, size: size}]; ok {
		return img
	}
	master, ok := d.fallbacks[fallbackKey{name: name, size: icons.MasterSize}]
	if !ok {
		master = icons.Fallback(name, icons.MasterSize, d.faces.fallback)
		d.fallbacks[fallbackKey{name: name, size: icons.MasterSize}] = master
	}
	if size == icons.MasterSize {
		return master
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), master, master.Bounds(), xdraw.Src, nil)
	d.fallbacks[fallbackKey{name: name, size: size}] = dst
	return dst
}

// roundedPanel draws the popup's plate. It is called on a surface that has
// just been cleared, so the plate needs no blend.
func roundedPanel(img *image.RGBA, r image.Rectangle, radius, scale float64, bg, edge color.RGBA) {
	paint.RoundRectOnClear(img, r, radius, bg)
	paint.FillBlend(img, image.Rect(r.Min.X+int(radius), r.Min.Y,
		r.Max.X-int(radius), r.Min.Y+max(int(scale), 1)), edge)
}

// close releases everything the dock holds.
func (d *dockApp) close() {
	if d.popup != nil {
		d.popup.close(d)
	}
	if d.surf != nil {
		d.surf.Close()
	}
	if d.win != 0 {
		xproto.DestroyWindow(d.conn, d.win)
	}
	if d.trigger != 0 {
		xproto.DestroyWindow(d.conn, d.trigger)
	}
	if d.colormap != 0 {
		xproto.FreeColormap(d.conn, d.colormap)
	}
	if d.faces != nil {
		d.faces.close()
	}
	if d.server != nil {
		// The Server was built on this connection rather than owning one,
		// so Close only drops its reference; there is nothing to report.
		if err := d.server.Close(); err != nil {
			_ = err
		}
	}
	d.conn.Close()
}
