// Package wl is the smallest Wayland client that answers one question:
// which toplevel windows exist, and please focus that one.
//
// It is not a general Wayland binding and should not grow into one. It
// speaks exactly five interfaces -- wl_display, wl_registry, wl_seat,
// wl_output and zwlr_foreign_toplevel_management_v1 -- because those are
// what a dock and a launcher need to replace the EWMH properties labwc
// does not publish. wl_output is bound for one reason: to answer "which
// monitor is that window on", which is how a launcher decides which screen
// to open on when the pointer is somewhere else entirely.
// Nothing here draws: osxflow's windows are still X11 surfaces under
// XWayland, which works, and porting the rendering is a separate job.
//
// Why hand-rolled. go-wayland is archived and carries no wlr protocols,
// and every maintained binding wants cgo, which would cost the static
// musl binaries that are the whole point of building these in Go. The
// wire format is small enough to write out: a header of two uint32s, then
// arguments in declaration order. The one genuinely awkward part of
// Wayland -- passing file descriptors over SCM_RIGHTS, needed for wl_shm
// and keyboard maps -- does not arise, because none of the four
// interfaces below ever sends one.
package wl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// Wire format. Every message is:
//
//	uint32 object id
//	uint32 (length << 16) | opcode      -- length includes this header
//	arguments, in the order the protocol declares them
//
// Scalars are 4 bytes in host order (little-endian everywhere this runs).
// Strings are a uint32 length *including* the trailing NUL, the bytes, the
// NUL, then padding to the next multiple of 4. Arrays are the same without
// the NUL.
const headerLen = 8

// displayID is wl_display, which the server guarantees is object 1.
const displayID = 1

// Opcodes. Named rather than inlined because a wrong one produces a
// protocol error whose message points at the object, not the request, and
// that is a miserable thing to debug.
const (
	displayGetRegistry = 1 // wl_display.get_registry(new_id)
	displaySync        = 0 // wl_display.sync(new_id)

	registryBind = 0 // wl_registry.bind(name, interface, version, new_id)

	outputRelease = 3 // wl_output.release(), which exists from version 3

	toplevelHandleActivate = 4 // zwlr_foreign_toplevel_handle_v1.activate(seat)
	toplevelHandleClose    = 5
	toplevelHandleDestroy  = 7
)

// Requests the bar sends. Drawing needs five more interfaces than asking
// what is open does; see layer.go for why each one is here.
const (
	compositorCreateSurface = 0 // wl_compositor.create_surface(new_id)

	surfaceDestroy      = 0 // wl_surface.destroy()
	surfaceAttach       = 1 // wl_surface.attach(buffer, x, y)
	surfaceCommit       = 6
	surfaceDamageBuffer = 9 // wl_surface.damage_buffer(x, y, w, h)

	shmCreatePool    = 0 // wl_shm.create_pool(new_id, fd, size)
	poolCreateBuffer = 0 // wl_shm_pool.create_buffer(new_id, offset, w, h, stride, format)
	poolDestroy      = 1
	bufferDestroy    = 0

	shellGetLayerSurface = 0 // zwlr_layer_shell_v1.get_layer_surface(new_id, surface, output, layer, namespace)

	shellSurfaceSetSize                  = 0
	shellSurfaceSetAnchor                = 1
	shellSurfaceSetExclusiveZone         = 2
	shellSurfaceSetKeyboardInteractivity = 4
	shellSurfaceAckConfigure             = 6
	shellSurfaceDestroy                  = 7

	viewporterGetViewport  = 1 // wp_viewporter.get_viewport(new_id, surface)
	viewportDestroy        = 0
	viewportSetDestination = 2 // wp_viewport.set_destination(width, height)

	fracMgrGetFractionalScale = 1 // wp_fractional_scale_manager_v1.get_fractional_scale(new_id, surface)
	fracDestroy               = 0

	seatGetPointer = 0 // wl_seat.get_pointer(new_id)
)

