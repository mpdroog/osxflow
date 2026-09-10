package ui

// Getting pixels onto the screen.
//
// xgbutil's xgraphics package would do this, but it pulls in freetype-go
// and graphics-go for text and scaling that this file does not need -- two
// unmaintained dependencies to avoid writing the sixty lines below. Text
// comes from x/image instead, so all that is wanted here is "put this RGBA
// buffer on that window".
//
// Drawing goes to a pixmap and is copied to the window in one operation,
// which is what makes the window appear complete rather than filling in.

import (
	"fmt"
	"image"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type surface struct {
	conn *xgb.Conn
	img  *image.RGBA

	// buf holds the server-order copy of img. It is kept across frames so
	// that repainting does not allocate a megabyte each time.
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

	// visible is how much of the buffer the window currently shows.
	visible int
}

func newSurface(conn *xgb.Conn, win xproto.Window, depth byte, width, height int) (*surface, error) {
	pixmap, err := xproto.NewPixmapId(conn)
	if err != nil {
		return nil, fmt.Errorf("allocating a pixmap id: %w", err)
	}
	if pxErr := xproto.CreatePixmapChecked(conn, depth, pixmap,
		xproto.Drawable(win), u16(width), u16(height)).Check(); pxErr != nil {
		return nil, fmt.Errorf("creating the pixmap: %w", pxErr)
	}

	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		return nil, fmt.Errorf("allocating a graphics context id: %w", err)
	}
	if gcErr := xproto.CreateGCChecked(conn, gc, xproto.Drawable(pixmap), 0, nil).Check(); gcErr != nil {
		return nil, fmt.Errorf("creating the graphics context: %w", gcErr)
	}

	setup := xproto.Setup(conn)
	s := &surface{
		conn:   conn,
		img:    image.NewRGBA(image.Rect(0, 0, width, height)),
		win:    win,
		pixmap: pixmap,
		gc:     gc,
		width:  width,
		height: height,
		depth:  depth,
		swapRB: setup.ImageByteOrder == xproto.ImageOrderLSBFirst,
	}
	s.buf = make([]byte, width*height*4)
	s.visible = height
	s.maxRows = rowsPerRequest(setup.MaximumRequestLength, width)
	return s, nil
}

// rowsPerRequest works out how much of the image fits in one PutImage.
//
// A request cannot exceed the server's maximum length, and exceeding it is
// not a graceful failure: the connection is dropped. MaximumRequestLength
// counts 4-byte units, and the request header and padding have to come out
// of the budget before the pixels do.
func rowsPerRequest(maxLenUnits uint16, width int) int {
	const requestOverhead = 64 // generous: the header is 28 bytes
	budget := int(maxLenUnits)*4 - requestOverhead
	if budget < width*4 {
		return 1
	}
	return budget / (width * 4)
}

// image returns the buffer to draw into.
func (s *surface) image() *image.RGBA { return s.img }

// flush pushes the top visible rows of the image to the server and copies
// them onto the window.
//
// Only `height` rows are uploaded, not the whole buffer: the pixmap is
// allocated once at the largest size the window can take, and a short
// result list should not cost a full-size upload.
func (s *surface) flush(height int) error {
	height = min(max(height, 1), s.height)
	s.visible = height
	s.encode()
	stride := s.width * 4

	for y := 0; y < height; y += s.maxRows {
		rows := min(s.maxRows, height-y)
		data := s.buf[y*stride : (y+rows)*stride]
		if err := xproto.PutImageChecked(s.conn, xproto.ImageFormatZPixmap,
			xproto.Drawable(s.pixmap), s.gc,
			u16(s.width), u16(rows), 0, i16(y),
			0, s.depth, data).Check(); err != nil {
			return fmt.Errorf("uploading rows %d-%d: %w", y, y+rows, err)
		}
	}
	return s.copyToWindow()
}

// copyToWindow is the only operation that changes what is on screen.
func (s *surface) copyToWindow() error {
	err := xproto.CopyAreaChecked(s.conn, xproto.Drawable(s.pixmap), xproto.Drawable(s.win),
		s.gc, 0, 0, 0, 0, u16(s.width), u16(s.visible)).Check()
	if err != nil {
		return fmt.Errorf("copying to the window: %w", err)
	}
	return nil
}

// encode converts Go's R,G,B,A byte order into the server's.
func (s *surface) encode() {
	src := s.img.Pix
	if !s.swapRB {
		copy(s.buf, src)
		return
	}
	// Little-endian ZPixmap at 32bpp wants the bytes as B,G,R,unused.
	for i := 0; i+3 < len(src); i += 4 {
		s.buf[i+0] = src[i+2]
		s.buf[i+1] = src[i+1]
		s.buf[i+2] = src[i+0]
		s.buf[i+3] = src[i+3]
	}
}

func (s *surface) close() {
	if s.gc != 0 {
		xproto.FreeGC(s.conn, s.gc)
		s.gc = 0
	}
	if s.pixmap != 0 {
		xproto.FreePixmap(s.conn, s.pixmap)
		s.pixmap = 0
	}
}
