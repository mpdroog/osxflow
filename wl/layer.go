package wl

// The bar: a layer-shell surface anchored across the top of one monitor.
//
// This is what XWayland cannot do. An X11 dock under labwc gets its strut
// honoured -- maximised windows do stop below it -- but it is one window on
// one X screen spanning every monitor, drawn at one scale, and on a desk
// with a 4K display at 1.5 and an ultrawide at 1 that is the wrong picture
// on at least one of them. zwlr_layer_shell_v1 is the protocol written for
// this exact job: a surface per output, anchored to its edges, with an
// exclusive zone the compositor subtracts from every other window's idea of
// the screen.
//
// Scale is handled the modern way rather than the integer way. Asking for
// buffer scale 2 on a 1.5 output and letting the compositor shrink the
// result is what most bars do and it is visibly soft; wp_fractional_scale_v1
// says the output really wants 1.5, and wp_viewporter lets a buffer of
// exactly that many device pixels be presented at the logical size. Text
// then lands on the pixel grid the monitor has.

import (
	"errors"
	"fmt"
	"image"
)

// Layers, from zwlr_layer_shell_v1. Top is above ordinary windows and
// below fullscreen ones, which is where a menu bar belongs.
const (
	layerBackground = 0
	layerBottom     = 1
	layerTop        = 2
	layerOverlay    = 3
)

// Anchor bits, from zwlr_layer_surface_v1.
const (
	anchorTop    = 1
	anchorBottom = 2
	anchorLeft   = 4
	anchorRight  = 8
)

// scaleUnit is what wp_fractional_scale_v1 counts in: the preferred scale
// arrives as 120ths, so 180 means 1.5.
const scaleUnit = 120

// Bar is a strip anchored across the top of one monitor.
//
// Its picture is in device pixels -- Frame's image is scale times the
// logical size -- because that is the only size at which drawing is sharp.
// A caller lays out in the same pixels it draws in and asks Scale what to
// multiply its own metrics by.
type Bar struct {
	c *Conn

	surface  uint32
	layer    uint32
	viewport uint32
	frac     uint32

	output    uint32
	namespace string

	// w and h are logical pixels, from the compositor's configure. h is
	// what was asked for; w is however wide the monitor is.
	w, h int

	// scale120 is the output's preferred scale in 120ths. It starts at
	// one because that is what a surface gets until the compositor says
	// otherwise, which it does only once the surface is mapped.
	scale120 uint32

	img  *image.RGBA
	pool *pool
	bufs []*buffer

	// pointer is where the pointer last was on this bar, in device
	// pixels. wl_pointer's button event carries no position of its own:
	// it is a click wherever the last motion left the cursor.
	pointer image.Point

	configured bool
	closed     bool
}

