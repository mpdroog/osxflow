package ui

// Turning X key events into text and commands.
//
// keybind.LookupString is not used for text: it returns keysym *names*, so
// pressing the space bar yields "space" and a full stop yields "period".
// Those are the right answer for describing a binding and the wrong one
// for typing into a field, so the keysym is taken raw and interpreted
// here.

import (
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/keybind"
)

// The keysyms that mean something other than a character. From
// keysymdef.h, which has not changed in thirty years.
const (
	ksBackSpace = 0xff08
	ksTab       = 0xff09
	ksReturn    = 0xff0d
	ksEscape    = 0xff1b
	ksHome      = 0xff50
	ksUp        = 0xff52
	ksPageUp    = 0xff55
	ksPageDown  = 0xff56
	ksEnd       = 0xff57
	ksDown      = 0xff54
	ksKPEnter   = 0xff8d
	ksKP0       = 0xffb0
	ksKP9       = 0xffb9
	ksDelete    = 0xffff
)

// action is what a key press means to the interface.
type action int

const (
	actNone action = iota
	actInsert
	actBackspace
	actDeleteWord
	actClear
	actUp
	actDown
	actPageUp
	actPageDown
	actTop
	actBottom
	actAccept
	actCancel
)

// interpret decodes a key press into an action and, for typing, the text
// it produced.
func interpret(xu *xgbutil.XUtil, state uint16, code xproto.Keycode) (act action, text string) {
	shift := state&xproto.ModMaskShift > 0
	lock := state&xproto.ModMaskLock > 0
	ctrl := state&xproto.ModMaskControl > 0

	plain := keybind.KeysymGet(xu, code, 0)
	shifted := keybind.KeysymGet(xu, code, 1)

	// Control combinations are commands, never text. They are checked
	// against the unshifted keysym so that Ctrl+Shift+U is still "clear".
	if ctrl {
		switch plain {
		case 'u':
			return actClear, ""
		case 'w':
			return actDeleteWord, ""
		case 'h':
			return actBackspace, ""
		case 'p':
			return actUp, ""
		case 'n', 'j':
			return actDown, ""
		case 'a':
			return actTop, ""
		case 'e':
			return actBottom, ""
		case 'c', 'g', '[':
			return actCancel, ""
		}
		return actNone, ""
	}

	switch plain {
	case ksEscape:
		return actCancel, ""
	case ksReturn, ksKPEnter:
		return actAccept, ""
	case ksBackSpace:
		return actBackspace, ""
	case ksUp:
		return actUp, ""
	case ksDown, ksTab:
		// Tab moves down rather than completing: with a ranked list the
		// next candidate is what "the other one" means.
		return actDown, ""
	case ksPageUp:
		return actPageUp, ""
	case ksPageDown:
		return actPageDown, ""
	case ksHome:
		return actTop, ""
	case ksEnd:
		return actBottom, ""
	case ksDelete:
		return actNone, ""
	}

	ks := plain
	if shift {
		ks = shifted
	}
	r, ok := keysymRune(ks)
	if !ok {
		return actNone, ""
	}
	// Caps Lock affects letters only, and inverts whatever Shift decided.
	if lock {
		if up, low := toUpper(r), toLower(r); up != low {
			if shift {
				r = low
			} else {
				r = up
			}
		}
	}
	return actInsert, string(r)
}

// keysymRune converts a keysym to the character it types, reporting false
// for keysyms that are not characters at all.
func keysymRune(ks xproto.Keysym) (rune, bool) {
	switch {
	case ks >= 0x20 && ks <= 0x7e:
		// Latin-1 and ASCII keysyms are their own code points.
		return rune(ks), true
	case ks >= 0xa0 && ks <= 0xff:
		return rune(ks), true
	case ks >= ksKP0 && ks <= ksKP9:
		// The numeric keypad, so that typing a sum with it works.
		return rune('0' + (ks - ksKP0)), true
	case ks >= 0x01000100 && ks <= 0x0110ffff:
		// The Unicode keysym range.
		return rune(ks - 0x01000000), true
	}
	return 0, false
}

// toUpper and toLower are ASCII-only on purpose: they exist to undo Caps
// Lock, and the full Unicode versions would also case-fold characters that
// Caps Lock never produced.
func toUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r - 'A' + 'a'
	}
	return r
}