// Event opcodes, per interface.
const (
	displayError    = 0
	displayDeleteID = 1

	registryGlobal       = 0
	registryGlobalRemove = 1

	callbackDone = 0

	managerToplevel = 0
	managerFinished = 1

	handleTitle       = 0
	handleAppID       = 1
	handleOutputEnter = 2
	handleOutputLeave = 3
	handleState       = 4
	handleDone        = 5
	handleClosed      = 6

	outputGeometry = 0
	outputName     = 4
)

// Event opcodes for the bar's interfaces.
const (
	seatCapabilities = 0

	pointerEnter  = 0
	pointerLeave  = 1
	pointerMotion = 2
	pointerButton = 3

	shellSurfaceConfigure = 0
	shellSurfaceClosed    = 1

	bufferRelease = 0

	fracPreferredScale = 0
)

// Versions the bar's globals are bound at. Each is the oldest that has
// what is used here, so that a compositor offering no more than that still
// works: layer shell 1 has the anchors and the exclusive zone, and
// wl_compositor 4 is the last version where attach's x and y are still
// allowed to be given (5 forbids anything but zero, which is what is sent
// anyway).
const (
	compositorVersion = 4
	shmVersion        = 1
	layerShellVersion = 1
	viewporterVersion = 1
	fracVersion       = 1
	seatVersion       = 1
)

// outputVersion is what we bind wl_output at. Version 4 is the first with
// the name event, which carries the connector name ("DP-8") -- the one
// string that also appears in XWayland's RandR monitor list, and so the
// only reliable way to say "that monitor" to a window we place through X.
const outputVersion = 4

// outputReleaseSince is the first wl_output version with a release
// request. Sending one to an older output would be a protocol error, which
// is fatal to the whole connection.
const outputReleaseSince = 3

// Toplevel state values from the protocol's state array.
const (
	stateMaximized = 0
	stateMinimized = 1
	stateActivated = 2
)

// Toplevel is one top-level window as the compositor describes it.
//
// The protocol is deliberately thin: there is a title, an app id, and a
// set of states, and that is all. In particular there is no process id --
// see Conn.Toplevels for what that costs.
type Toplevel struct {
	// ID is the Wayland object id of the handle. It is stable for the
	// lifetime of the window and unique among live windows, which is what
	// xwin.Window.ID needs.
	ID uint32

	AppID string
	Title string

	Activated bool
	Minimized bool

	// Outputs are the monitors the window is on, by wl_output object id. A
	// window straddling two monitors is on both, and one the compositor
	// has not placed yet is on none.
	Outputs []uint32

	// seen is set once the compositor has sent a done event for this
	// handle. Until then the window is half-described and reporting it
	// would show a dock entry with no name.
	seen bool
}

// Output is one monitor as the compositor describes it.
//
// Only the identity and the position are kept. The size is deliberately
// absent: wl_output reports the mode in physical pixels, which is not the
// logical size once the output is scaled, and a caller that wants a
// rectangle to place a window in should ask the display server it is
// placing the window through.
type Output struct {
	// ID is the wl_output object id, which is what a toplevel's
	// output_enter refers to.
	ID uint32

	// Name is the connector, "DP-8". It is empty when the compositor
	// offers only wl_output before version 4, which has no name event.
	Name string

	// X, Y is the top-left corner in the compositor's logical layout --
	// the same coordinate space XWayland's RandR monitors use, so the two
	// can be matched up when there is no name to match on.
	X, Y int

	// ver is the version this output was bound at, which decides whether
	// it can be released when the monitor is unplugged.
	ver uint32
}

