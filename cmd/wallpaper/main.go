// Command wallpaper sets the desktop background and exits.
//
// It replaces xfdesktop where all that is wanted from it is the picture:
// no desktop icons, and nothing left running. The picture is drawn into a
// pixmap that becomes the root window's background, and X is told to keep
// it after this program has gone. _XROOTPMAP_ID and ESETROOT_PMAP_ID name
// it, as feh and Esetroot do, so xfwm4's compositor draws it and a later
// run can free it.
//
//	wallpaper [-style zoom|fit|stretch|center] picture.jpg
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg" // the decoders image.Decode picks from
	_ "image/png"
	"log"
	"os"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/xsurface"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func main() {
	log.SetPrefix("wallpaper: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	styleFlag := flag.String("style", "zoom", "how to fit the picture: zoom, fit, stretch or center")
	flag.Usage = func() {
		if _, err := fmt.Fprintf(flag.CommandLine.Output(), "usage: wallpaper [-style zoom|fit|stretch|center] picture\n"); err != nil {
			log.Printf("writing usage: %v", err)
		}
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		return errors.New("want exactly one picture")
	}
	st, err := parseStyle(*styleFlag)
	if err != nil {
		return err
	}
	pic, err := load(flag.Arg(0))
	if err != nil {
		return err
	}

	conn, err := xgb.NewConn()
	if err != nil {
		return fmt.Errorf("connecting to X display: %w", err)
	}
	// Closed only after the close-down mode is set, or the pixmap would
	// go with the connection.
	defer conn.Close()
	screen := xproto.Setup(conn).DefaultScreen(conn)

	frame, err := render(pic, int(screen.WidthInPixels), int(screen.HeightInPixels), st)
	if err != nil {
		return err
	}
	return setRoot(conn, screen, frame)
}

func load(path string) (image.Image, error) {
	f, err := os.Open(path) //nolint:gosec // the picture is whatever the user names; reading it is the point
	if err != nil {
		return nil, err
	}
	pic, _, decodeErr := image.Decode(f)
	closeErr := f.Close()
	if decodeErr != nil {
		return nil, errors.Join(fmt.Errorf("decoding %s: %w", path, decodeErr), closeErr)
	}
	if closeErr != nil {
		log.Printf("closing %s: %v", path, closeErr)
	}
	return pic, nil
}

// setRoot makes frame the root window's background and keeps it after
// exit.
func setRoot(conn *xgb.Conn, screen *xproto.ScreenInfo, frame *image.RGBA) error {
	root := screen.Root
	rootPmap, err := internAtom(conn, "_XROOTPMAP_ID")
	if err != nil {
		return err
	}
	esetroot, err := internAtom(conn, "ESETROOT_PMAP_ID")
	if err != nil {
		return err
	}

	pixmap, err := xproto.NewPixmapId(conn)
	if err != nil {
		return fmt.Errorf("allocating a pixmap id: %w", err)
	}
	b := frame.Bounds()
	if pxErr := xproto.CreatePixmapChecked(conn, screen.RootDepth, pixmap, xproto.Drawable(root),
		geom.U16(b.Dx()), geom.U16(b.Dy())).Check(); pxErr != nil {
		return fmt.Errorf("creating the background pixmap: %w", pxErr)
	}
	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		return fmt.Errorf("allocating a graphics context id: %w", err)
	}
	if gcErr := xproto.CreateGCChecked(conn, gc, xproto.Drawable(pixmap), 0, nil).Check(); gcErr != nil {
		return fmt.Errorf("creating the graphics context: %w", gcErr)
	}
	if putErr := xsurface.Put(conn, xproto.Drawable(pixmap), gc, screen.RootDepth, frame); putErr != nil {
		return putErr
	}
	if freeErr := xproto.FreeGCChecked(conn, gc).Check(); freeErr != nil {
		log.Printf("freeing the graphics context: %v", freeErr)
	}

	// The previous background, left behind by the last run, is freed now
	// that this one is ready -- or every run would leave a screen-sized
	// pixmap in the X server for as long as the session lasts.
	if killErr := killPrevious(conn, root, esetroot); killErr != nil {
		log.Printf("freeing the previous background: %v", killErr)
	}

	if err := xproto.ChangeWindowAttributesChecked(conn, root, xproto.CwBackPixmap,
		[]uint32{uint32(pixmap)}).Check(); err != nil {
		return fmt.Errorf("setting the root background: %w", err)
	}
	if err := xproto.ClearAreaChecked(conn, false, root, 0, 0, 0, 0).Check(); err != nil {
		return fmt.Errorf("repainting the root window: %w", err)
	}
	id := make([]byte, 4)
	binary.NativeEndian.PutUint32(id, uint32(pixmap))
	for _, prop := range []xproto.Atom{rootPmap, esetroot} {
		if err := xproto.ChangePropertyChecked(conn, xproto.PropModeReplace, root, prop,
			xproto.AtomPixmap, 32, 1, id).Check(); err != nil {
			return fmt.Errorf("naming the background pixmap: %w", err)
		}
	}
	if err := xproto.SetCloseDownModeChecked(conn, xproto.CloseDownRetainPermanent).Check(); err != nil {
		return fmt.Errorf("keeping the background after exit: %w", err)
	}
	return nil
}

// killPrevious frees the resources of the client that set the background
// last, named by ESETROOT_PMAP_ID. That client exited long ago with its
// resources kept on purpose; KillClient on one of its ids is how they are
// given back. No property means nobody set one; an id the server no longer
// knows means they are already gone.
func killPrevious(conn *xgb.Conn, root xproto.Window, esetroot xproto.Atom) error {
	reply, err := xwin.GetProperty(conn, root, esetroot, "ESETROOT_PMAP_ID")
	switch {
	case errors.Is(err, xwin.ErrPropUnset):
		return nil
	case err != nil:
		return err
	}
	if reply.Type != xproto.AtomPixmap {
		return fmt.Errorf("ESETROOT_PMAP_ID has type %d, not PIXMAP; leaving it", reply.Type)
	}
	old, err := xwin.DecodeCard32(reply)
	if err != nil {
		return fmt.Errorf("ESETROOT_PMAP_ID: %w", err)
	}
	if old == 0 {
		return nil
	}
	err = xproto.KillClientChecked(conn, old).Check()
	var gone xproto.ValueError
	if errors.As(err, &gone) {
		return nil
	}
	return err
}

func internAtom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	if reply == nil {
		return 0, fmt.Errorf("interning %s: no reply from the X server", name)
	}
	return reply.Atom, nil
}
