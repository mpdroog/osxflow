package notify

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestDecodePixelsPremultipliesRGBA(t *testing.T) {
	img, err := decodePixels(2, 1, 8, true, 8, 4, []byte{255, 0, 0, 128, 0, 0, 255, 255})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{128, 0, 0, 128, 0, 0, 255, 255}; !slices.Equal(img.Pix, want) {
		t.Fatalf("pixels = %v, want %v", img.Pix, want)
	}
}

func TestDecodePixelsHonoursRowstride(t *testing.T) {
	// One pixel wide and two high, three channels, and a byte of padding
	// after each row -- which is what GdkPixbuf's 4-byte alignment produces.
	// The last row need not carry its padding.
	img, err := decodePixels(1, 2, 4, false, 8, 3, []byte{10, 20, 30, 0, 40, 50, 60})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.RGBAAt(0, 0), (color.RGBA{R: 10, G: 20, B: 30, A: 255}); got != want {
		t.Errorf("row 0 = %v, want %v", got, want)
	}
	if got, want := img.RGBAAt(0, 1), (color.RGBA{R: 40, G: 50, B: 60, A: 255}); got != want {
		t.Errorf("row 1 = %v, want %v", got, want)
	}
}

func TestDecodePixelsRejectsWhatDoesNotAddUp(t *testing.T) {
	for _, tc := range []struct {
		name                string
		w, h, stride        int
		alpha               bool
		bps, channels, size int
	}{
		{"empty", 0, 1, 4, true, 8, 4, 4},
		{"negative", -1, 1, 4, true, 8, 4, 4},
		{"too large", MaxImageSide + 1, 1, 4 * (MaxImageSide + 1), true, 8, 4, 4 * (MaxImageSide + 1)},
		{"16-bit samples", 1, 1, 8, true, 16, 4, 8},
		{"alpha without a fourth channel", 1, 1, 3, true, 8, 3, 3},
		{"fourth channel without alpha", 1, 1, 4, false, 8, 4, 4},
		{"rowstride shorter than a row", 2, 1, 7, true, 8, 4, 8},
		{"data one byte short", 2, 2, 8, true, 8, 4, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodePixels(tc.w, tc.h, tc.stride, tc.alpha, tc.bps, tc.channels, make([]byte, tc.size))
			if !errors.Is(err, ErrImageData) {
				t.Fatalf("err = %v, want ErrImageData", err)
			}
		})
	}
}

func TestDecodeImageDataWantsTheSpecShape(t *testing.T) {
	good := []any{int32(1), int32(1), int32(4), true, int32(8), int32(4), []byte{1, 2, 3, 255}}
	if _, err := DecodeImageData(good); err != nil {
		t.Fatalf("well-formed image-data rejected: %v", err)
	}
	for _, bad := range []any{
		"not a struct",
		[]any{int32(1)},
		[]any{int64(1), int32(1), int32(4), true, int32(8), int32(4), []byte{1, 2, 3, 255}},
		[]any{int32(1), int32(1), int32(4), true, int32(8), int32(4), "pixels"},
	} {
		if _, err := DecodeImageData(bad); !errors.Is(err, ErrImageData) {
			t.Errorf("DecodeImageData(%#v) err = %v, want ErrImageData", bad, err)
		}
	}
}

// FuzzDecodePixels feeds arbitrary descriptions and buffers to the decoder,
// which has to read another process's idea of where its pixels are without
// ever reading past the end of them.
func FuzzDecodePixels(f *testing.F) {
	f.Add(int32(2), int32(2), int32(8), true, int32(8), int32(4), make([]byte, 16))
	f.Add(int32(1), int32(2), int32(4), false, int32(8), int32(3), make([]byte, 7))
	f.Add(int32(3), int32(1), int32(1<<30), true, int32(8), int32(4), make([]byte, 12))
	f.Fuzz(func(t *testing.T, w, h, stride int32, alpha bool, bps, channels int32, data []byte) {
		img, err := decodePixels(int(w), int(h), int(stride), alpha, int(bps), int(channels), data)
		if err != nil {
			return
		}
		if b := img.Bounds(); b.Dx() != int(w) || b.Dy() != int(h) {
			t.Fatalf("decoded %v for %dx%d", b, w, h)
		}
		for i := 0; i+3 < len(img.Pix); i += 4 {
			a := img.Pix[i+3]
			if img.Pix[i] > a || img.Pix[i+1] > a || img.Pix[i+2] > a {
				t.Fatalf("pixel %v is not premultiplied", img.Pix[i:i+4])
			}
		}
	})
}

func writePNG(t *testing.T, w, h int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "icon.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if encErr := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, w, h))); encErr != nil {
		t.Fatal(encErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return path
}

func TestLoadImageFileReadsPNG(t *testing.T) {
	img, err := LoadImageFile(writePNG(t, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("bounds = %v, want 3x2", b)
	}
}

func TestLoadImageFileRejects(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "icon.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"svg":           svg,
		"directory":     dir,
		"missing":       filepath.Join(dir, "nope.png"),
		"huge declared": writePNG(t, maxImageFileSide+1, 1),
	} {
		if _, err := LoadImageFile(path); err == nil {
			t.Errorf("%s: loaded, want an error", name)
		}
	}
}

// The everyday failures -- no such file, a format not understood -- stay
// recognisable through the wrapping, so the daemon can fall back quietly
// for them and still report a file that is really broken.
func TestLoadImageFileErrorsKeepTheirCause(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "icon.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadImageFile(filepath.Join(dir, "nope.png")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}
	if _, err := LoadImageFile(svg); !errors.Is(err, image.ErrFormat) {
		t.Errorf("svg: err = %v, want image.ErrFormat", err)
	}
	truncated := filepath.Join(dir, "cut.png")
	whole, err := os.ReadFile(writePNG(t, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(truncated, whole[:len(whole)/2], 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, err := LoadImageFile(truncated); err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, image.ErrFormat) {
		t.Errorf("truncated PNG: err = %v, want a failure that is neither missing nor unknown format", err)
	}
}

func TestLoadImageFileDoesNotHangOnAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadImageFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO loaded as an image")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadImageFile blocked opening a FIFO")
	}
}
