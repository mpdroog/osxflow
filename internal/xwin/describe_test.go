package xwin

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// fakeProps serves canned property replies, standing in for a display.
type fakeProps struct {
	// props maps window -> property name -> reply. A missing entry is an
	// unset property.
	props map[xproto.Window]map[string]*xproto.GetPropertyReply

	// errs maps window -> property name -> the error reading it returns,
	// taking precedence over props.
	errs map[xproto.Window]map[string]error

	// gone is the set of windows that have been destroyed.
	gone map[xproto.Window]bool

	atoms map[string]xproto.Atom
	names map[xproto.Atom]string
}

const (
	atomSkipTaskbar xproto.Atom = 500
	atomTypeNormal  xproto.Atom = 501
	atomTypeDock    xproto.Atom = 502
	atomBogus       xproto.Atom = 999
)

func newFakeProps() *fakeProps {
	return &fakeProps{
		props: map[xproto.Window]map[string]*xproto.GetPropertyReply{},
		errs:  map[xproto.Window]map[string]error{},
		gone:  map[xproto.Window]bool{},
		atoms: map[string]xproto.Atom{"_NET_WM_STATE_SKIP_TASKBAR": atomSkipTaskbar},
		names: map[xproto.Atom]string{
			atomSkipTaskbar: "_NET_WM_STATE_SKIP_TASKBAR",
			atomTypeNormal:  "_NET_WM_WINDOW_TYPE_NORMAL",
			atomTypeDock:    "_NET_WM_WINDOW_TYPE_DOCK",
		},
	}
}

func (f *fakeProps) set(win xproto.Window, name string, r *xproto.GetPropertyReply) {
	if f.props[win] == nil {
		f.props[win] = map[string]*xproto.GetPropertyReply{}
	}
	f.props[win][name] = r
}

func (f *fakeProps) fail(win xproto.Window, name string, err error) {
	if f.errs[win] == nil {
		f.errs[win] = map[string]error{}
	}
	f.errs[win][name] = err
}

// Get mirrors GetProperty's error contract.
func (f *fakeProps) Get(win xproto.Window, name string) (*xproto.GetPropertyReply, error) {
	if f.gone[win] {
		return nil, replyError(name, win, xproto.WindowError{NiceName: "Window", BadValue: uint32(win)})
	}
	if err := f.errs[win][name]; err != nil {
		return nil, err
	}
	r, ok := f.props[win][name]
	if !ok {
		return nil, fmt.Errorf("%s on window 0x%x: %w", name, win, ErrPropUnset)
	}
	return r, nil
}

func (f *fakeProps) Atom(name string) (xproto.Atom, error) {
	a, ok := f.atoms[name]
	if !ok {
		return 0, fmt.Errorf("fake: no atom %s", name)
	}
	return a, nil
}

func (f *fakeProps) AtomName(atom xproto.Atom) (string, error) {
	name, ok := f.names[atom]
	if !ok {
		return "", fmt.Errorf("naming atom %d: %w", atom, xproto.AtomError{NiceName: "Atom", BadValue: uint32(atom)})
	}
	return name, nil
}

// clientList sets the root window's stacking list.
func (f *fakeProps) clientList(ids ...xproto.Window) {
	vals := make([]uint32, len(ids))
	for i, id := range ids {
		vals[i] = uint32(id)
	}
	f.set(rootWin, "_NET_CLIENT_LIST_STACKING", card32Reply(vals...))
	f.set(rootWin, "_NET_CLIENT_LIST", card32Reply(vals...))
}

// fullWindow gives win every property a well-behaved client sets.
func (f *fakeProps) fullWindow(win xproto.Window, pid uint32, instance, class string) {
	f.set(win, "_NET_WM_PID", card32Reply(pid))
	f.set(win, "WM_CLASS", strReply(instance+"\x00"+class+"\x00"))
	f.set(win, "_NET_WM_WINDOW_TYPE", card32Reply(uint32(atomTypeNormal)))
	f.set(win, "_NET_WM_STATE", card32Reply())
	f.set(win, "_NET_WM_NAME", strReply(instance+" window"))
}

const rootWin xproto.Window = 1

// testX11 builds the X11 server over fake properties, collecting what it
// logs.
func testX11(f *fakeProps) (x *x11, logged *[]string) {
	var lines []string
	x = &x11{
		props:    f,
		root:     rootWin,
		reported: make(map[propKey]bool),
		logf: func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	}
	return x, &lines
}

