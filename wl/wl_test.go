package wl

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadStringDecodesPaddingAndNUL(t *testing.T) {
	// "foot" is 4 bytes + NUL = 5, padded to 8.
	b := make([]byte, 0, 12)
	b = binary.LittleEndian.AppendUint32(b, 5)
	b = append(b, "foot"...)
	b = append(b, 0, 0, 0, 0) // NUL + 3 padding
	b = binary.LittleEndian.AppendUint32(b, 0xdeadbeef)

	s, rest := readString(b)
	if s != "foot" {
		t.Errorf("got %q, want %q", s, "foot")
	}
	if len(rest) != 4 || binary.LittleEndian.Uint32(rest) != 0xdeadbeef {
		t.Errorf("padding consumed wrongly: %d bytes left", len(rest))
	}
}

func TestReadStringRejectsShortAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"truncated header", []byte{1, 2}},
		{"zero length", binary.LittleEndian.AppendUint32(nil, 0)},
		{"length past the end", binary.LittleEndian.AppendUint32(nil, 99)},
	} {
		if s, _ := readString(tc.in); s != "" {
			t.Errorf("%s: got %q, want empty", tc.name, s)
		}
	}
}

func TestArgStringMatchesTheWireFormat(t *testing.T) {
	var b []byte
	argString("wl_seat")(&b)
	// 4 length + 7 bytes + NUL = 12, already a multiple of 4.
	if len(b) != 12 {
		t.Fatalf("encoded length %d, want 12", len(b))
	}
	if got := binary.LittleEndian.Uint32(b[:4]); got != 8 {
		t.Errorf("length prefix %d, want 8 (7 + NUL)", got)
	}
	if b[11] != 0 {
		t.Error("string is not NUL-terminated")
	}
	if s, _ := readString(b); s != "wl_seat" {
		t.Errorf("round trip gave %q", s)
	}
}

func TestArgStringPadsToFourBytes(t *testing.T) {
	var b []byte
	argString("a")(&b) // 4 + 1 + NUL = 6, must pad to 8
	if len(b)%4 != 0 {
		t.Errorf("encoded length %d is not 4-byte aligned", len(b))
	}
}

// TestLiveCompositor is the one that matters: it talks to the compositor
// this session is running under. It is skipped off Wayland so the suite
// still passes on the Mint laptop.
func TestLiveCompositor(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("not a Wayland session")
	}
	c, err := Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Disconnect()

	tops, err := c.Toplevels()
	if err != nil {
		t.Fatalf("Toplevels: %v", err)
	}
	t.Logf("compositor reports %d toplevel(s)", len(tops))
	for _, w := range tops {
		t.Logf("  id=0x%x app_id=%q activated=%v minimized=%v outputs=%v title=%q",
			w.ID, w.AppID, w.Activated, w.Minimized, w.Outputs, w.Title)
	}
	// The monitor a hotkey-summoned window should open on. It has to have a
	// name: the name is the only thing XWayland's RandR also knows this
	// monitor by, and without it the launcher falls back to the pointer.
	out, ok := c.ActiveOutput()
	t.Logf("active output: %+v ok=%v", out, ok)
	if ok && out.Name == "" {
		t.Errorf("output 0x%x has no name; wl_output is bound below version 4", out.ID)
	}
	if len(tops) > 0 && !ok {
		t.Error("windows are open but no monitor holds any of them; output_enter is not being tracked")
	}
	if len(tops) == 0 {
		t.Skip("no windows open to check against")
	}
	for _, w := range tops {
		if w.AppID == "" && w.Title == "" {
			t.Errorf("window 0x%x has neither app_id nor title; the handle was reported before its done event", w.ID)
		}
	}
}

