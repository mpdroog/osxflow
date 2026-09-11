package notify

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"syscall"

	// The formats LoadImageFile understands.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// MaxImageSide bounds each dimension of an image-data hint. The largest
// thing drawn is an icon a few dozen pixels across; a client sending more
// than this is confused, or trying to make the daemon allocate.
const MaxImageSide = 1024

// ErrImageData is returned for an image-data hint that is not the
// (iiibiiay) structure the specification describes.
var ErrImageData = errors.New("malformed image-data")

// DecodeImageData converts an image-data hint into an image.
//
// The hint is a raw pixel buffer -- width, height, rowstride, has-alpha,
// bits per sample, channels, data -- which is how an application with an
// image in memory (an avatar, an album cover) sends it without writing a
// file. godbus hands the struct over as []any.
func DecodeImageData(v any) (*image.RGBA, error) {
	f, ok := v.([]any)
	if !ok || len(f) != 7 {
		return nil, fmt.Errorf("%w: want a struct of 7 fields, got %T", ErrImageData, v)
	}
	w, okW := f[0].(int32)
	h, okH := f[1].(int32)
	stride, okS := f[2].(int32)
	alpha, okA := f[3].(bool)
	bps, okB := f[4].(int32)
	channels, okC := f[5].(int32)
	data, okD := f[6].([]byte)
	if !okW || !okH || !okS || !okA || !okB || !okC || !okD {
		return nil, fmt.Errorf("%w: fields are not (iiibiiay)", ErrImageData)
	}
	return decodePixels(int(w), int(h), int(stride), alpha, int(bps), int(channels), data)
}

// decodePixels validates a raw buffer against its own description and
// converts it to premultiplied RGBA.
//
// Every field is checked against the others before a byte is read. The
// description comes from another process, and an image whose rowstride
// times height runs past the end of its data would otherwise be an
// out-of-range read.
func decodePixels(w, h, stride int, alpha bool, bps, channels int, data []byte) (*image.RGBA, error) {
	want := 3
	if alpha {
		want = 4
	}
	switch {
	case w <= 0 || h <= 0:
		return nil, fmt.Errorf("%w: %dx%d is empty", ErrImageData, w, h)
	case w > MaxImageSide || h > MaxImageSide:
		return nil, fmt.Errorf("%w: %dx%d is larger than %d on a side", ErrImageData, w, h, MaxImageSide)
	case bps != 8:
		return nil, fmt.Errorf("%w: %d bits per sample, only 8 is supported", ErrImageData, bps)
	case channels != want:
		return nil, fmt.Errorf("%w: %d channels with alpha=%t", ErrImageData, channels, alpha)
	case stride < w*channels:
		return nil, fmt.Errorf("%w: rowstride %d is shorter than a row", ErrImageData, stride)
	}
	// In int64 because a hostile stride times the height can overflow an
	// int on a 32-bit build.
	need := int64(stride)*int64(h-1) + int64(w*channels)
	if int64(len(data)) < need {
		return nil, fmt.Errorf("%w: %d bytes of data, the description needs %d", ErrImageData, len(data), need)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		src := data[y*stride : y*stride+w*channels]
		dst := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := range w {
			s := src[x*channels : x*channels+channels]
			a := byte(0xff)
			if alpha {
				a = s[3]
			}
			d := dst[x*4 : x*4+4]
			d[0], d[1], d[2], d[3] = premul(s[0], a), premul(s[1], a), premul(s[2], a), a
		}
	}
	return img, nil
}

// premul scales a straight-alpha channel by its alpha, which is what
// image.RGBA and the X server both expect.
func premul(c, a byte) byte {
	//nolint:gosec // c*a/255 with both at most 255 fits a byte
	return byte((uint32(c)*uint32(a) + 127) / 255)
}

// Bounds on an image file a notification names. The largest thing drawn
// is an icon, so these do not limit anything real; they stop a client
// pointing the daemon at something that takes a second and a gigabyte to
// decode.
const (
	maxImageFileBytes = 16 << 20
	maxImageFileSide  = 4096
)

// LoadImageFile decodes a PNG, JPEG or GIF that a notification names.
//
// SVG is not among them, though most icon themes are made of it. The
// pure-Go rasterisers draw this desktop's SVGs badly enough to be worse
// than the placeholder (see internal/icons), so an SVG comes back as an
// error and the caller falls back to an icon it has.
//
// A file that does not exist comes back wrapping fs.ErrNotExist, and one in
// a format not understood here (SVG) wrapping image.ErrFormat: those are
// the everyday reasons to fall back, and a caller can tell them apart from
// a file that is really broken.
func LoadImageFile(path string) (img image.Image, err error) {
	// O_NONBLOCK so that a path naming a FIFO fails instead of hanging the
	// daemon until something writes to it. On a regular file it changes
	// nothing.
	//nolint:gosec // the path comes from a client on the user's own session bus
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("opening image: %w", err)
	}
	defer func() {
		// Read-only, so a failed close loses nothing; it is still reported,
		// alongside whatever else went wrong.
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing image %s: %w", path, closeErr))
		}
	}()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting image: %w", err)
	}
	switch {
	case !st.Mode().IsRegular():
		return nil, fmt.Errorf("image %s is not a regular file", path)
	case st.Size() > maxImageFileBytes:
		return nil, fmt.Errorf("image %s is %d bytes, over the %d limit", path, st.Size(), maxImageFileBytes)
	}

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("reading image %s: %w", path, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageFileSide || cfg.Height > maxImageFileSide {
		return nil, fmt.Errorf("image %s is %dx%d, outside what is drawn", path, cfg.Width, cfg.Height)
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return nil, fmt.Errorf("rewinding image: %w", seekErr)
	}
	decoded, _, err := image.Decode(io.LimitReader(f, maxImageFileBytes))
	if err != nil {
		return nil, fmt.Errorf("decoding image %s: %w", path, err)
	}
	return decoded, nil
}
