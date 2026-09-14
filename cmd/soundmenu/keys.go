package main

// The volume keys. The panel's PulseAudio plugin used to take them; with
// it gone, nothing else would, so this does -- and while the plugin still
// runs it holds them, and grabbing fails, which is logged and survivable.

import (
	"errors"
	"log"
	"math"

	"github.com/jezek/xgb/xproto"
)

type keyAction int

const (
	keyRaise keyAction = iota + 1
	keyLower
	keyMute
	keyMicMute
)

// volumeKeys are the XF86 keysyms the Mac's keyboard (through gokeyd)
// produces for its sound keys.
var volumeKeys = [...]struct {
	sym  xproto.Keysym
	name string
	act  keyAction
}{
	{0x1008ff13, "XF86AudioRaiseVolume", keyRaise},
	{0x1008ff11, "XF86AudioLowerVolume", keyLower},
	{0x1008ff12, "XF86AudioMute", keyMute},
	{0x1008ffb2, "XF86AudioMicMute", keyMicMute},
}

// volumeStep is one key press, or one notch of the wheel over the icon:
// the PulseAudio plugin's default, which the user is used to.
const volumeStep = 0.05

// nextVolume is the volume one step up or down from v, snapped to whole
// steps so a volume set by dragging falls back into line, and kept to 0-100%.
func nextVolume(v float64, up bool) float64 {
	if math.IsNaN(v) {
		v = 0
	}
	step := volumeStep
	if !up {
		step = -step
	}
	return math.Max(0, math.Min(1, math.Round((v+step)/volumeStep)*volumeStep))
}

// grabKeys takes the volume keys on the root window, with any modifiers,
// and records which keycode does what.
func (a *app) grabKeys() {
	a.keys = make(map[xproto.Keycode]keyAction)
	for _, k := range volumeKeys {
		codes, err := a.host.Keycodes(k.sym)
		if err != nil {
			log.Printf("%s: %v", k.name, err)
			continue
		}
		for _, code := range codes {
			err := xproto.GrabKeyChecked(a.conn, true, a.host.Screen.Root, xproto.ModMaskAny, code,
				xproto.GrabModeAsync, xproto.GrabModeAsync).Check()
			var taken xproto.AccessError
			switch {
			case errors.As(err, &taken):
				log.Printf("%s is taken by another program -- the panel's PulseAudio plugin, while it runs; the key stays with it", k.name)
			case err != nil:
				log.Printf("grabbing %s: %v", k.name, err)
			default:
				a.keys[code] = k.act
			}
		}
	}
}
