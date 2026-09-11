// Package xsurface gets pixels onto an X11 window.
//
// Drawing goes into an off-screen RGBA image, is uploaded to a pixmap, and
// is copied to the window in one operation, so the window never shows a
// half-drawn frame.
//
// xgbutil's xgraphics would do this, but it pulls in freetype-go and
// graphics-go for text and scaling that nothing here wants -- two
// unmaintained dependencies to avoid writing this file.
package xsurface

import (
	"errors"
	"fmt"
	"image"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
)

// Surface is a double-buffered drawing target for one window.
type Surface struct {
	conn *xgb.Conn
	img  *image.RGBA

	// buf holds the server-order copy of img, kept across frames so that
	// repainting does not allocate a megabyte every time.
	buf []byte

	win    xproto.Window
	pixmap xproto.Pixmap
	gc     xproto.Gcontext

	width, height int
	depth         byte

	// swapRB is set when the server wants B,G,R,A rather than R,G,B,A.
	// Every ordinary little-endian X server does.
	swapRB bool

	// maxRows is how many rows of pixels fit in one PutImage request.
	maxRows int
}

// New creates a surface for a window of the given depth and size.
func New(conn *xgb.Conn, win xproto.Window, depth byte, width, height int) (*Surface, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("surface must have a positive size, got %dx%d", width, height)
	}
	pixmap, err := xproto.NewPixmapId(conn)
	if err != nil {
		return nil, fmt.Errorf("allocating a pixmap id: %w", err)
	}
	if pxErr := xproto.CreatePixmapChecked(conn, depth, pixmap,
		xproto.Drawable(win), geom.U16(width), geom.U16(height)).Check(); pxErr != nil {
		return nil, fmt.Errorf("creating the pixmap: %w", pxErr)
	}

	// Past this point a failure owns a pixmap on the server, which lives as
	// long as the connection does unless it is given back.
	freePixmap := func(cause error) error {
		if freeErr := xproto.FreePixmapChecked(conn, pixmap).Check(); freeErr != nil {
			return errors.Join(cause, fmt.Errorf("freeing the pixmap: %w", freeErr))
		}
		return cause
	}

	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		return nil, freePixmap(fmt.Errorf("allocating a graphics context id: %w", err))
	}
	// The GC is made against the pixmap, not the window: they must share a
	// depth, and on an ARGB dock the window is 32-bit while the root is 24.
	if gcErr := xproto.CreateGCChecked(conn, gc, xproto.Drawable(pixmap), 0, nil).Check(); gcErr != nil {
		return nil, freePixmap(fmt.Errorf("creating the graphics context: %w", gcErr))
	}

	setup := xproto.Setup(conn)
	s := &Surface{
		conn:    conn,
		img:     image.NewRGBA(image.Rect(0, 0, width, height)),
		buf:     make([]byte, width*height*4),
		win:     win,
		pixmap:  pixmap,
		gc:      gc,
		width:   width,
		height:  height,
		depth:   depth,
		swapRB:  setup.ImageByteOrder == xproto.ImageOrderLSBFirst,
		maxRows: rowsPerRequest(setup.MaximumRequestLength, width),
	}
	return s, nil
}

// rowsPerRequest works out how much of the image fits in one PutImage.
//
// Exceeding the server's maximum request length is not a graceful failure:
// the connection is dropped. MaximumRequestLength counts 4-byte units, and
// the request header has to come out of the budget before the pixels do.
func rowsPerRequest(maxLenUnits uint16, width int) int {
	const requestOverhead = 64 // generous: the header is 28 bytes
	budget := int(maxLenUnits)*4 - requestOverhead
	if budget < width*4 {
		return 1
	}
	return budget / (width * 4)
}

// Image returns the buffer to draw into.
func (s *Surface) Image() *image.RGBA { return s.img }

// Size reports the surface's dimensions.
func (s *Surface) Size() (width, height int) { return s.width, s.height }

