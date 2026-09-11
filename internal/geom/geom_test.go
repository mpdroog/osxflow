package geom

import "testing"

func TestI16Clamps(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int16
	}{
		{0, 0}, {100, 100}, {-100, -100},
		{32767, 32767}, {32768, 32767}, {1 << 40, 32767},
		{-32768, -32768}, {-32769, -32768}, {-(1 << 40), -32768},
	} {
		if got := I16(tc.in); got != tc.want {
			t.Errorf("I16(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestU16Clamps(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want uint16
	}{
		{0, 0}, {100, 100}, {65535, 65535}, {65536, 65535}, {1 << 40, 65535},
		{-1, 0}, {-(1 << 40), 0},
	} {
		if got := U16(tc.in); got != tc.want {
			t.Errorf("U16(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestU32Clamps(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want uint32
	}{
		{0, 0}, {100, 100}, {1<<32 - 1, 1<<32 - 1}, {1 << 32, 1<<32 - 1}, {-1, 0},
	} {
		if got := U32(tc.in); got != tc.want {
			t.Errorf("U32(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestI32AsU32RoundTrips checks the reinterpretation a ConfigureWindow
// value list needs: a negative coordinate has to survive as the same
// two's-complement word the server reads back as negative.
func TestI32AsU32RoundTrips(t *testing.T) {
	for _, in := range []int{0, 1, 1000, -1, -1000, 32767, -32768} {
		got := int32(I32AsU32(in))
		if int(got) != in {
			t.Errorf("I32AsU32(%d) came back as %d", in, got)
		}
	}
	// Out of range still clamps rather than wrapping to a nonsense
	// position on the far side of the screen.
	if got := int32(I32AsU32(1 << 20)); got != 32767 {
		t.Errorf("I32AsU32(1<<20) = %d, want the clamped 32767", got)
	}
}

func FuzzI16(f *testing.F) {
	f.Add(0)
	f.Add(1 << 40)
	f.Fuzz(func(t *testing.T, v int) {
		got := int(I16(v))
		if got > maxInt16 || got < minInt16 {
			t.Fatalf("I16(%d) = %d, outside the int16 range", v, got)
		}
		if v >= minInt16 && v <= maxInt16 && got != v {
			t.Fatalf("I16(%d) = %d, should have been exact", v, got)
		}
	})
}

func FuzzU16(f *testing.F) {
	f.Add(0)
	f.Add(-1)
	f.Fuzz(func(t *testing.T, v int) {
		got := int(U16(v))
		if got < 0 || got > maxUint16 {
			t.Fatalf("U16(%d) = %d, outside the uint16 range", v, got)
		}
		if v >= 0 && v <= maxUint16 && got != v {
			t.Fatalf("U16(%d) = %d, should have been exact", v, got)
		}
	})
}
