// Package geom narrows Go's int arithmetic to the widths the X protocol
// uses.
//
// X11 geometry is 16-bit: coordinates are int16 and sizes uint16. Every
// value crossing into a request has to be narrowed, and a silent
// wraparound would put a window at a nonsensical position rather than
// failing -- a dock 40 pixels wide at the far edge of the screen, from an
// arithmetic slip nobody would think to look for.
//
// Clamping rather than erroring is the right call here: a display scale
// that produced an absurd size should still show something usable at the
// edge of the range, not refuse to start.
package geom

const (
	maxInt16  = 1<<15 - 1
	minInt16  = -(1 << 15)
	maxUint16 = 1<<16 - 1
	maxUint32 = 1<<32 - 1
)

// I16 clamps to the range of an X coordinate.
func I16(v int) int16 {
	switch {
	case v > maxInt16:
		return maxInt16
	case v < minInt16:
		return minInt16
	}
	return int16(v)
}

// U16 clamps to the range of an X size.
func U16(v int) uint16 {
	switch {
	case v > maxUint16:
		return maxUint16
	case v < 0:
		return 0
	}
	return uint16(v)
}

// U32 clamps to the range of an X property word.
func U32(v int) uint32 {
	switch {
	case v < 0:
		return 0
	case int64(v) > maxUint32:
		return maxUint32
	}
	return uint32(v)
}

// I32AsU32 reinterprets a signed coordinate as the unsigned word that
// ConfigureWindow's value list carries.
//
// The value list is []uint32 whatever the field means, and a negative x is
// legitimate: a dock wider than the screen, or one being slid off the
// bottom edge, has one. Two's complement is what the server expects, so
// the conversion is a reinterpretation rather than a range check.
func I32AsU32(v int) uint32 {
	return uint32(int32(I16(v))) //nolint:gosec // clamped to int16 by I16, so int32 cannot overflow
}