// TestLiveActivate focuses a real window, so it is opt-in: a test suite
// that steals focus while you are working is worse than one that skips.
//
//	OSXFLOW_WL_ACTIVATE=firefox go test ./internal/wl -run TestLiveActivate -v
func TestLiveActivate(t *testing.T) {
	want := os.Getenv("OSXFLOW_WL_ACTIVATE")
	if want == "" || os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("set OSXFLOW_WL_ACTIVATE to an app_id substring to run this")
	}
	c, err := Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Disconnect()

	tops, _ := c.Toplevels()
	// Prefer a window that is NOT already focused: activating the window
	// that already has focus proves nothing, and would pass even if
	// Activate did nothing at all.
	var target *Toplevel
	for i := range tops {
		if strings.Contains(tops[i].AppID, want) && !tops[i].Activated {
			target = &tops[i]
			break
		}
	}
	if target == nil {
		t.Skipf("no UNfocused window whose app_id contains %q", want)
	}
	if target == nil {
		t.Skipf("no window whose app_id contains %q", want)
	}
	t.Logf("activating 0x%x %q (activated=%v before)", target.ID, target.Title, target.Activated)
	if err := c.Activate(target.ID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// Give the compositor a moment, then re-read the state it reports.
	time.Sleep(500 * time.Millisecond)
	if err := c.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	after, _ := c.Toplevels()
	for _, w := range after {
		if w.ID == target.ID {
			t.Logf("after: activated=%v", w.Activated)
			if !w.Activated {
				t.Errorf("window 0x%x did not become activated", w.ID)
			}
			if len(after) > 0 && after[len(after)-1].ID != w.ID {
				t.Errorf("activated window is not last in the order; ordering is wrong")
			}
		}
	}
}

// newTestConn is a Conn with a pipe where the compositor would be. Every
// event handler below the read loop is a pure function of the maps, so the
// interesting half of this package can be tested without a compositor --
// and the pipe is drained rather than absent because a handler is allowed
// to answer an event with a request.
func newTestConn(t *testing.T) *Conn {
	t.Helper()
	ours, theirs := net.Pipe()
	go io.Copy(io.Discard, theirs)
	t.Cleanup(func() {
		ours.Close()
		theirs.Close()
	})
	return &Conn{
		c:           ours,
		toplevels:   make(map[uint32]*Toplevel),
		outputs:     make(map[uint32]*Output),
		outputNames: make(map[uint32]uint32),
		pending:     make(map[uint32]chan struct{}),
		bars:        make(map[uint32]*Bar),
		surfaces:    make(map[uint32]*Bar),
		fracs:       make(map[uint32]*Bar),
		buffers:     make(map[uint32]*buffer),
		events:      make(chan Event, eventBuffer),
		done:        make(chan struct{}),
	}
}

// addToplevel registers a handle the way manager2 does when the compositor
// announces one.
func (c *Conn) addToplevel(id uint32) {
	c.toplevels[id] = &Toplevel{ID: id, seen: true}
	c.order = append(c.order, id)
}

func outputEvent(id uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, id)
}

// stateEvent encodes the state array the compositor sends.
func stateEvent(states ...uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(states))*4)
	for _, s := range states {
		b = binary.LittleEndian.AppendUint32(b, s)
	}
	return b
}

func TestOutputEnterAndLeaveTrackMonitors(t *testing.T) {
	c := newTestConn(t)
	c.addToplevel(10)

	c.toplevel(10, handleOutputEnter, outputEvent(6))
	c.toplevel(10, handleOutputEnter, outputEvent(6)) // re-sent when the window moves
	if got := c.toplevels[10].Outputs; len(got) != 1 || got[0] != 6 {
		t.Fatalf("outputs = %v, want [6] exactly once", got)
	}

	// Dragged across the seam: on both monitors, then only the new one.
	c.toplevel(10, handleOutputEnter, outputEvent(7))
	if got := c.toplevels[10].Outputs; len(got) != 2 {
		t.Fatalf("outputs = %v, want two monitors", got)
	}
	c.toplevel(10, handleOutputLeave, outputEvent(6))
	if got := c.toplevels[10].Outputs; len(got) != 1 || got[0] != 7 {
		t.Errorf("outputs = %v, want [7]", got)
	}

	// A leave for an output the window was never on changes nothing.
	c.toplevel(10, handleOutputLeave, outputEvent(99))
	if got := c.toplevels[10].Outputs; len(got) != 1 || got[0] != 7 {
		t.Errorf("outputs = %v after a spurious leave, want [7]", got)
	}

	// A truncated event is a compositor bug, and must not panic.
	c.toplevel(10, handleOutputEnter, []byte{1, 2})
	if got := c.toplevels[10].Outputs; len(got) != 1 {
		t.Errorf("outputs = %v after a short event", got)
	}
}