// NewBar anchors a bar across the top of one monitor and waits for the
// compositor to say how wide it is.
//
// height is logical pixels, and is reserved: the compositor takes it out of
// the area it gives every other window, so maximised windows stop below the
// bar. namespace is what the compositor calls the surface in its own
// configuration and logs.
func (c *Conn) NewBar(output uint32, height int, namespace string) (*Bar, error) {
	if height <= 0 {
		return nil, fmt.Errorf("a bar must have a positive height, got %d", height)
	}
	c.mu.Lock()
	compositor, shell := c.compositor, c.layerShell
	viewporter, fracMgr := c.viewporter, c.fracMgr
	c.mu.Unlock()
	if compositor == 0 {
		return nil, errors.New("the compositor offers no wl_compositor")
	}
	if shell == 0 {
		return nil, errors.New("the compositor does not offer zwlr_layer_shell_v1, " +
			"so a bar cannot reserve its strip")
	}

	b := &Bar{
		c: c, output: output, namespace: namespace,
		h: height, scale120: scaleUnit,
		surface: c.alloc(),
	}
	if err := c.send(compositor, compositorCreateSurface, argUint(b.surface)); err != nil {
		return nil, err
	}
	b.layer = c.alloc()
	if err := c.send(shell, shellGetLayerSurface,
		argUint(b.layer), argUint(b.surface), argUint(output),
		argUint(layerTop), argString(namespace)); err != nil {
		return nil, err
	}

	// Registered before the first commit so that the configure, which the
	// compositor sends in reply to it, finds the bar it belongs to.
	c.mu.Lock()
	c.bars[b.layer] = b
	c.surfaces[b.surface] = b
	c.mu.Unlock()

	// Width 0 means "as wide as the anchors make it". Anchored left, right
	// and top, that is the whole width of this monitor, and it stays right
	// when the mode changes.
	for _, m := range []struct {
		opcode uint16
		args   []arg
	}{
		{shellSurfaceSetSize, []arg{argUint(0), argUint(uint32(height))}}, //nolint:gosec // height is checked positive above
		{shellSurfaceSetAnchor, []arg{argUint(anchorTop | anchorLeft | anchorRight)}},
		{shellSurfaceSetExclusiveZone, []arg{argInt(height)}},
		// A bar takes no keyboard focus: clicking it must not take the
		// caret out of whatever the user is typing in.
		{shellSurfaceSetKeyboardInteractivity, []arg{argUint(0)}},
	} {
		if err := c.send(b.layer, m.opcode, m.args...); err != nil {
			return nil, err
		}
	}

	if viewporter != 0 && fracMgr != 0 {
		b.frac = c.alloc()
		if err := c.send(fracMgr, fracMgrGetFractionalScale, argUint(b.frac), argUint(b.surface)); err != nil {
			return nil, err
		}
		b.viewport = c.alloc()
		if err := c.send(viewporter, viewporterGetViewport, argUint(b.viewport), argUint(b.surface)); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.fracs[b.frac] = b
		c.mu.Unlock()
	}

	// The first commit is what asks for a configure. Nothing may be
	// attached before that configure has been acknowledged.
	if err := c.send(b.surface, surfaceCommit); err != nil {
		return nil, err
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	configured := b.configured
	c.mu.Unlock()
	if !configured {
		return nil, errors.New("the compositor never configured the bar")
	}
	return b, nil
}

// Size is the bar in logical pixels: as wide as its monitor, as tall as it
// asked to be.
func (b *Bar) Size() (width, height int) {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	return b.w, b.h
}

// Scale is what to multiply logical pixels by to get the device pixels
// Frame's image is drawn in: 1.5 on a 4K monitor set to 150%.
func (b *Bar) Scale() float64 {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	return float64(b.scale120) / scaleUnit
}

// Output is the wl_output the bar is on.
func (b *Bar) Output() uint32 { return b.output }

// deviceSize is the picture's size in real pixels.
func (b *Bar) deviceSize() (w, h int) {
	round := func(logical int) int {
		return (logical*int(b.scale120) + scaleUnit/2) / scaleUnit
	}
	return round(b.w), round(b.h)
}

// Frame is the image to draw this frame into, in device pixels.
//
// It is reallocated when the compositor changes the bar's width or the
// monitor's scale, so its bounds are the truth about how big the picture
// is and a caller should lay out against them rather than remembering.
func (b *Bar) Frame() (*image.RGBA, error) {
	b.c.mu.Lock()
	if b.closed {
		b.c.mu.Unlock()
		return nil, errors.New("the bar has been closed by the compositor")
	}
	w, h := b.deviceSize()
	same := b.img != nil && b.img.Bounds().Dx() == w && b.img.Bounds().Dy() == h
	b.c.mu.Unlock()

	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("the compositor configured the bar to %dx%d", w, h)
	}
	if same {
		return b.img, nil
	}
	if err := b.reallocate(w, h); err != nil {
		return nil, err
	}
	return b.img, nil
}

// reallocate throws away the old buffers and makes ones of the new size.
//
// The old pool is destroyed rather than resized: a resize can only grow a
// pool, and a monitor going from 4K to 1080p would leave the bar holding
// four times the memory it needs for the rest of the session.
func (b *Bar) reallocate(w, h int) error {
	if err := b.freeBuffers(); err != nil {
		return err
	}
	p, bufs, err := b.c.newPool(w, h)
	if err != nil {
		return err
	}
	b.c.mu.Lock()
	b.pool, b.bufs, b.img = p, bufs, image.NewRGBA(image.Rect(0, 0, w, h))
	for _, buf := range bufs {
		b.c.buffers[buf.id] = buf
	}
	b.c.mu.Unlock()

	// With a viewport the buffer's own size is only the resolution: the
	// destination is what the compositor lays out against, and it is the
	// logical size whatever the scale.
	if b.viewport != 0 {
		logicalW, logicalH := b.Size()
		if err := b.c.send(b.viewport, viewportSetDestination, argInt(logicalW), argInt(logicalH)); err != nil {
			return err
		}
	}
	return nil
}

// freeBuffers gives back the current pool and its buffers.
func (b *Bar) freeBuffers() error {
	b.c.mu.Lock()
	p, bufs := b.pool, b.bufs
	b.pool, b.bufs, b.img = nil, nil, nil
	for _, buf := range bufs {
		delete(b.c.buffers, buf.id)
	}
	b.c.mu.Unlock()
	if p == nil {
		return nil
	}
	for _, buf := range bufs {
		if err := b.c.send(buf.id, bufferDestroy); err != nil {
			return err
		}
	}
	if err := b.c.send(p.id, poolDestroy); err != nil {
		return err
	}
	return p.release()
}

