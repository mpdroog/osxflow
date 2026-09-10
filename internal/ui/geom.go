package ui

// The 16-bit narrowing X requires moved to internal/geom when the dock
// needed it too. These wrappers stay because this package reads better
// with short names in the middle of a request.

import "github.com/mpdroog/osxflow/internal/geom"

func i16(v int) int16  { return geom.I16(v) }
func u16(v int) uint16 { return geom.U16(v) }
func u32(v int) uint32 { return geom.U32(v) }