// Conn is a connection to the compositor.
type Conn struct {
	c net.Conn

	// uc is c again, typed: passing a file descriptor to wl_shm needs
	// SCM_RIGHTS, which is a Unix socket operation rather than a write.
	uc *net.UnixConn

	mu        sync.Mutex
	nextID    uint32
	reg       uint32
	seat      uint32
	manager   uint32
	toplevels map[uint32]*Toplevel
	outputs   map[uint32]*Output

	// The globals a bar needs, all zero on a compositor that offers none
	// of them -- which is not an error until a bar is actually asked for.
	compositor uint32
	shm        uint32
	layerShell uint32
	viewporter uint32
	fracMgr    uint32
	pointer    uint32

	// Bars by the object id each kind of event arrives on, because a
	// Wayland event says only which object it is about.
	bars     map[uint32]*Bar // by zwlr_layer_surface_v1
	surfaces map[uint32]*Bar // by wl_surface, for pointer enter
	fracs    map[uint32]*Bar // by wp_fractional_scale_v1
	buffers  map[uint32]*buffer

	// pointerOn is the bar the pointer is over, from the last enter.
	pointerOn *Bar

	events  chan Event
	dropped atomic.Int64

	// outputNames maps a wl_registry global name to the wl_output object
	// bound for it, so that a monitor being unplugged -- the one global
	// that really does go away mid-session -- can be forgotten.
	outputNames map[uint32]uint32

	// order is toplevel ids oldest-activated first. The protocol gives no
	// stacking order at all, so this stands in for one: every time a
	// window becomes activated it moves to the end. That makes the last
	// entry the most recently focused window, which is the only property
	// callers actually rely on.
	order []uint32

	// err is the first fatal protocol error; every later call returns it.
	err  error
	done chan struct{}

	// pending counts in-flight wl_display.sync callbacks by object id.
	pending map[uint32]chan struct{}
}

// Dial connects to the compositor named by WAYLAND_DISPLAY, binds the
// globals we need and waits for the first batch of toplevels to arrive.
//
// It fails rather than degrades when the compositor has no
// zwlr_foreign_toplevel_management_v1: a dock that silently lists nothing
// is harder to diagnose than one that says the compositor cannot tell it
// what is open.
func Dial() (*Conn, error) {
	sock, err := socketPath()
	if err != nil {
		return nil, err
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connecting to the compositor at %s: %w", sock, err)
	}
	uc, _ := c.(*net.UnixConn) // always a Unix socket; nil only makes fd passing fail loudly
	conn := &Conn{
		c:           c,
		uc:          uc,
		nextID:      1, // 1 is wl_display; allocation starts at 2
		toplevels:   make(map[uint32]*Toplevel),
		outputs:     make(map[uint32]*Output),
		outputNames: make(map[uint32]uint32),
		pending:     make(map[uint32]chan struct{}),
		bars:        make(map[uint32]*Bar),
		surfaces:    make(map[uint32]*Bar),
		fracs:       make(map[uint32]*Bar),
		buffers:     make(map[uint32]*buffer),
		events:      make(chan Event, eventBuffer),
		done:        make(chan struct{}),
	}

	registry := conn.alloc()
	conn.reg = registry // set once, before readLoop starts, so reads need no lock
	if err := conn.send(displayID, displayGetRegistry, argUint(registry)); err != nil {
		c.Close()
		return nil, err
	}
	go conn.readLoop(registry)

	// Two roundtrips: the first delivers the globals, the second the
	// toplevels that binding the manager caused the compositor to send.
	if err := conn.roundtrip(); err != nil {
		c.Close()
		return nil, err
	}
	conn.mu.Lock()
	manager, seat := conn.manager, conn.seat
	conn.mu.Unlock()
	if manager == 0 {
		c.Close()
		return nil, errors.New("the compositor does not offer " +
			"zwlr_foreign_toplevel_management_v1, so no window list is available")
	}
	if seat == 0 {
		c.Close()
		return nil, errors.New("the compositor offers no wl_seat, so windows cannot be activated")
	}
	if err := conn.roundtrip(); err != nil {
		c.Close()
		return nil, err
	}
	return conn, nil
}