// Flush puts what has been drawn into Frame's image on the screen.
func (b *Bar) Flush() error {
	b.c.mu.Lock()
	if b.closed {
		b.c.mu.Unlock()
		return errors.New("the bar has been closed by the compositor")
	}
	buf, img, p := b.free(), b.img, b.pool
	b.c.mu.Unlock()
	if img == nil || p == nil {
		return errors.New("Flush before Frame: there is nothing drawn to show")
	}
	if buf == nil {
		// Both buffers are still the compositor's. One roundtrip is
		// enough to collect the releases it has already sent.
		if err := b.c.roundtrip(); err != nil {
			return err
		}
		b.c.mu.Lock()
		buf = b.free()
		b.c.mu.Unlock()
		if buf == nil {
			// Not an error worth failing a frame over: the compositor is
			// simply behind, and the next paint will show this one's
			// content anyway.
			return nil
		}
	}

	p.write(buf, img.Pix)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	const x, y = 0, 0 // the whole buffer, from its top left
	if err := b.c.send(b.surface, surfaceAttach, argUint(buf.id), argInt(x), argInt(y)); err != nil {
		return err
	}
	// damage_buffer, not damage: the argument is in the buffer's own
	// pixels, which is the only one of the two that means anything when a
	// viewport is scaling the result.
	if err := b.c.send(b.surface, surfaceDamageBuffer, argInt(x), argInt(y), argInt(w), argInt(h)); err != nil {
		return err
	}
	if err := b.c.send(b.surface, surfaceCommit); err != nil {
		return err
	}
	b.c.mu.Lock()
	buf.busy = true
	b.c.mu.Unlock()
	return nil
}

// free is a buffer the compositor is not reading. The caller holds c.mu.
func (b *Bar) free() *buffer {
	for _, buf := range b.bufs {
		if !buf.busy {
			return buf
		}
	}
	return nil
}

// Close takes the bar off the screen and gives back everything it holds.
func (b *Bar) Close() error {
	b.c.mu.Lock()
	delete(b.c.bars, b.layer)
	delete(b.c.surfaces, b.surface)
	delete(b.c.fracs, b.frac)
	b.c.mu.Unlock()

	errs := []error{b.freeBuffers()}
	for _, m := range []struct {
		obj    uint32
		opcode uint16
	}{
		{b.frac, fracDestroy},
		{b.viewport, viewportDestroy},
		{b.layer, shellSurfaceDestroy},
		{b.surface, surfaceDestroy},
	} {
		if m.obj == 0 {
			continue
		}
		errs = append(errs, b.c.send(m.obj, m.opcode))
	}
	return errors.Join(errs...)
}

// -- events -----------------------------------------------------------------

// layerSurface handles the compositor's two words to a bar: how big it is,
// and that it is going away.
func (c *Conn) layerSurface(obj uint32, opcode uint16, body []byte) {
	c.mu.Lock()
	b := c.bars[obj]
	c.mu.Unlock()
	if b == nil {
		return
	}
	switch opcode {
	case shellSurfaceConfigure:
		if len(body) < 12 {
			return
		}
		serial := le32(body[0:4])
		w, h := int(le32(body[4:8])), int(le32(body[8:12]))
		c.mu.Lock()
		resized := w != b.w || h != b.h
		b.w, b.h, b.configured = w, h, true
		c.mu.Unlock()
		// Acknowledged whether or not anything changed: an unacknowledged
		// configure leaves the surface unmapped, which is a bar that never
		// appears and never says why.
		c.sendOrFail(obj, shellSurfaceAckConfigure, argUint(serial))
		if resized {
			c.emit(Configure{Bar: b, Width: w, Height: h})
		}
	case shellSurfaceClosed:
		c.mu.Lock()
		b.closed = true
		c.mu.Unlock()
		c.emit(Closed{Bar: b})
	}
}

// fractionalScale records the scale the monitor under this bar prefers.
func (c *Conn) fractionalScale(obj uint32, opcode uint16, body []byte) {
	if opcode != fracPreferredScale || len(body) < 4 {
		return
	}
	c.mu.Lock()
	b := c.fracs[obj]
	var changed bool
	if b != nil {
		if s := le32(body[0:4]); s != 0 && s != b.scale120 {
			b.scale120, changed = s, true
		}
	}
	c.mu.Unlock()
	if changed {
		c.emit(Scale{Bar: b, Scale: b.Scale()})
	}
}

// bufferRelease marks a frame as the client's again.
func (c *Conn) bufferReleased(obj uint32, opcode uint16) {
	if opcode != bufferRelease {
		return
	}
	c.mu.Lock()
	if buf := c.buffers[obj]; buf != nil {
		buf.busy = false
	}
	c.mu.Unlock()
}
