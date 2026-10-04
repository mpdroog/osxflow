package menu

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jezek/xgb/xproto"
)

func TestDecodeKey(t *testing.T) {
	const (
		shift = xproto.ModMaskShift
		caps  = xproto.ModMaskLock
		ctrl  = xproto.ModMaskControl
		alt   = xproto.ModMask1
		super = xproto.ModMask4
		num   = modNumLock
		// The second keyboard group, where XKB reports it.
		group2 = 1 << 13
	)
	var (
		a       = []xproto.Keysym{'a', 'A'}
		seven   = []xproto.Keysym{'7', '&'}
		lone    = []xproto.Keysym{'q'}
		kp7     = []xproto.Keysym{0xff95, 0xffb7}                   // KP_Home, KP_7
		twoLang = []xproto.Keysym{'a', 'A', 0x010003b1, 0x01000391} // a A α Α
	)
	for _, tc := range []struct {
		name  string
		syms  []xproto.Keysym
		state uint16
		act   keyAction
		r     rune
	}{
		{"letter", a, 0, keyInsert, 'a'},
		{"shifted letter", a, shift, keyInsert, 'A'},
		{"caps lock", a, caps, keyInsert, 'A'},
		{"caps lock and shift cancel", a, caps | shift, keyInsert, 'a'},
		{"digit", seven, 0, keyInsert, '7'},
		{"shifted digit", seven, shift, keyInsert, '&'},
		{"caps lock leaves digits alone", seven, caps, keyInsert, '7'},
		{"num lock leaves letters alone", a, num, keyInsert, 'a'},
		{"one keysym, shifted", lone, shift, keyInsert, 'Q'},
		{"keypad with num lock", kp7, num, keyInsert, '7'},
		{"keypad without", kp7, 0, keyNone, 0},
		{"second group", twoLang, group2, keyInsert, 'α'},
		{"second group, shifted", twoLang, group2 | shift, keyInsert, 'Α'},
		{"second group on a key with one", a, group2, keyInsert, 'a'},
		{"space", []xproto.Keysym{' '}, 0, keyInsert, ' '},
		{"backspace", []xproto.Keysym{keysymBackSpace}, 0, keyBackspace, 0},
		{"return", []xproto.Keysym{keysymReturn}, 0, keySubmit, 0},
		{"keypad enter", []xproto.Keysym{keysymKPEnter}, num, keySubmit, 0},
		{"ctrl+u", []xproto.Keysym{'u', 'U'}, ctrl, keyClear, 0},
		{"ctrl+h", []xproto.Keysym{'h', 'H'}, ctrl, keyBackspace, 0},
		{"ctrl+a", a, ctrl, keyNone, 0},
		{"alt+a", a, alt, keyNone, 0},
		{"super+a", a, super, keyNone, 0},
		{"shift alone", []xproto.Keysym{0xffe1}, shift, keyNone, 0},
		{"an arrow", []xproto.Keysym{0xff51}, 0, keyNone, 0},
		{"no keysyms", nil, 0, keyNone, 0},
	} {
		act, r := decodeKey(tc.syms, tc.state)
		if act != tc.act || r != tc.r {
			t.Errorf("%s: decodeKey = %d %q, want %d %q", tc.name, act, r, tc.act, tc.r)
		}
	}
}

func FuzzDecodeKey(f *testing.F) {
	f.Add(uint32('a'), uint32('A'), uint32(0), uint32(0), uint16(0))
	f.Add(uint32(0xff95), uint32(0xffb7), uint32(0), uint32(0), uint16(modNumLock))
	f.Add(uint32(0x0110ffff), uint32(0x0100d800), uint32(0xffffffff), uint32(1), uint16(0xffff))
	f.Fuzz(func(t *testing.T, s0, s1, s2, s3 uint32, state uint16) {
		syms := []xproto.Keysym{xproto.Keysym(s0), xproto.Keysym(s1), xproto.Keysym(s2), xproto.Keysym(s3)}
		for n := range len(syms) + 1 {
			act, r := decodeKey(syms[:n], state)
			if act != keyInsert {
				if r != 0 {
					t.Fatalf("action %d came with the character %q", act, r)
				}
				continue
			}
			if !utf8.ValidRune(r) || r < 0x20 || r == 0x7f {
				t.Fatalf("typed %U, which is not a character to put in a field", r)
			}
		}
	})
}