// socketPath resolves WAYLAND_DISPLAY the way the protocol specifies: an
// absolute value is used as-is, a bare name is relative to
// XDG_RUNTIME_DIR.
func socketPath() (string, error) {
	disp := os.Getenv("WAYLAND_DISPLAY")
	if disp == "" {
		return "", errors.New("WAYLAND_DISPLAY is not set: not a Wayland session")
	}
	if filepath.IsAbs(disp) {
		return disp, nil
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set, so " + disp + " cannot be resolved")
	}
	return filepath.Join(dir, disp), nil
}

// Toplevels returns the windows the compositor currently reports, oldest
// activation first.
//
// Two fields an X11 caller would expect are absent from the protocol and
// cannot be synthesised: there is no process id, and no window type. A
// dock that matched windows to .desktop files by _NET_WM_PID has to match
// on AppID here instead -- which on Wayland is usually the better key, as
// app_id is by convention the .desktop basename.
func (c *Conn) Toplevels() ([]Toplevel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	out := make([]Toplevel, 0, len(c.order))
	for _, id := range c.order {
		t, ok := c.toplevels[id]
		if !ok || !t.seen {
			continue
		}
		w := *t
		// The copy would otherwise share its backing array with the read
		// loop, which appends to it on every output_enter.
		w.Outputs = slices.Clone(t.Outputs)
		out = append(out, w)
	}
	return out, nil
}

// Active is the toplevel that has the keyboard, and whether anything
// does. A compositor with nothing focused -- the pointer on the desktop --
// is the false case, and is normal rather than an error.
func (c *Conn) Active() (Toplevel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.order) - 1; i >= 0; i-- {
		t, ok := c.toplevels[c.order[i]]
		if ok && t.seen && t.Activated {
			return *t, true
		}
	}
	return Toplevel{}, false
}

// Outputs returns every monitor the compositor has.
//
// They are sorted by position, which puts them left to right on a
// compositor that reports one. labwc does not: wl_output.geometry gives
// 0,0 for every monitor there, and the real layout position is in
// zxdg_output_manager_v1, which this client does not bind. The order is
// then the order the globals arrived in, which is stable within a session
// and means nothing across one. A caller that needs to know which monitor
// is which should use Name, as the bar does.
func (c *Conn) Outputs() []Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Output, 0, len(c.outputs))
	for _, o := range c.outputs {
		out = append(out, *o)
	}
	slices.SortFunc(out, func(a, b Output) int {
		if a.X != b.X {
			return a.X - b.X
		}
		return a.Y - b.Y
	})
	return out
}

// ActiveOutput returns the monitor the user is working on: the one holding
// the window that has the keyboard, or that had it last.
//
// "or had it last" is the whole subtlety. A launcher asks this question
// after it has appeared, and by then the compositor has taken focus away
// from the window the user was typing in and given it to the launcher's own
// surface -- which is not a toplevel and so is not in this list at all, so
// the answer would be "nothing is focused". The order slice remembers where
// focus came from, and that is the monitor the user is looking at.
//
// Minimized windows are skipped: one has no useful position, and on some
// compositors no output at all.
func (c *Conn) ActiveOutput() (Output, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Currently activated first, most recently activated second. The two
	// passes agree whenever anything is focused, and differ exactly in the
	// case above.
	for _, activatedOnly := range []bool{true, false} {
		for i := len(c.order) - 1; i >= 0; i-- {
			t, ok := c.toplevels[c.order[i]]
			if !ok || !t.seen || t.Minimized {
				continue
			}
			if activatedOnly && !t.Activated {
				continue
			}
			for _, id := range t.Outputs {
				if o, ok := c.outputs[id]; ok {
					return *o, true
				}
			}
		}
	}
	return Output{}, false
}

// Activate asks the compositor to focus a window, which is the request
// that makes this protocol usable where a plain listing would not be.
//
// It waits for the compositor to have processed the request rather than
// only writing it. The caller may be a one-shot tool -- the launcher
// hands the keyboard back on its way out -- and a request still sitting in
// the socket when the connection closes is a request that may never be
// acted on.
func (c *Conn) Activate(id uint32) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	_, ok := c.toplevels[id]
	seat := c.seat
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such window 0x%x (it may have closed)", id)
	}
	if err := c.send(id, toplevelHandleActivate, argUint(seat)); err != nil {
		return err
	}
	return c.roundtrip()
}

