package main

import (
	"image/color"
)

// menuWidth is the menu's width in logical pixels.
const menuWidth = 300

// The tray icon's colours. It sits on the dark panel; the menu's own
// palette lives in internal/menu.
var (
	colIconOn  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colIconDim = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x5a}
	colIconOff = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x30}
)