func TestKeymapOf(t *testing.T) {
	km := &keymap{first: 8, per: 2, syms: []xproto.Keysym{'a', 'A', 'b', 'B'}}
	if got := km.of(9); len(got) != 2 || got[0] != 'b' || got[1] != 'B' {
		t.Errorf("of(9) = %v, want b B", got)
	}
	for _, code := range []xproto.Keycode{0, 7, 10, 255} {
		if got := km.of(code); got != nil {
			t.Errorf("of(%d) = %v, want nothing: the key is not in the map", code, got)
		}
	}
	// The map a failed read leaves behind, and none at all.
	if (&keymap{}).of(9) != nil || (*keymap)(nil).of(9) != nil {
		t.Error("an empty keymap has keys")
	}
}

func TestEdit(t *testing.T) {
	full := strings.Repeat("é", maxInput)
	for _, tc := range []struct {
		name    string
		text    string
		act     keyAction
		r       rune
		want    string
		changed bool
	}{
		{"insert", "ab", keyInsert, 'c', "abc", true},
		{"insert into a full field", full, keyInsert, 'c', full, false},
		{"backspace", "ab", keyBackspace, 0, "a", true},
		{"backspace takes a whole character", "aé", keyBackspace, 0, "a", true},
		{"backspace on nothing", "", keyBackspace, 0, "", false},
		{"clear", "ab", keyClear, 0, "", true},
		{"clear nothing", "", keyClear, 0, "", false},
		{"submit leaves the text", "ab", keySubmit, 0, "ab", false},
		{"nothing", "ab", keyNone, 0, "ab", false},
	} {
		got, changed := edit(tc.text, tc.act, tc.r)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: edit = %q %t, want %q %t", tc.name, got, changed, tc.want, tc.changed)
		}
	}
}

func FuzzEdit(f *testing.F) {
	f.Add("pass", int(keyInsert), 'w')
	f.Add("\xff\xfe", int(keyBackspace), rune(0))
	f.Add("", int(keyClear), rune(0))
	f.Fuzz(func(t *testing.T, text string, act int, r rune) {
		got, changed := edit(text, keyAction(act), r)
		if changed == (got == text) {
			t.Fatalf("edit(%q, %d, %q) = %q, changed %t", text, act, r, got, changed)
		}
		if keyAction(act) == keyInsert && utf8.RuneCountInString(got) > max(maxInput, utf8.RuneCountInString(text)) {
			t.Fatalf("typing grew the text past %d characters", maxInput)
		}
		if keyAction(act) != keyInsert && len(got) > len(text) {
			t.Fatalf("action %d grew the text", act)
		}
	})
}

func TestInputRow(t *testing.T) {
	if got := inputRow([]Row{{Kind: Toggle}, {Kind: Item}}); got != -1 {
		t.Errorf("inputRow without one = %d", got)
	}
	if got := inputRow([]Row{{Kind: Toggle}, {Kind: Input}, {Kind: Input}}); got != 1 {
		t.Errorf("inputRow = %d, want the first, 1", got)
	}
}

func TestTail(t *testing.T) {
	for _, tc := range []struct {
		s     string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"too long by far", 3, "far"},
		{"héllo", 4, "éllo"},
		{"anything", 0, ""},
		{"", 5, ""},
	} {
		if got := tail(tc.s, tc.width, utf8.RuneCountInString); got != tc.want {
			t.Errorf("tail(%q, %d) = %q, want %q", tc.s, tc.width, got, tc.want)
		}
	}
}
