package xsurface

import (
	"bytes"
	"testing"
)

func TestEncodeInto(t *testing.T) {
	src := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	dst := make([]byte, len(src))

	encodeInto(dst, src, false)
	if !bytes.Equal(dst, src) {
		t.Errorf("without a swap: %v, want %v", dst, src)
	}

	// Little-endian servers want B,G,R,A: red and blue trade places, green
	// and alpha stay.
	encodeInto(dst, src, true)
	if want := []byte{3, 2, 1, 4, 7, 6, 5, 8}; !bytes.Equal(dst, want) {
		t.Errorf("with a swap: %v, want %v", dst, want)
	}

	// A destination longer than the source is written only as far as the
	// source goes.
	long := bytes.Repeat([]byte{9}, 12)
	encodeInto(long, src, true)
	if !bytes.Equal(long[8:], []byte{9, 9, 9, 9}) {
		t.Errorf("wrote past the source: %v", long)
	}
}

func TestRowsPerRequest(t *testing.T) {
	for _, tc := range []struct {
		maxUnits uint16
		width    int
		want     int
	}{
		// 65535 units is 262140 bytes, less the overhead, over 2560*4.
		{65535, 2560, (65535*4 - 64) / (2560 * 4)},
		// A row wider than the whole budget still goes, one at a time.
		{100, 2560, 1},
		{4096, 1, (4096*4 - 64) / 4},
	} {
		if got := rowsPerRequest(tc.maxUnits, tc.width); got != tc.want {
			t.Errorf("rowsPerRequest(%d, %d) = %d, want %d", tc.maxUnits, tc.width, got, tc.want)
		}
		if got := rowsPerRequest(tc.maxUnits, tc.width); got < 1 {
			t.Errorf("rowsPerRequest(%d, %d) = %d, want at least 1", tc.maxUnits, tc.width, got)
		}
	}
}
