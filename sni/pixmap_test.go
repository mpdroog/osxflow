package sni

import (
	"image"
	"image/color"
	"testing"
)

func TestToImageRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 0x40, G: 0x20, B: 0x10, A: 0x80})
	img.SetRGBA(2, 1, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
	p := FromImage(img)
	back, err := ToImage(&p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range img.Pix {
		if d := int(img.Pix[i]) - int(back.Pix[i]); d < -1 || d > 1 {
			t.Fatalf("byte %d: %d, want %d", i, back.Pix[i], img.Pix[i])
		}
	}
}

func TestToImageRejects(t *testing.T) {
	for _, p := range []Pixmap{
		{Width: 0, Height: 1},
		{Width: -1, Height: 1, Pix: make([]byte, 4)},
		{Width: 2, Height: 2, Pix: make([]byte, 15)},
		{Width: 2000, Height: 1, Pix: make([]byte, 8000)},
	} {
		if _, err := ToImage(&p); err == nil {
			t.Errorf("ToImage(%dx%d, %d bytes) accepted it", p.Width, p.Height, len(p.Pix))
		}
	}
}

func square(side int32) Pixmap {
	return Pixmap{Width: side, Height: side, Pix: make([]byte, 4*side*side)}
}

func TestBest(t *testing.T) {
	for _, tc := range []struct {
		sides []int32
		size  int
		want  int
	}{
		{[]int32{16, 22, 32, 64}, 36, 64},
		{[]int32{16, 22, 32}, 36, 32},
		{[]int32{64, 48}, 36, 48},
		{[]int32{36, 48}, 36, 36},
	} {
		ps := make([]Pixmap, 0, len(tc.sides))
		for _, s := range tc.sides {
			ps = append(ps, square(s))
		}
		img, problems := Best(ps, tc.size)
		if img == nil || img.Bounds().Dx() != tc.want || len(problems) != 0 {
			t.Errorf("Best(%v, %d) picked %v (%v), want side %d", tc.sides, tc.size, img, problems, tc.want)
		}
	}
	img, problems := Best([]Pixmap{{Width: 5, Height: 5}}, 10)
	if img != nil || len(problems) != 2 {
		t.Errorf("Best of a broken pixmap = %v, %v", img, problems)
	}
}

func FuzzToImage(f *testing.F) {
	f.Add(int32(1), int32(1), []byte{1, 2, 3, 4})
	f.Add(int32(2), int32(1), []byte{1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, w, h int32, pix []byte) {
		img, err := ToImage(&Pixmap{Width: w, Height: h, Pix: pix})
		if err != nil {
			return
		}
		if img.Bounds().Dx() != int(w) || img.Bounds().Dy() != int(h) {
			t.Fatalf("%dx%d decoded as %v", w, h, img.Bounds())
		}
		for i := 0; i < len(img.Pix); i += 4 {
			a := img.Pix[i+3]
			if img.Pix[i] > a || img.Pix[i+1] > a || img.Pix[i+2] > a {
				t.Fatalf("pixel %d not premultiplied: %v", i/4, img.Pix[i:i+4])
			}
		}
	})
}