func TestDescribeReadsEveryProperty(t *testing.T) {
	f := newFakeProps()
	f.fullWindow(0x10, 4312, "ghostty", "com.mitchellh.ghostty")
	f.set(0x10, "_NET_WM_STATE", card32Reply(123, uint32(atomSkipTaskbar)))
	x, logged := testX11(f)

	w, err := x.describe(0x10)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	want := Window{ID: 0x10, PID: 4312, Instance: "ghostty", Class: "com.mitchellh.ghostty",
		Title: "ghostty window", Type: "NORMAL", SkipTaskbar: true}
	if w != want {
		t.Errorf("describe = %+v, want %+v", w, want)
	}
	if len(*logged) != 0 {
		t.Errorf("logged %q for a well-formed window", *logged)
	}
}

func TestDescribeAbsentPropertiesAreSilent(t *testing.T) {
	f := newFakeProps()
	x, logged := testX11(f)
	w, err := x.describe(0x10)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if w != (Window{ID: 0x10}) {
		t.Errorf("describe = %+v, want only the id", w)
	}
	if len(*logged) != 0 {
		t.Errorf("logged %q for properties that are merely absent", *logged)
	}
}

// Each malformed property costs its own field and is logged; the other
// fields are still read. The zero-length _NET_WM_PID case is the one that
// used to panic the dock inside xgb.Get32.
func TestDescribeMalformedPropertyKeepsTheRest(t *testing.T) {
	for _, tc := range []struct {
		prop  string
		reply *xproto.GetPropertyReply
		check func(w Window) bool
	}{
		{"_NET_WM_PID", card32Reply(), func(w Window) bool { return w.PID == 0 }},
		{"_NET_WM_PID", strReply("4312"), func(w Window) bool { return w.PID == 0 }},
		{"WM_CLASS", strReply("a\x00b\x00c\x00"), func(w Window) bool { return w.Instance == "" && w.Class == "" }},
		{"WM_CLASS", card32Reply(1), func(w Window) bool { return w.Class == "" }},
		{"_NET_WM_WINDOW_TYPE", card32Reply(uint32(atomBogus)), func(w Window) bool { return w.Type == "" }},
		{"_NET_WM_STATE", strReply("x"), func(w Window) bool { return !w.SkipTaskbar }},
		{"_NET_WM_NAME", card32Reply(7), func(w Window) bool { return w.Title == "" }},
	} {
		t.Run(tc.prop, func(t *testing.T) {
			f := newFakeProps()
			f.fullWindow(0x10, 4312, "ghostty", "com.mitchellh.ghostty")
			f.set(0x10, tc.prop, tc.reply)
			x, logged := testX11(f)

			w, err := x.describe(0x10)
			if err != nil {
				t.Fatalf("describe returned %v for a malformed property; want it logged", err)
			}
			if !tc.check(w) {
				t.Errorf("describe = %+v; the malformed %s leaked into its field", w, tc.prop)
			}
			if tc.prop != "_NET_WM_PID" && w.PID != 4312 {
				t.Errorf("PID = %d, want 4312 read despite the broken %s", w.PID, tc.prop)
			}
			if tc.prop != "WM_CLASS" && w.Class != "com.mitchellh.ghostty" {
				t.Errorf("Class = %q, want it read despite the broken %s", w.Class, tc.prop)
			}
			if len(*logged) != 1 || !strings.Contains((*logged)[0], tc.prop) {
				t.Errorf("logged %q, want one line naming %s", *logged, tc.prop)
			}
		})
	}
}

func TestDescribeTitleFallsBackToWMName(t *testing.T) {
	for name, netName := range map[string]*xproto.GetPropertyReply{
		"unset": nil,
		"empty": strReply(""),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeProps()
			if netName != nil {
				f.set(0x10, "_NET_WM_NAME", netName)
			}
			f.set(0x10, "WM_NAME", strReply("xterm"))
			x, _ := testX11(f)
			w, err := x.describe(0x10)
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			if w.Title != "xterm" {
				t.Errorf("Title = %q, want the WM_NAME fallback", w.Title)
			}
		})
	}
}

