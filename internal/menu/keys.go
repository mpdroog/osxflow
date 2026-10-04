package menu

// Typing into an Input row: turning a key press into what it does to the
// text. The keyboard mapping is the core protocol's, read raw, for the
// reason internal/ui gives: keybind's lookup returns keysym names, which
// describe a binding and are useless for typing.

import (
	"unicode/utf8"

	"github.com/jezek/xgb/xproto"
)

// The keysyms that mean something other than a character. From
// keysymdef.h.
const (
	keysymBackSpace = 0xff08
	keysymReturn    = 0xff0d
	keysymEscape    = 0xff1b
	keysymKPEnter   = 0xff8d
	keysymKP0       = 0xffb0
	keysymKP9       = 0xffb9
)

// modNumLock is where every stock keymap puts Num Lock.
const modNumLock = xproto.ModMask2

// maxInput is the most an Input holds, in characters. Twice the longest
// thing asked for so far, a 64-digit Wi-Fi key; a held-down key stops
// there rather than growing a string for as long as it is held.
const maxInput = 128

// keyAction is what a key press does to an Input.
type keyAction int

const (
	keyNone keyAction = iota
	keyInsert
	keyBackspace
	keyClear
	keySubmit
)

// keymap is the server's keyboard mapping: for each keycode from first on,
// per keysyms, one for each column.
type keymap struct {
	first xproto.Keycode
	per   int
	syms  []xproto.Keysym
}

// of returns the keysyms of one key, or nil for a keycode not in the map.
func (k *keymap) of(code xproto.Keycode) []xproto.Keysym {
	if k == nil || k.per <= 0 || code < k.first {
		return nil
	}
	start := int(code-k.first) * k.per
	if start+k.per > len(k.syms) {
		return nil
	}
	return k.syms[start : start+k.per]
}

// decodeKey turns a press of a key with keysyms syms, under the modifiers
// in state, into an action and, for keyInsert, the character typed.
//
// The columns are the core protocol's: two for each keyboard group, plain
// and shifted. The group in use is in bits 13 and 14 of state, which is
// where XKB puts it.
func decodeKey(syms []xproto.Keysym, state uint16) (act keyAction, r rune) {
	sym := func(col int) xproto.Keysym {
		if col < len(syms) {
			return syms[col]
		}
		return 0
	}
	base := int(state>>13) & 3 * 2
	if sym(base) == 0 {
		// A key the same in every layout is only filled in for the first.
		base = 0
	}
	plain, shifted := sym(base), sym(base+1)

	// Control combinations are commands, never text, and the ones here are
	// the shell's. Alt and Super belong to the window manager.
	if state&xproto.ModMaskControl != 0 {
		switch plain {
		case 'u', 'w':
			return keyClear, 0
		case 'h':
			return keyBackspace, 0
		}
		return keyNone, 0
	}
	if state&(xproto.ModMask1|xproto.ModMask4) != 0 {
		return keyNone, 0
	}

	switch plain {
	case keysymBackSpace:
		return keyBackspace, 0
	case keysymReturn, keysymKPEnter:
		return keySubmit, 0
	}

	shift := state&xproto.ModMaskShift != 0
	// The keypad's digits are in its shifted column, and Num Lock is what
	// selects them.
	if shifted >= keysymKP0 && shifted <= keysymKP9 {
		if state&modNumLock != 0 && !shift {
			return keyInsert, rune('0' + (shifted - keysymKP0))
		}
		return keyNone, 0
	}

	ks := plain
	if shift {
		ks = shifted
		if ks == 0 {
			// A key with one keysym is a letter given in lower case, and
			// Shift is expected to capitalise it.
			ks = plain
			if plain >= 'a' && plain <= 'z' {
				ks = plain - 'a' + 'A'
			}
		}
	}
	var ok bool
	if r, ok = keysymRune(ks); !ok {
		return keyNone, 0
	}
	// Caps Lock affects letters only, and inverts whatever Shift decided.
	if state&xproto.ModMaskLock != 0 {
		if up, low := asciiUpper(r), asciiLower(r); up != low {
			if shift {
				r = low
			} else {
				r = up
			}
		}
	}
	return keyInsert, r
}

// keysymRune converts a keysym to the character it types, reporting false
// for keysyms that are not characters at all.
func keysymRune(ks xproto.Keysym) (rune, bool) {
	switch {
	case ks >= 0x20 && ks <= 0x7e, ks >= 0xa0 && ks <= 0xff:
		// Latin-1 keysyms are their own code points.
		return rune(ks), true
	case ks >= 0x01000100 && ks <= 0x0110ffff:
		// The Unicode keysym range.
		r := rune(ks - 0x01000000)
		return r, utf8.ValidRune(r)
	}
	return 0, false
}

// asciiUpper and asciiLower are ASCII-only on purpose: they exist to apply
// Shift and undo Caps Lock, and the Unicode versions would also fold
// characters neither key ever produced.
func asciiUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}

func asciiLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r - 'A' + 'a'
	}
	return r
}

// edit applies an action to an Input's text and reports whether that
// changed it.
func edit(text string, act keyAction, r rune) (string, bool) {
	switch act {
	case keyInsert:
		if utf8.RuneCountInString(text) >= maxInput {
			return text, false
		}
		return text + string(r), true
	case keyBackspace:
		if text == "" {
			return text, false
		}
		_, size := utf8.DecodeLastRuneInString(text)
		return text[:len(text)-size], true
	case keyClear:
		return "", text != ""
	case keyNone, keySubmit:
	}
	return text, false
}

// inputRow is the row typing goes to: the first Input, or -1.
func inputRow(rows []Row) int {
	for i := range rows {
		if rows[i].Kind == Input {
			return i
		}
	}
	return -1
}

// tail cuts s from the front until measure says it fits in width, so a
// field too narrow for its text shows the end, where the typing is.
func tail(s string, width int, measure func(string) int) string {
	for s != "" && measure(s) > width {
		_, size := utf8.DecodeRuneInString(s)
		s = s[size:]
	}
	return s
}