// Close asks a window to close itself.
func (c *Conn) Close(id uint32) error {
	c.mu.Lock()
	_, ok := c.toplevels[id]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such window 0x%x (it may have closed)", id)
	}
	return c.send(id, toplevelHandleClose)
}

// Disconnect closes the connection to the compositor.
func (c *Conn) Disconnect() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.c.Close()
}

func (c *Conn) alloc() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

// roundtrip sends wl_display.sync and waits for its callback, which the
// compositor emits only after everything queued before it has been sent.
func (c *Conn) roundtrip() error {
	cb := c.alloc()
	ch := make(chan struct{})
	c.mu.Lock()
	c.pending[cb] = ch
	c.mu.Unlock()

	if err := c.send(displayID, displaySync, argUint(cb)); err != nil {
		return err
	}
	select {
	case <-ch:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return err
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("the connection to the compositor closed")
		}
		return err
	}
}

// -- writing ----------------------------------------------------------------

type arg func(*[]byte)

func argUint(v uint32) arg {
	return func(b *[]byte) { *b = binary.LittleEndian.AppendUint32(*b, v) }
}

func argString(s string) arg {
	return func(b *[]byte) {
		*b = binary.LittleEndian.AppendUint32(*b, uint32(len(s)+1))
		*b = append(*b, s...)
		*b = append(*b, 0)
		for len(*b)%4 != 0 {
			*b = append(*b, 0)
		}
	}
}

// argInt appends one of the protocol's signed 32-bit arguments.
func argInt(v int) arg {
	return func(b *[]byte) {
		//nolint:gosec // the protocol's int is a signed 32-bit word, which is the truncation asked for
		*b = binary.LittleEndian.AppendUint32(*b, uint32(int32(v)))
	}
}

// le32 reads one of the protocol's 32-bit words out of an event body.
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func (c *Conn) send(obj uint32, opcode uint16, args ...arg) error {
	buf := make([]byte, headerLen, headerLen+32)
	for _, a := range args {
		a(&buf)
	}
	binary.LittleEndian.PutUint32(buf[0:4], obj)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf))<<16|uint32(opcode))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if _, err := c.c.Write(buf); err != nil {
		return fmt.Errorf("writing to the compositor: %w", err)
	}
	return nil
}

// sendFD is send with a file descriptor attached.
//
// Wayland passes descriptors out of band: the fd is not in the message at
// all, and the compositor takes the next one off the socket when it reads
// a request whose signature says there is one. So the message and its
// descriptor must go in a single sendmsg -- splitting them pairs the fd
// with whatever request happens to be next, which is a protocol error at
// best and a compositor reading the wrong file at worst.
func (c *Conn) sendFD(obj uint32, opcode uint16, fd int, args ...arg) error {
	buf := make([]byte, headerLen, headerLen+32)
	for _, a := range args {
		a(&buf)
	}
	binary.LittleEndian.PutUint32(buf[0:4], obj)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf))<<16|uint32(opcode))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.uc == nil {
		return errors.New("the compositor connection is not a Unix socket, so no memory can be shared")
	}
	rights := unix.UnixRights(fd)
	if _, _, err := c.uc.WriteMsgUnix(buf, rights, nil); err != nil {
		return fmt.Errorf("sending a file descriptor to the compositor: %w", err)
	}
	return nil
}

// -- reading ----------------------------------------------------------------

func (c *Conn) readLoop(registry uint32) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := c.c.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			buf = c.drain(buf, registry)
		}
		if err != nil {
			c.fail(fmt.Errorf("reading from the compositor: %w", err))
			return
		}
	}
}

