package xwin

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// card32Reply builds the reply a client's format-32 property produces.
func card32Reply(vals ...uint32) *xproto.GetPropertyReply {
	buf := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[4*i:], v)
	}
	return &xproto.GetPropertyReply{Format: 32, Type: xproto.AtomCardinal, ValueLen: uint32(len(vals)), Value: buf}
}

// strReply builds the reply a client's format-8 property produces.
func strReply(s string) *xproto.GetPropertyReply {
	return &xproto.GetPropertyReply{Format: 8, Type: xproto.AtomString, ValueLen: uint32(len(s)), Value: []byte(s)}
}

func TestDecodeCard32(t *testing.T) {
	for _, tc := range []struct {
		name      string
		r         *xproto.GetPropertyReply
		want      uint32
		malformed bool
	}{
		{"one value", card32Reply(4312), 4312, false},
		{"first of several", card32Reply(7, 8), 7, false},
		{"largest value", card32Reply(0xffffffff), 0xffffffff, false},
		// The crash this package exists to prevent: xprop.PropValNum checks
		// only the format and hands an empty value to xgb.Get32.
		{"format 32, no values", card32Reply(), 0, true},
		{"wrong format", strReply("4312"), 0, true},
		{"claims more than it holds", &xproto.GetPropertyReply{Format: 32, ValueLen: 2, Value: make([]byte, 4)}, 0, true},
		{"claims a value it lacks", &xproto.GetPropertyReply{Format: 32, ValueLen: 1, Value: []byte{1, 2}}, 0, true},
		{"nil reply", nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeCard32(tc.r)
			if tc.malformed {
				if !errors.Is(err, ErrPropMalformed) {
					t.Fatalf("DecodeCard32 = %d, %v; want ErrPropMalformed", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("DecodeCard32 = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestDecodeLists(t *testing.T) {
	wins, err := DecodeWindows(card32Reply(0x400001, 0x400002))
	if err != nil || !reflect.DeepEqual(wins, []xproto.Window{0x400001, 0x400002}) {
		t.Errorf("DecodeWindows = %v, %v", wins, err)
	}
	// An empty list is a valid list: no clients, no states.
	if empty, emptyErr := DecodeWindows(card32Reply()); emptyErr != nil || len(empty) != 0 {
		t.Errorf("DecodeWindows(empty) = %v, %v; want empty, nil", empty, emptyErr)
	}
	atoms, err := DecodeAtoms(card32Reply(300, 301))
	if err != nil || !reflect.DeepEqual(atoms, []xproto.Atom{300, 301}) {
		t.Errorf("DecodeAtoms = %v, %v", atoms, err)
	}
	for name, r := range map[string]*xproto.GetPropertyReply{
		"format 8":  strReply("abcd"),
		"format 16": {Format: 16, ValueLen: 2, Value: make([]byte, 4)},
		"short":     {Format: 32, ValueLen: 3, Value: make([]byte, 8)},
	} {
		if _, err := DecodeAtoms(r); !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeAtoms(%s) error = %v, want ErrPropMalformed", name, err)
		}
		if _, err := DecodeWindows(r); !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeWindows(%s) error = %v, want ErrPropMalformed", name, err)
		}
	}
}

func TestDecodeStrings(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"ghostty\x00com.mitchellh.ghostty\x00", []string{"ghostty", "com.mitchellh.ghostty"}},
		{"Navigator\x00firefox", []string{"Navigator", "firefox"}},
		{"one", []string{"one"}},
		{"\x00", []string{""}},
		{"a\x00\x00b", []string{"a", "", "b"}},
		{"", nil},
	} {
		got, err := DecodeStrings(strReply(tc.in))
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("DecodeStrings(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := DecodeStrings(card32Reply(1)); !errors.Is(err, ErrPropMalformed) {
		t.Errorf("DecodeStrings(format 32) error = %v, want ErrPropMalformed", err)
	}
}

func TestDecodeString(t *testing.T) {
	if got, err := DecodeString(strReply("a title")); err != nil || got != "a title" {
		t.Errorf("DecodeString = %q, %v", got, err)
	}
	// Only ValueLen bytes are the value; xgb pads the buffer to four.
	r := &xproto.GetPropertyReply{Format: 8, ValueLen: 2, Value: []byte("ab\x00\x00")}
	if got, err := DecodeString(r); err != nil || got != "ab" {
		t.Errorf("DecodeString(padded) = %q, %v; want \"ab\"", got, err)
	}
	for name, r := range map[string]*xproto.GetPropertyReply{
		"format 32": card32Reply(1),
		"short":     {Format: 8, ValueLen: 5, Value: []byte("ab")},
		"nil":       nil,
	} {
		if _, err := DecodeString(r); !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeString(%s) error = %v, want ErrPropMalformed", name, err)
		}
	}
}

// A BadWindow must be recognisable both ways: as the sentinel that says
// "skip this window", and as the X error it really is.
func TestReplyErrorKeepsTheXError(t *testing.T) {
	bad := xproto.WindowError{NiceName: "Window", BadValue: 0x400001}
	err := replyError("_NET_WM_PID", 0x400001, bad)
	if !errors.Is(err, ErrWindowGone) {
		t.Errorf("BadWindow error %v is not ErrWindowGone", err)
	}
	var got xproto.WindowError
	if !errors.As(err, &got) || got.BadValue != 0x400001 {
		t.Errorf("BadWindow error %v lost the xproto.WindowError", err)
	}

	other := xproto.AtomError{NiceName: "Atom"}
	err = replyError("_NET_WM_PID", 0x400001, other)
	if errors.Is(err, ErrWindowGone) {
		t.Errorf("BadAtom error %v was classed as a vanished window", err)
	}
	var atomErr xproto.AtomError
	if !errors.As(err, &atomErr) {
		t.Errorf("BadAtom error %v lost the xproto.AtomError", err)
	}
}

// FuzzDecodeProperty feeds arbitrary replies to every decoder. Any client
// on the display can set any property to anything, so none of them may
// panic, and a success must mean exactly the values the reply claims.
func FuzzDecodeProperty(f *testing.F) {
	f.Add(byte(32), uint32(0), []byte{})
	f.Add(byte(32), uint32(1), []byte{1, 2, 3, 4})
	f.Add(byte(32), uint32(2), []byte{1, 2, 3, 4})
	f.Add(byte(8), uint32(5), []byte("a\x00b\x00c"))
	f.Add(byte(16), uint32(1), []byte{1, 2})
	f.Add(byte(0), uint32(0), []byte(nil))
	f.Fuzz(func(t *testing.T, format byte, valueLen uint32, value []byte) {
		r := &xproto.GetPropertyReply{Format: format, Type: xproto.AtomCardinal, ValueLen: valueLen, Value: value}

		if vals, err := DecodeCard32s(r); err == nil {
			if uint32(len(vals)) != valueLen {
				t.Errorf("DecodeCard32s returned %d values for ValueLen %d", len(vals), valueLen)
			}
		} else if !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeCard32s error %v is not ErrPropMalformed", err)
		}
		if _, err := DecodeCard32(r); err != nil && !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeCard32 error %v is not ErrPropMalformed", err)
		}
		if _, err := DecodeAtoms(r); err != nil && !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeAtoms error %v is not ErrPropMalformed", err)
		}
		if _, err := DecodeWindows(r); err != nil && !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeWindows error %v is not ErrPropMalformed", err)
		}
		if s, err := DecodeString(r); err == nil {
			if uint32(len(s)) != valueLen {
				t.Errorf("DecodeString returned %d bytes for ValueLen %d", len(s), valueLen)
			}
		} else if !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeString error %v is not ErrPropMalformed", err)
		}
		if _, err := DecodeStrings(r); err != nil && !errors.Is(err, ErrPropMalformed) {
			t.Errorf("DecodeStrings error %v is not ErrPropMalformed", err)
		}
	})
}
