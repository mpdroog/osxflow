package wl

// The pointer, and everything else the compositor says to a bar.
//
// Under X11 these arrive as events on the connection every tool already
// has. Here they arrive on the same socket as everything else and are
// handed on as a channel, because the read loop must never be the thing
// that blocks: a client that stops reading its socket while the compositor
// is writing to it deadlocks them both.

import "image"

// ButtonLeft is BTN_LEFT from the kernel's input codes, which is what
// wl_pointer reports rather than a number of its own.
const ButtonLeft = 0x110

// Event is something the compositor said about a bar. The concrete types
// are the four things that actually change what is on screen.
type Event interface{ isEvent() }

// Configure is the compositor giving the bar its size, in logical pixels.
// It arrives when the bar is first mapped and whenever the monitor's mode
// or layout changes.
type Configure struct {
	Bar           *Bar
	Width, Height int
}

// Scale is the monitor under the bar preferring a different scale, as when
// it is moved to another screen or the user changes the setting.
type Scale struct {
	Bar   *Bar
	Scale float64
}

// Motion is the pointer moving over the bar. X and Y are in the device
// pixels of Frame's image, so they index the same picture the caller drew.
type Motion struct {
	Bar  *Bar
	X, Y int
}

// Leave is the pointer going somewhere else.
type Leave struct{ Bar *Bar }

// Button is a click on the bar, at the pointer's last position.
type Button struct {
	Bar     *Bar
	X, Y    int
	Button  uint32
	Pressed bool
}

// Focus is the focused window changing: a different one was activated,
// or the focused one closed. What is on the bar because of it -- the
// application's name -- is now out of date.
//
// It carries nothing. The answer is a question for Toplevels or Active,
// which report the state as it is when asked rather than as it was when
// the event was queued.
type Focus struct{}

// Monitor is a monitor appearing or going away: a screen switched on, a
// laptop undocked. What the set of monitors now is, is a question for
// Outputs.
type Monitor struct{}

// Closed is the compositor taking the bar away -- the monitor was
// unplugged, or it is shutting down. The Bar is finished; its Close is
// still worth calling, and nothing else on it is.
type Closed struct{ Bar *Bar }

func (Configure) isEvent() {}
func (Scale) isEvent()     {}
func (Motion) isEvent()    {}
func (Leave) isEvent()     {}
func (Button) isEvent()    {}
func (Closed) isEvent()    {}
func (Focus) isEvent()     {}
func (Monitor) isEvent()   {}

// eventBuffer is how many events are held for a caller that is busy. It is
// generous because the events that matter -- a click, a configure -- must
// not be the ones dropped, and motion arrives in bursts that a bar redraws
// only the last of.
const eventBuffer = 64

// Events is the stream of things that happened to this connection's bars.
//
// It is never closed: the connection failing is reported by the call that
// next touches it, and a caller selecting on this channel also has a
// timer, a bus and an X connection to listen to.
func (c *Conn) Events() <-chan Event { return c.events }

// emit hands an event to the caller, or drops it.
//
// Dropping is deliberate. The alternative is blocking the read loop until
// the caller catches up, which stops the connection being read at all --
// and the only events that can pile up fast enough to fill the buffer are
// pointer motion, where the newest one is the only one that matters and
// the dropped ones would have been overwritten anyway.
//
// The counter is atomic rather than under the mutex because emit is called
// from handlers that already hold it -- a mutex here would deadlock the
// read loop on the first dropped event.
func (c *Conn) emit(e Event) {
	select {
	case c.events <- e:
	default:
		c.dropped.Add(1)
	}
}

// Dropped is how many events have been discarded because the caller was
// not reading. It is diagnostic: a bar that feels unresponsive and a
// number here that climbs is a caller with a blocked event loop.
func (c *Conn) Dropped() int { return int(c.dropped.Load()) }

// seat binds a pointer once the seat says it has one.
//
// The capability can arrive more than once and can lose the pointer bit
// when the last mouse is unplugged; binding a second pointer on top of the
// first would leak the object and double every event.
func (c *Conn) seatEvent(opcode uint16, body []byte) {
	if opcode != seatCapabilities || len(body) < 4 {
		return
	}
	const capabilityPointer = 1
	if le32(body[0:4])&capabilityPointer == 0 {
		return
	}
	c.mu.Lock()
	if c.pointer != 0 || c.seat == 0 {
		c.mu.Unlock()
		return
	}
	id := c.nextID + 1
	c.nextID = id
	c.pointer = id
	seat := c.seat
	c.mu.Unlock()
	c.sendOrFail(seat, seatGetPointer, argUint(id))
}

// pointerEvent turns wl_pointer's events into Motion, Leave and Button.
func (c *Conn) pointerEvent(opcode uint16, body []byte) {
	switch opcode {
	case pointerEnter:
		if len(body) < 16 {
			return
		}
		surface := le32(body[4:8])
		c.mu.Lock()
		b := c.surfaces[surface]
		c.pointerOn = b
		c.mu.Unlock()
		if b != nil {
			c.emit(Motion{Bar: b, X: b.device(fixed(body[8:12])), Y: b.device(fixed(body[12:16]))})
		}
	case pointerLeave:
		c.mu.Lock()
		b := c.pointerOn
		c.pointerOn = nil
		c.mu.Unlock()
		if b != nil {
			c.emit(Leave{Bar: b})
		}
	case pointerMotion:
		if len(body) < 12 {
			return
		}
		c.mu.Lock()
		b := c.pointerOn
		c.mu.Unlock()
		if b != nil {
			p := image.Pt(b.device(fixed(body[4:8])), b.device(fixed(body[8:12])))
			c.mu.Lock()
			b.pointer = p
			c.mu.Unlock()
			c.emit(Motion{Bar: b, X: p.X, Y: p.Y})
		}
	case pointerButton:
		if len(body) < 16 {
			return
		}
		c.mu.Lock()
		b := c.pointerOn
		var p image.Point
		if b != nil {
			p = b.pointer
		}
		c.mu.Unlock()
		if b != nil {
			c.emit(Button{
				Bar: b, X: p.X, Y: p.Y,
				Button:  le32(body[8:12]),
				Pressed: le32(body[12:16]) == 1,
			})
		}
	}
}

// fixed reads wl_fixed_t, the protocol's 24.8 signed fixed point, as a
// float of logical pixels.
func fixed(b []byte) float64 {
	//nolint:gosec // the protocol's fixed point is a signed 32-bit word
	return float64(int32(le32(b))) / 256
}

// device turns a logical coordinate from the pointer into the device pixel
// of the same place in Frame's image.
func (b *Bar) device(logical float64) int {
	b.c.mu.Lock()
	scale := float64(b.scale120) / scaleUnit
	b.c.mu.Unlock()
	return int(logical*scale + 0.5)
}