// drain consumes every whole message in buf and returns the remainder.
func (c *Conn) drain(buf []byte, registry uint32) []byte {
	for len(buf) >= headerLen {
		obj := binary.LittleEndian.Uint32(buf[0:4])
		word := binary.LittleEndian.Uint32(buf[4:8])
		size, opcode := int(word>>16), uint16(word&0xffff)
		if size < headerLen {
			c.fail(fmt.Errorf("compositor sent a %d-byte message, which is malformed", size))
			return nil
		}
		if len(buf) < size {
			return buf
		}
		c.dispatch(obj, opcode, buf[headerLen:size], registry)
		buf = buf[size:]
	}
	return buf
}

func (c *Conn) dispatch(obj uint32, opcode uint16, body []byte, registry uint32) {
	switch obj {
	case displayID:
		c.display(opcode, body)
		return
	case registry:
		c.registry(opcode, body)
		return
	}

	c.mu.Lock()
	isManager := obj == c.manager
	isSeat := obj == c.seat
	isPointer := c.pointer != 0 && obj == c.pointer
	_, isToplevel := c.toplevels[obj]
	_, isOutput := c.outputs[obj]
	_, isBar := c.bars[obj]
	_, isFrac := c.fracs[obj]
	_, isBuffer := c.buffers[obj]
	ch, isCallback := c.pending[obj]
	if isCallback {
		delete(c.pending, obj)
	}
	c.mu.Unlock()

	switch {
	case isCallback && opcode == callbackDone:
		close(ch)
	case isManager:
		c.manager2(opcode, body)
	case isToplevel:
		c.toplevel(obj, opcode, body)
	case isOutput:
		c.output(obj, opcode, body)
	case isBar:
		c.layerSurface(obj, opcode, body)
	case isFrac:
		c.fractionalScale(obj, opcode, body)
	case isBuffer:
		c.bufferReleased(obj, opcode)
	case isPointer:
		c.pointerEvent(opcode, body)
	case isSeat:
		c.seatEvent(opcode, body)
	}
}

func (c *Conn) display(opcode uint16, body []byte) {
	switch opcode {
	case displayError:
		if len(body) < 8 {
			return
		}
		badObj := binary.LittleEndian.Uint32(body[0:4])
		code := binary.LittleEndian.Uint32(body[4:8])
		msg, _ := readString(body[8:])
		c.fail(fmt.Errorf("compositor protocol error on object 0x%x (code %d): %s", badObj, code, msg))
	case displayDeleteID:
		// Object ids may be reused after this. We never reuse ours, which
		// costs a few ids over a session and avoids a whole class of bug.
	}
}

