package wl

import (
	"encoding/binary"
	"testing"
)

// newTestBar is a bar registered the way NewBar registers one, without a
// compositor to configure it.
func newTestBar(c *Conn, layer, surface uint32) *Bar {
	b := &Bar{c: c, surface: surface, layer: layer, scale120: scaleUnit}
	c.bars[layer] = b
	c.surfaces[surface] = b
	return b
}

// words encodes a body of 32-bit arguments.
func words(vs ...uint32) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// fixedArg encodes logical pixels as wl_fixed_t, the 24.8 the pointer
// reports positions in.
func fixedArg(v float64) uint32 {
	//nolint:gosec // building the protocol's signed fixed point for a test
	return uint32(int32(v * 256))
}

// next is the event the connection emitted, or a failure.
func next(t *testing.T, c *Conn) Event {
	t.Helper()
	select {
	case e := <-c.events:
		return e
	default:
		t.Fatal("no event was emitted")
		return nil
	}
}

func TestFixedDecodesTheProtocolsFixedPoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   uint32
		want float64
	}{
		{"zero", fixedArg(0), 0},
		{"whole", fixedArg(37), 37},
		{"fraction", fixedArg(12.5), 12.5},
		{"negative", fixedArg(-3.25), -3.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fixed(words(tc.in)); got != tc.want {
				t.Errorf("fixed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeviceSizeRoundsToWholePixels(t *testing.T) {
	c := newTestConn(t)
	b := newTestBar(c, 10, 11)
	b.w, b.h = 2560, 29
	for _, tc := range []struct {
		name         string
		scale120     uint32
		wantW, wantH int
	}{
		{"unscaled", 120, 2560, 29},
		// 29 logical at 1.5 is 43.5 real pixels, which has to become a
		// whole number of rows one way or the other.
		{"one and a half", 180, 3840, 44},
		{"double", 240, 5120, 58},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.scale120 = tc.scale120
			w, h := b.deviceSize()
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("deviceSize = %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestConfigureRecordsTheSizeAndAnnouncesIt(t *testing.T) {
	c := newTestConn(t)
	b := newTestBar(c, 10, 11)

	c.layerSurface(10, shellSurfaceConfigure, words(7, 3440, 29))

	if !b.configured {
		t.Error("the bar was not marked configured")
	}
	if b.w != 3440 || b.h != 29 {
		t.Errorf("size = %dx%d, want 3440x29", b.w, b.h)
	}
	e, ok := next(t, c).(Configure)
	if !ok {
		t.Fatalf("emitted %T, want Configure", e)
	}
	if e.Bar != b || e.Width != 3440 || e.Height != 29 {
		t.Errorf("Configure = %+v, want the bar at 3440x29", e)
	}
}

// A configure that changes nothing still has to be acknowledged, but it is
// not news: redrawing on it would repaint the bar every time the
// compositor repeats itself.
func TestRepeatedConfigureIsNotAnnouncedTwice(t *testing.T) {
	c := newTestConn(t)
	newTestBar(c, 10, 11)

	c.layerSurface(10, shellSurfaceConfigure, words(7, 3440, 29))
	<-c.events
	c.layerSurface(10, shellSurfaceConfigure, words(8, 3440, 29))

	select {
	case e := <-c.events:
		t.Errorf("emitted %T for an unchanged configure", e)
	default:
	}
}

func TestClosedMarksTheBarFinished(t *testing.T) {
	c := newTestConn(t)
	b := newTestBar(c, 10, 11)

	c.layerSurface(10, shellSurfaceClosed, nil)

	if !b.closed {
		t.Error("the bar was not marked closed")
	}
	if _, ok := next(t, c).(Closed); !ok {
		t.Error("no Closed event")
	}
	if _, err := b.Frame(); err == nil {
		t.Error("Frame on a closed bar returned no error")
	}
	if err := b.Flush(); err == nil {
		t.Error("Flush on a closed bar returned no error")
	}
}

func TestPreferredScaleIsFollowed(t *testing.T) {
	c := newTestConn(t)
	b := newTestBar(c, 10, 11)
	c.fracs[12] = b

	c.fractionalScale(12, fracPreferredScale, words(180))

	if got := b.Scale(); got != 1.5 {
		t.Errorf("Scale = %v, want 1.5", got)
	}
	e, ok := next(t, c).(Scale)
	if !ok {
		t.Fatalf("emitted %T, want Scale", e)
	}
	if e.Scale != 1.5 {
		t.Errorf("Scale event = %v, want 1.5", e.Scale)
	}

	// The compositor repeats the scale whenever the surface is remapped;
	// rebuilding every font for that would be a stutter for nothing.
	c.fractionalScale(12, fracPreferredScale, words(180))
	select {
	case e := <-c.events:
		t.Errorf("emitted %T for an unchanged scale", e)
	default:
	}
}

func TestBufferReleaseMakesItDrawableAgain(t *testing.T) {
	c := newTestConn(t)
	buf := &buffer{id: 20, busy: true}
	c.buffers[20] = buf

	c.bufferReleased(20, bufferRelease)

	if buf.busy {
		t.Error("the buffer is still marked busy after its release")
	}
}

func TestPointerReportsDevicePixels(t *testing.T) {
	c := newTestConn(t)
	b := newTestBar(c, 10, 11)
	b.scale120 = 180 // a monitor at 1.5

	// enter carries a serial and the surface before the coordinates.
	c.pointerEvent(pointerEnter, words(1, 11, fixedArg(100), fixedArg(10)))
	e, ok := next(t, c).(Motion)
	if !ok {
		t.Fatalf("emitted %T, want Motion", e)
	}
	if e.Bar != b || e.X != 150 || e.Y != 15 {
		t.Errorf("Motion = %d,%d on %v, want 150,15 on the bar", e.X, e.Y, e.Bar)
	}

	c.pointerEvent(pointerMotion, words(0, fixedArg(200), fixedArg(4)))
	m, ok := next(t, c).(Motion)
	if !ok {
		t.Fatalf("emitted %T, want Motion", m)
	}
	if m.X != 300 || m.Y != 6 {
		t.Errorf("Motion = %d,%d, want 300,6", m.X, m.Y)
	}

	// The button event carries no position of its own: it is a click
	// wherever the last motion left the pointer.
	c.pointerEvent(pointerButton, words(2, 0, ButtonLeft, 1))
	btn, ok := next(t, c).(Button)
	if !ok {
		t.Fatalf("emitted %T, want Button", btn)
	}
	if btn.X != 300 || btn.Y != 6 || btn.Button != ButtonLeft || !btn.Pressed {
		t.Errorf("Button = %+v, want a left press at 300,6", btn)
	}

	c.pointerEvent(pointerLeave, words(3, 11))
	if _, ok := next(t, c).(Leave); !ok {
		t.Error("no Leave event")
	}
	// Motion after a leave belongs to no bar and must not be reported as
	// if the pointer were still on this one.
	c.pointerEvent(pointerMotion, words(0, fixedArg(1), fixedArg(1)))
	select {
	case e := <-c.events:
		t.Errorf("emitted %T after the pointer left", e)
	default:
	}
}

// The read loop must never block on a caller that has stopped listening:
// a client that stops reading its socket deadlocks the compositor too.
func TestEmitDropsRatherThanBlocking(t *testing.T) {
	c := newTestConn(t)
	for range eventBuffer + 10 {
		c.emit(Focus{})
	}
	if got := c.Dropped(); got != 10 {
		t.Errorf("Dropped = %d, want 10", got)
	}
	if len(c.events) != eventBuffer {
		t.Errorf("buffered %d events, want %d", len(c.events), eventBuffer)
	}
}