// Only a failure of X itself -- not a window's own properties -- fails the
// enumeration.
func TestDescribeReturnsXFailures(t *testing.T) {
	broken := errors.New("connection reset")
	f := newFakeProps()
	f.fullWindow(0x10, 1, "a", "a")
	f.fail(0x10, "WM_CLASS", broken)
	x, _ := testX11(f)
	if _, err := x.describe(0x10); !errors.Is(err, broken) {
		t.Errorf("describe error = %v, want the X failure", err)
	}
}

func TestWindowsSkipsVanishedWindows(t *testing.T) {
	f := newFakeProps()
	f.clientList(0x10, 0x20, 0x30)
	f.fullWindow(0x10, 1, "a", "a")
	f.fullWindow(0x30, 3, "c", "c")
	f.gone[0x20] = true // closed between the list and the property reads
	x, logged := testX11(f)

	wins, err := x.Windows()
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if len(wins) != 2 || wins[0].ID != 0x10 || wins[1].ID != 0x30 {
		t.Errorf("Windows = %+v, want 0x10 and 0x30 in order", wins)
	}
	if len(*logged) != 0 {
		t.Errorf("logged %q for a window that merely closed", *logged)
	}
}

func TestWindowsReturnsXFailures(t *testing.T) {
	broken := errors.New("connection reset")
	f := newFakeProps()
	f.clientList(0x10)
	f.fail(0x10, "_NET_WM_PID", broken)
	x, _ := testX11(f)
	if _, err := x.Windows(); !errors.Is(err, broken) {
		t.Errorf("Windows error = %v, want the X failure", err)
	}
}

// A malformed window must not fail Windows: the launcher reads any error
// as "cannot tell" and would start a second copy of the app.
func TestWindowsMalformedIsLoggedOncePerWindow(t *testing.T) {
	f := newFakeProps()
	f.clientList(0x10)
	f.fullWindow(0x10, 1, "a", "a")
	f.set(0x10, "_NET_WM_PID", card32Reply())
	x, logged := testX11(f)

	for range 3 {
		if _, err := x.Windows(); err != nil {
			t.Fatalf("Windows: %v", err)
		}
	}
	if len(*logged) != 1 {
		t.Fatalf("logged %d lines over three enumerations, want 1: %q", len(*logged), *logged)
	}

	// Once the window is gone its report is forgotten, so a new window
	// that reuses the id is reported afresh.
	f.clientList()
	if _, err := x.Windows(); err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if len(x.reported) != 0 {
		t.Errorf("reported = %v after the window closed, want it pruned", x.reported)
	}
	f.clientList(0x10)
	if _, err := x.Windows(); err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if len(*logged) != 2 {
		t.Errorf("logged %d lines, want the reused id reported again", len(*logged))
	}
}

func TestClientListStackingFallback(t *testing.T) {
	t.Run("unset falls back quietly", func(t *testing.T) {
		f := newFakeProps()
		f.set(rootWin, "_NET_CLIENT_LIST", card32Reply(0x10))
		x, logged := testX11(f)
		wins, err := x.Windows()
		if err != nil || len(wins) != 1 {
			t.Fatalf("Windows = %v, %v; want one window", wins, err)
		}
		if len(*logged) != 0 {
			t.Errorf("logged %q for a window manager without a stacking list", *logged)
		}
	})

	t.Run("broken is logged once", func(t *testing.T) {
		f := newFakeProps()
		f.set(rootWin, "_NET_CLIENT_LIST", card32Reply(0x10))
		f.set(rootWin, "_NET_CLIENT_LIST_STACKING", strReply("junk"))
		x, logged := testX11(f)
		for range 2 {
			wins, err := x.Windows()
			if err != nil || len(wins) != 1 {
				t.Fatalf("Windows = %v, %v; want one window", wins, err)
			}
		}
		if len(*logged) != 1 || !strings.Contains((*logged)[0], "_NET_CLIENT_LIST_STACKING") {
			t.Errorf("logged %q, want one line about the stacking list", *logged)
		}
	})

	t.Run("both failing reports both", func(t *testing.T) {
		stackErr, listErr := errors.New("stacking broke"), errors.New("list broke")
		f := newFakeProps()
		f.fail(rootWin, "_NET_CLIENT_LIST_STACKING", stackErr)
		f.fail(rootWin, "_NET_CLIENT_LIST", listErr)
		x, _ := testX11(f)
		_, err := x.Windows()
		if !errors.Is(err, stackErr) || !errors.Is(err, listErr) {
			t.Errorf("Windows error = %v, want both failures", err)
		}
	})
}