func (c *Conn) registry(opcode uint16, body []byte) {
	if opcode == registryGlobalRemove {
		c.globalGone(body)
		return
	}
	if opcode != registryGlobal {
		return
	}
	if len(body) < 4 {
		return
	}
	name := binary.LittleEndian.Uint32(body[0:4])
	iface, rest := readString(body[4:])
	if len(rest) < 4 {
		return
	}
	version := binary.LittleEndian.Uint32(rest[0:4])

	switch iface {
	case "wl_seat":
		// Version 1 is enough: the seat is passed back as an argument to
		// activate, and listened to for one event -- capabilities, which
		// is how a pointer is found and is in version 1.
		id := c.alloc()
		c.mu.Lock()
		if c.seat != 0 {
			c.mu.Unlock()
			return
		}
		c.seat = id
		c.mu.Unlock()
		_ = c.send(c.reg, registryBind, argUint(name), argString(iface), argUint(seatVersion), argUint(id))
	case "wl_compositor":
		c.bindOnce(name, iface, version, compositorVersion, &c.compositor)
	case "wl_shm":
		c.bindOnce(name, iface, version, shmVersion, &c.shm)
	case "zwlr_layer_shell_v1":
		c.bindOnce(name, iface, version, layerShellVersion, &c.layerShell)
	case "wp_viewporter":
		c.bindOnce(name, iface, version, viewporterVersion, &c.viewporter)
	case "wp_fractional_scale_manager_v1":
		c.bindOnce(name, iface, version, fracVersion, &c.fracMgr)
	case "zwlr_foreign_toplevel_manager_v1":
		// Bind at most 3: version 3 adds the parent event, which we ignore
		// but must not be surprised by. Asking for more than the
		// compositor offers is a protocol error, so clamp.
		want := version
		if want > 3 {
			want = 3
		}
		id := c.alloc()
		c.mu.Lock()
		if c.manager != 0 {
			c.mu.Unlock()
			return
		}
		c.manager = id
		c.mu.Unlock()
		_ = c.send(c.reg, registryBind, argUint(name), argString(iface), argUint(want), argUint(id))
	case "wl_output":
		// Every one of them, not just the first: the question these are
		// bound for is which of the monitors a window is on, which needs
		// them all. An output older than version 4 is still bound, and
		// still gives a position; it just has no name.
		want := version
		if want > outputVersion {
			want = outputVersion
		}
		id := c.alloc()
		c.mu.Lock()
		c.outputs[id] = &Output{ID: id, ver: want}
		c.outputNames[name] = id
		c.mu.Unlock()
		_ = c.send(c.reg, registryBind, argUint(name), argString(iface), argUint(want), argUint(id))
	}
}

// bindOnce binds a global there is only ever one of.
//
// The version asked for is the oldest that has everything used here, not
// the newest the compositor offers: a newer one only adds events this
// client would have to know to ignore, and asking for more than is offered
// is a protocol error that kills the connection.
func (c *Conn) bindOnce(name uint32, iface string, offered, want uint32, dst *uint32) {
	if want > offered {
		want = offered
	}
	id := c.alloc()
	c.mu.Lock()
	if *dst != 0 {
		c.mu.Unlock()
		return
	}
	*dst = id
	c.mu.Unlock()
	_ = c.send(c.reg, registryBind, argUint(name), argString(iface), argUint(want), argUint(id))
}

// globalGone forgets an output that has been unplugged.
//
// Monitors are the one global this client binds that really does come and
// go: the seat and the toplevel manager last as long as the compositor.
// Keeping a vanished one would mean reporting a window as being on a
// monitor that is not there, and the caller would place a window off the
// side of the desktop.
func (c *Conn) globalGone(body []byte) {
	if len(body) < 4 {
		return
	}
	name := binary.LittleEndian.Uint32(body[0:4])
	c.mu.Lock()
	id, ok := c.outputNames[name]
	var ver uint32
	if o := c.outputs[id]; ok && o != nil {
		ver = o.ver
		delete(c.outputs, id)
		delete(c.outputNames, name)
	}
	c.mu.Unlock()
	if ok {
		c.emit(Monitor{})
	}
	if ok && ver >= outputReleaseSince {
		// The proxy is ours to destroy once the global is gone -- from
		// version 3, which is where wl_output.release was added; asking an
		// older one would be a protocol error. Sent unlocked, because send
		// takes the same lock.
		_ = c.send(id, outputRelease)
	}
}

func (c *Conn) manager2(opcode uint16, body []byte) {
	switch opcode {
	case managerToplevel:
		if len(body) < 4 {
			return
		}
		id := binary.LittleEndian.Uint32(body[0:4])
		c.mu.Lock()
		c.toplevels[id] = &Toplevel{ID: id}
		c.order = append(c.order, id)
		c.mu.Unlock()
	case managerFinished:
		c.fail(errors.New("the compositor withdrew the toplevel manager"))
	}
}

