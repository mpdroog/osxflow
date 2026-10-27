package wl

// A virtual pointer: zwlr_virtual_pointer_v1.
//
// It is for tests. The dock is summoned by the pointer reaching a bottom
// edge, and the only way to check that end to end -- through the
// compositor, XWayland and the dock's own strips -- is to put a pointer
// there the way a mouse would.

import "errors"

// Requests, zwlr_virtual_pointer_manager_v1 and zwlr_virtual_pointer_v1.
const (
	vptrMgrCreate      = 0 // create_virtual_pointer(seat, new_id)
	vptrMotionAbsolute = 1 // motion_absolute(time, x, y, x_extent, y_extent)
	vptrFrame          = 4
	vptrDestroy        = 8
	vptrMgrVersion     = 1
)

// Pointer is a virtual pointer on the seat.
type Pointer struct {
	c  *Conn
	id uint32
}

// VirtualPointer creates a pointer.
func (c *Conn) VirtualPointer() (*Pointer, error) {
	c.mu.Lock()
	mgr, seat := c.vptrMgr, c.seat
	c.mu.Unlock()
	if mgr == 0 {
		return nil, errors.New("the compositor does not offer zwlr_virtual_pointer_manager_v1")
	}
	id := c.alloc()
	if err := c.send(mgr, vptrMgrCreate, argUint(seat), argUint(id)); err != nil {
		return nil, err
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	return &Pointer{c: c, id: id}, nil
}

// MoveTo puts the pointer at x,y in a layout w by h -- the bounding box of
// every monitor, in logical pixels -- and waits until the compositor has
// acted on it.
func (p *Pointer) MoveTo(x, y, w, h uint32) error {
	if err := p.c.send(p.id, vptrMotionAbsolute,
		argUint(0), argUint(x), argUint(y), argUint(w), argUint(h)); err != nil {
		return err
	}
	if err := p.c.send(p.id, vptrFrame); err != nil {
		return err
	}
	return p.c.roundtrip()
}

// Close destroys the pointer.
func (p *Pointer) Close() error {
	return p.c.send(p.id, vptrDestroy)
}