// Flush uploads the image and copies it onto the window.
//
// The requests are sent unchecked, which is the difference between a dock
// that keeps up with the pointer and one that does not. A checked request
// blocks until the server replies, and the image does not fit in one
// request -- at this width the server's limit works out to about sixty
// rows -- so a checked flush meant five or six synchronous round trips per
// frame. X is an asynchronous protocol and a render loop is exactly what
// that is for.
//
// The cost is that a malformed request is reported as an error event
// rather than returned here. That is the right trade: these requests are
// built from the surface's own fields, so a failure is a bug in this file
// rather than a condition the caller can handle, and the event loop logs
// it.
func (s *Surface) Flush() error {
	s.encode()
	stride := s.width * 4
	for y := 0; y < s.height; y += s.maxRows {
		rows := min(s.maxRows, s.height-y)
		data := s.buf[y*stride : (y+rows)*stride]
		xproto.PutImage(s.conn, xproto.ImageFormatZPixmap,
			xproto.Drawable(s.pixmap), s.gc,
			geom.U16(s.width), geom.U16(rows), 0, geom.I16(y),
			0, s.depth, data)
	}
	return s.Copy()
}

// Copy repaints the window from the pixmap without re-uploading, which is
// all an Expose event needs.
func (s *Surface) Copy() error {
	xproto.CopyArea(s.conn, xproto.Drawable(s.pixmap), xproto.Drawable(s.win),
		s.gc, 0, 0, 0, 0, geom.U16(s.width), geom.U16(s.height))
	return nil
}

// encode converts Go's R,G,B,A byte order into the server's.
//
// The alpha byte is carried through unchanged, which is what makes a
// 32-bit visual translucent under a compositor: the server reads the
// fourth byte as alpha rather than as padding.
//
// A pixel at a time rather than a byte at a time: this runs over the whole
// surface on every frame, and the byte-wise version spent two thirds of a
// millisecond of an 8 ms budget on four dependent byte loads and stores
// where one 32-bit load, a swap of two of its bytes and one store will do.
func (s *Surface) encode() {
	src := s.img.Pix
	if !s.swapRB {
		copy(s.buf, src)
		return
	}
	dst := s.buf[:len(src)]
	for i := 0; i+4 <= len(src); i += 4 {
		in := src[i : i+4 : i+4]
		out := dst[i : i+4 : i+4]
		v := uint32(in[0]) | uint32(in[1])<<8 | uint32(in[2])<<16 | uint32(in[3])<<24
		v = v&0xff00ff00 | (v&0x00ff0000)>>16 | (v&0x000000ff)<<16
		//nolint:gosec // a byte conversion of a 32-bit word is the truncation asked for
		out[0], out[1], out[2], out[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
}

// Close releases the server-side resources.
//
// The frees are checked, unlike the drawing requests: a failure here means
// the ids were already wrong, which is worth hearing about before the next
// frame draws into them. Both requests are sent before either is checked,
// so the check costs one round trip rather than two -- a resize on the way
// to revealing the dock goes through here, and that is latency the user
// feels.
func (s *Surface) Close() error {
	var (
		gcCookie     *xproto.FreeGCCookie
		pixmapCookie *xproto.FreePixmapCookie
	)
	if s.gc != 0 {
		c := xproto.FreeGCChecked(s.conn, s.gc)
		gcCookie = &c
		s.gc = 0
	}
	if s.pixmap != 0 {
		c := xproto.FreePixmapChecked(s.conn, s.pixmap)
		pixmapCookie = &c
		s.pixmap = 0
	}
	var errs []error
	if gcCookie != nil {
		if err := gcCookie.Check(); err != nil {
			errs = append(errs, fmt.Errorf("freeing the graphics context: %w", err))
		}
	}
	if pixmapCookie != nil {
		if err := pixmapCookie.Check(); err != nil {
			errs = append(errs, fmt.Errorf("freeing the pixmap: %w", err))
		}
	}
	return errors.Join(errs...)
}