func (c *Conn) toplevel(obj uint32, opcode uint16, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.toplevels[obj]
	if !ok {
		return
	}
	switch opcode {
	case handleTitle:
		t.Title, _ = readString(body)
	case handleAppID:
		t.AppID, _ = readString(body)
	case handleOutputEnter, handleOutputLeave:
		if len(body) < 4 {
			return
		}
		out := binary.LittleEndian.Uint32(body[0:4])
		// A window can be on two monitors at once, so these are a set and
		// not a single field. The compositor re-sends enter for every
		// output when a window moves, so a duplicate is normal.
		if opcode == handleOutputEnter {
			if !slices.Contains(t.Outputs, out) {
				t.Outputs = append(t.Outputs, out)
			}
			return
		}
		if i := slices.Index(t.Outputs, out); i >= 0 {
			t.Outputs = slices.Delete(t.Outputs, i, i+1)
		}
	case handleState:
		if len(body) < 4 {
			return
		}
		n := int(binary.LittleEndian.Uint32(body[0:4]))
		vals := body[4:]
		if n > len(vals) {
			n = len(vals)
		}
		wasActive := t.Activated
		t.Activated, t.Minimized = false, false
		for i := 0; i+4 <= n; i += 4 {
			switch binary.LittleEndian.Uint32(vals[i : i+4]) {
			case stateActivated:
				t.Activated = true
			case stateMinimized:
				t.Minimized = true
			}
		}
		// Newly focused: move to the end, so the caller's "last match is
		// the most recently raised" assumption holds.
		if t.Activated && !wasActive {
			c.raise(obj)
			c.emit(Focus{})
		}
	case handleDone:
		// The first done is where a window stops being half-described.
		// One that is already focused by then -- the window open when
		// this client started -- has had no state event a caller could
		// have noticed, so this is the only announcement of it.
		first := !t.seen
		t.seen = true
		if first && t.Activated {
			c.emit(Focus{})
		}
	case handleClosed:
		wasActive := t.Activated
		delete(c.toplevels, obj)
		c.forget(obj)
		if wasActive {
			c.emit(Focus{})
		}
		// The handle is ours to destroy once the compositor is done with it.
		go func() { _ = c.send(obj, toplevelHandleDestroy) }()
	}
}

// output records what a monitor says about itself.
//
// Two of its six events are read. geometry carries the position, which is
// what a caller matching this monitor against another display server's
// idea of the same monitor falls back to; name carries the connector,
// which is what it matches on first. The rest -- the physical size, the
// mode, the scale, the description -- describe a monitor to something that
// draws on it, and nothing here draws.
func (c *Conn) output(obj uint32, opcode uint16, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.outputs[obj]
	if !ok {
		return
	}
	switch opcode {
	case outputGeometry:
		if len(body) < 8 {
			return
		}
		// Signed: an output left of or above the origin has negative
		// coordinates, which is an ordinary two-monitor layout.
		o.X = int(int32(binary.LittleEndian.Uint32(body[0:4])))
		o.Y = int(int32(binary.LittleEndian.Uint32(body[4:8])))
	case outputName:
		o.Name, _ = readString(body)
		// A monitor is worth announcing once it has a name, not when its
		// global arrives: a bar placed on a nameless output cannot be
		// matched to XWayland's idea of the same monitor, and the name is
		// the last of the describing events to come.
		c.emit(Monitor{})
	}
}

// raise moves id to the end of the order slice. Callers hold c.mu.
func (c *Conn) raise(id uint32) {
	c.forget(id)
	c.order = append(c.order, id)
}

// forget removes id from the order slice. Callers hold c.mu.
func (c *Conn) forget(id uint32) {
	for i, v := range c.order {
		if v == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

// readString decodes a length-prefixed, NUL-terminated, 4-byte-padded
// string and returns it with whatever follows it.
func readString(b []byte) (string, []byte) {
	if len(b) < 4 {
		return "", nil
	}
	n := int(binary.LittleEndian.Uint32(b[0:4]))
	if n == 0 || 4+n > len(b) {
		return "", nil
	}
	s := string(b[4 : 4+n-1]) // drop the NUL
	adv := 4 + (n+3)&^3
	if adv > len(b) {
		return s, nil
	}
	return s, b[adv:]
}
