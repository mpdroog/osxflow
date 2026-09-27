package wl

// Shared memory: the pixels themselves.
//
// A Wayland client does not send its pixels to the compositor the way an X
// client sends a PutImage. It opens a file, maps it, draws into the map,
// and hands the compositor the file descriptor once; from then on both
// processes are looking at the same pages and a frame costs a commit
// rather than a megabyte down the socket.
//
// The file is a memfd, which is the one part of this that needs a
// system call rather than a protocol message: anonymous memory with a file
// descriptor attached, living only as long as the descriptor. It is sealed
// against shrinking before it is shared, because the compositor maps it and
// would take a SIGBUS if it were truncated underneath -- a client can crash
// the compositor that way, and the seal is what says "this cannot happen".

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/mpdroog/osxflow/internal/paint"
)

// Pixel format. ARGB8888 is the one format every compositor must support,
// and its bytes in memory are B,G,R,A -- the same order a little-endian X
// server wants, which is why paint.BGRA serves both.
const formatARGB8888 = 0

// buffers is how many frames a bar cycles through. Two is the smallest
// number that lets the next frame be drawn while the compositor still has
// the last one on screen; with one the client must wait for a release
// before every paint, which shows up as a bar that lags the pointer.
const buffers = 2

// pool is a shared-memory file and the compositor's handle on it.
type pool struct {
	id   uint32 // wl_shm_pool
	fd   int
	data []byte // the mapping, buffers laid end to end
	size int
}

// buffer is one frame's worth of that mapping.
type buffer struct {
	id     uint32 // wl_buffer
	offset int
	size   int

	// busy is set while the compositor may still be reading this buffer:
	// from the commit that attached it until its release event. Drawing
	// into a busy buffer is drawing into what is on screen.
	busy bool
}

// newPool makes a shared-memory file big enough for every buffer, maps it,
// and creates the wl_shm_pool and wl_buffers over it.
//
// width and height are in device pixels: this is the real size of the
// picture, after the output's scale has been applied.
func (c *Conn) newPool(width, height int) (*pool, []*buffer, error) {
	if width <= 0 || height <= 0 {
		return nil, nil, fmt.Errorf("a buffer must have a positive size, got %dx%d", width, height)
	}
	stride := width * 4
	frame := stride * height
	size := frame * buffers

	fd, err := unix.MemfdCreate("osxflow-wl", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, nil, fmt.Errorf("creating a shared-memory file: %w", err)
	}
	closeFD := func(cause error) (*pool, []*buffer, error) {
		if closeErr := unix.Close(fd); closeErr != nil {
			return nil, nil, fmt.Errorf("%w (and closing the file: %v)", cause, closeErr)
		}
		return nil, nil, cause
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return closeFD(fmt.Errorf("sizing the shared-memory file to %d bytes: %w", size, err))
	}
	// Sealed after the size is final and before the compositor ever sees
	// it. F_SEAL_SHRINK is the one that matters; the others cost nothing
	// and say plainly that this file is finished being changed.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW); err != nil {
		return closeFD(fmt.Errorf("sealing the shared-memory file: %w", err))
	}
	data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return closeFD(fmt.Errorf("mapping the shared-memory file: %w", err))
	}

	p := &pool{id: c.alloc(), fd: fd, data: data, size: size}
	if err := c.sendFD(c.shm, shmCreatePool, fd, argUint(p.id), argUint(uint32(size))); err != nil {
		_ = unix.Munmap(data)
		return closeFD(err)
	}

	bufs := make([]*buffer, buffers)
	for i := range bufs {
		b := &buffer{id: c.alloc(), offset: i * frame, size: frame}
		err := c.send(p.id, poolCreateBuffer,
			argUint(b.id), argInt(b.offset), argInt(width), argInt(height),
			argInt(stride), argUint(formatARGB8888))
		if err != nil {
			return nil, nil, err
		}
		bufs[i] = b
	}
	return p, bufs, nil
}

// write copies one frame of Go's R,G,B,A pixels into the buffer's slice of
// the mapping, in the order the compositor reads.
func (p *pool) write(b *buffer, pix []byte) {
	paint.BGRA(p.data[b.offset:b.offset+b.size], pix)
}

// release gives back the mapping and the file. The wl_shm_pool and the
// wl_buffers over it are destroyed by the caller, which knows their ids.
func (p *pool) release() error {
	var errs []error
	if p.data != nil {
		if err := unix.Munmap(p.data); err != nil {
			errs = append(errs, fmt.Errorf("unmapping the shared-memory file: %w", err))
		}
		p.data = nil
	}
	if p.fd >= 0 {
		if err := unix.Close(p.fd); err != nil {
			errs = append(errs, fmt.Errorf("closing the shared-memory file: %w", err))
		}
		p.fd = -1
	}
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}
