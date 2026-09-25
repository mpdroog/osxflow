// Command wlprobe draws a plain bar on every monitor through
// zwlr_layer_shell_v1 and prints what the compositor says back.
//
// It is the smallest thing that exercises internal/wl's drawing half
// end to end -- shared memory, the layer surface, the exclusive zone,
// fractional scale and the pointer -- without any of menubar's tray, fonts
// or D-Bus in the way. When the bar is wrong, run this first: if its strip
// is in the right place on both screens and its marks are sharp, the
// problem is above this layer.
//
//	go run ./tools/wlprobe            # 10 seconds, then tidies up
//	go run ./tools/wlprobe -seconds 0 # until interrupted
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/wl"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("wlprobe: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	height := flag.Int("height", 29, "bar height in logical pixels")
	seconds := flag.Int("seconds", 10, "how long to stay up (0 for until interrupted)")
	flag.Parse()

	c, err := wl.Dial()
	if err != nil {
		return err
	}
	defer func() {
		if err := c.Disconnect(); err != nil {
			log.Printf("disconnecting: %v", err)
		}
	}()

	outputs := c.Outputs()
	if len(outputs) == 0 {
		return fmt.Errorf("the compositor reports no monitors")
	}
	bars := make(map[*wl.Bar]wl.Output)
	defer func() {
		for b, o := range bars {
			if err := b.Close(); err != nil {
				log.Printf("closing the bar on %s: %v", o.Name, err)
			}
		}
	}()
	for _, o := range outputs {
		b, err := c.NewBar(o.ID, *height, "osxflow-wlprobe")
		if err != nil {
			return fmt.Errorf("bar on %s: %w", o.Name, err)
		}
		bars[b] = o
		w, h := b.Size()
		log.Printf("%s at %d,%d: bar %dx%d logical, scale %.3g", o.Name, o.X, o.Y, w, h, b.Scale())
		if err := draw(b, -1); err != nil {
			return err
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	var deadline <-chan time.Time
	if *seconds > 0 {
		t := time.NewTimer(time.Duration(*seconds) * time.Second)
		defer t.Stop()
		deadline = t.C
	}
	for {
		select {
		case <-stop:
			return nil
		case <-deadline:
			return nil
		case e := <-c.Events():
			switch e := e.(type) {
			case wl.Configure:
				log.Printf("%s: configured %dx%d", bars[e.Bar].Name, e.Width, e.Height)
				if err := draw(e.Bar, -1); err != nil {
					return err
				}
			case wl.Scale:
				w, h := e.Bar.Size()
				log.Printf("%s: scale %.3g (bar %dx%d logical)", bars[e.Bar].Name, e.Scale, w, h)
				if err := draw(e.Bar, -1); err != nil {
					return err
				}
			case wl.Motion:
				if err := draw(e.Bar, e.X); err != nil {
					return err
				}
			case wl.Leave:
				if err := draw(e.Bar, -1); err != nil {
					return err
				}
			case wl.Button:
				log.Printf("%s: button %d pressed=%t at %d,%d",
					bars[e.Bar].Name, e.Button, e.Pressed, e.X, e.Y)
			case wl.Closed:
				return fmt.Errorf("the compositor closed the bar on %s", bars[e.Bar].Name)
			}
		}
	}
}

// draw fills the bar and marks both of its ends, so that a screenshot
// shows whether it really spans the monitor it is on. cursor is where the
// pointer is, in device pixels, or -1 for not on this bar.
func draw(b *wl.Bar, cursor int) error {
	img, err := b.Frame()
	if err != nil {
		return err
	}
	r := img.Bounds()
	paint.Fill(img, r, color.RGBA{R: 0x1b, G: 0x1c, B: 0x24, A: 0xff})

	// A square at each end and one in the middle: if the bar is clipped to
	// the wrong monitor, or stretched by a viewport that disagrees with
	// its buffer, the right-hand one is the square that goes missing.
	mark := r.Dy() - 8
	for _, x := range []int{4, (r.Dx() - mark) / 2, r.Dx() - mark - 4} {
		paint.Fill(img, image.Rect(x, 4, x+mark, 4+mark), color.RGBA{R: 0x8f, G: 0xbc, B: 0xbb, A: 0xff})
	}
	// A hairline along the bottom edge, one device pixel tall: it is sharp
	// on a correctly scaled surface and blurred to two grey rows on one
	// the compositor is stretching.
	paint.Fill(img, image.Rect(0, r.Dy()-1, r.Dx(), r.Dy()), color.RGBA{R: 0x4c, G: 0x56, B: 0x6a, A: 0xff})

	if cursor >= 0 {
		paint.Fill(img, image.Rect(cursor-1, 0, cursor+1, r.Dy()), color.RGBA{R: 0xbf, G: 0x61, B: 0x6a, A: 0xff})
	}
	return b.Flush()
}