func TestOutputGeometryAndName(t *testing.T) {
	c := newTestConn(t)
	c.outputs[6] = &Output{ID: 6}

	// geometry(x, y, ...): the rest of the event is not read, so a body
	// with only the two coordinates is enough.
	var x int32 = -1920
	body := binary.LittleEndian.AppendUint32(nil, uint32(x))
	body = binary.LittleEndian.AppendUint32(body, 0)
	c.output(6, outputGeometry, body)
	if got := c.outputs[6]; got.X != -1920 || got.Y != 0 {
		t.Errorf("geometry = %d,%d, want -1920,0 (a monitor left of the origin)", got.X, got.Y)
	}

	var name []byte
	argString("DP-8")(&name)
	c.output(6, outputName, name)
	if got := c.outputs[6].Name; got != "DP-8" {
		t.Errorf("name = %q, want %q", got, "DP-8")
	}

	// An event for an output that was unplugged is ignored, not a panic.
	c.output(99, outputName, name)
}

func TestActiveOutputFollowsTheFocusedWindow(t *testing.T) {
	c := newTestConn(t)
	c.outputs[6] = &Output{ID: 6, Name: "DP-8"}
	c.outputs[7] = &Output{ID: 7, Name: "DP-12"}

	if _, ok := c.ActiveOutput(); ok {
		t.Error("ActiveOutput answered with no windows open")
	}

	c.addToplevel(10)
	c.toplevel(10, handleOutputEnter, outputEvent(6))
	c.addToplevel(11)
	c.toplevel(11, handleOutputEnter, outputEvent(7))

	// 11 is the newer window but 10 has the keyboard, so the answer is
	// where the keyboard is and not where the last window appeared.
	c.toplevel(10, handleState, stateEvent(stateActivated))
	if got, _ := c.ActiveOutput(); got.Name != "DP-8" {
		t.Errorf("ActiveOutput = %q, want DP-8: the activated window is there", got.Name)
	}

	// Focus moves, and so does the answer.
	c.toplevel(10, handleState, stateEvent())
	c.toplevel(11, handleState, stateEvent(stateActivated))
	if got, _ := c.ActiveOutput(); got.Name != "DP-12" {
		t.Errorf("ActiveOutput = %q, want DP-12", got.Name)
	}

	// The case this exists for: the launcher has taken the keyboard, so
	// nothing is activated any more. The monitor to open on is still the
	// one focus came from, not the one the other window happens to be on.
	c.toplevel(11, handleState, stateEvent())
	if got, ok := c.ActiveOutput(); !ok || got.Name != "DP-12" {
		t.Errorf("ActiveOutput = %q,%v with nothing focused, want DP-12", got.Name, ok)
	}

	// A minimized window is not where the user is working.
	c.toplevel(11, handleState, stateEvent(stateMinimized))
	if got, _ := c.ActiveOutput(); got.Name != "DP-8" {
		t.Errorf("ActiveOutput = %q with the last window minimized, want DP-8", got.Name)
	}

	// A window the compositor has not placed on any monitor is skipped
	// rather than answered with a zero output.
	c2 := newTestConn(t)
	c2.addToplevel(10)
	c2.toplevel(10, handleState, stateEvent(stateActivated))
	if got, ok := c2.ActiveOutput(); ok {
		t.Errorf("ActiveOutput = %+v for a window on no monitor, want no answer", got)
	}
}

func TestToplevelsCopyTheOutputSlice(t *testing.T) {
	c := newTestConn(t)
	c.addToplevel(10)
	c.toplevel(10, handleOutputEnter, outputEvent(6))

	tops, err := c.Toplevels()
	if err != nil {
		t.Fatalf("Toplevels: %v", err)
	}
	if len(tops) != 1 || len(tops[0].Outputs) != 1 {
		t.Fatalf("Toplevels = %+v", tops)
	}
	// The caller's copy must not share a backing array with the read loop.
	tops[0].Outputs[0] = 999
	if c.toplevels[10].Outputs[0] != 6 {
		t.Error("a caller writing to its copy changed the connection's state")
	}
}

func TestGlobalGoneForgetsAnUnpluggedMonitor(t *testing.T) {
	c := newTestConn(t)
	const global, object = 58, 6
	c.outputs[object] = &Output{ID: object, Name: "DP-8", ver: 4}
	c.outputNames[global] = object
	c.addToplevel(10)
	c.toplevel(10, handleOutputEnter, outputEvent(object))

	c.globalGone(binary.LittleEndian.AppendUint32(nil, global))

	if _, ok := c.outputs[object]; ok {
		t.Error("the output survived its global being removed")
	}
	if _, ok := c.ActiveOutput(); ok {
		t.Error("ActiveOutput still names a monitor that was unplugged")
	}
	c.globalGone(binary.LittleEndian.AppendUint32(nil, global)) // twice is harmless
	c.globalGone([]byte{1})                                     // and so is a short event
}
