package bluez

import (
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const agentPath = dbus.ObjectPath("/org/osxflow/agent")

// agentUnderTest registers an agent from the client connection with the
// fake bluetoothd, and returns a function that calls one of the agent's
// methods the way bluetoothd would, asynchronously.
func agentUnderTest(t *testing.T, timeout time.Duration) (*fakeBluez, *Agent, func(method string, args ...any) <-chan *dbus.Call) {
	t.Helper()
	f, conn := startFake(t)
	a, err := registerAgent(conn, agentPath, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("closing the agent: %v", err)
		}
	})
	names := conn.Names()
	if len(names) == 0 {
		t.Fatal("the client connection has no unique name")
	}
	call := func(method string, args ...any) <-chan *dbus.Call {
		out := make(chan *dbus.Call, 1)
		go func() {
			out <- f.conn.Object(names[0], agentPath).Call(ifaceAgent+"."+method, 0, args...)
		}()
		return out
	}
	return f, a, call
}

func nextRequest(t *testing.T, a *Agent) *Pending {
	t.Helper()
	select {
	case p := <-a.Requests():
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no request delivered")
	}
	return nil
}

func result(t *testing.T, c <-chan *dbus.Call) *dbus.Call {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never answered BlueZ")
	}
	return nil
}

func wantErrName(t *testing.T, what string, call *dbus.Call, want string) {
	t.Helper()
	if got := errorName(call.Err); got != want {
		t.Errorf("%s: error %v (%q), want %q", what, call.Err, got, want)
	}
}

func isDone(p *Pending) bool {
	select {
	case <-p.Done():
		return true
	default:
		return false
	}
}

func TestAgentRegistration(t *testing.T) {
	f, a, _ := agentUnderTest(t, time.Second)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"register " + string(agentPath) + " " + AgentCapability,
		"default " + string(agentPath),
		"unregister " + string(agentPath),
	}
	if got := f.recorded(); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}
}

func TestAgentConfirm(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	for _, accept := range []bool{true, false} {
		c := call("RequestConfirmation", pathKeyboard, uint32(123456))
		p := nextRequest(t, a)
		if p.Kind != Confirm || p.Device != pathKeyboard || p.Passkey != 123456 {
			t.Errorf("request = %+v", p.Request)
		}
		if isDone(p) {
			t.Error("a confirmation is done before it is answered")
		}
		p.Answer(accept)
		p.Answer(!accept) // the first answer counts
		r := result(t, c)
		if accept {
			if r.Err != nil {
				t.Errorf("accepted confirmation: %v", r.Err)
			}
		} else {
			wantErrName(t, "refused confirmation", r, "org.bluez.Error.Rejected")
		}
		<-p.Done()
	}

	c := call("RequestAuthorization", pathKeyboard)
	p := nextRequest(t, a)
	if p.Kind != Authorize {
		t.Errorf("request = %+v, want Authorize", p.Request)
	}
	p.Answer(true)
	if r := result(t, c); r.Err != nil {
		t.Errorf("authorization: %v", r.Err)
	}
}

func TestAgentCancel(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	c := call("RequestConfirmation", pathKeyboard, uint32(1))
	p := nextRequest(t, a)

	cancelled := call("Cancel")
	note := nextRequest(t, a)
	if note.Kind != Cancelled || !isDone(note) {
		t.Errorf("after Cancel: %+v (done %t), want a finished Cancelled", note.Request, isDone(note))
	}
	if r := result(t, cancelled); r.Err != nil {
		t.Errorf("Cancel: %v", r.Err)
	}
	wantErrName(t, "cancelled confirmation", result(t, c), "org.bluez.Error.Canceled")
	if !isDone(p) {
		t.Error("the cancelled request is not done")
	}
	p.Answer(true) // too late; must not panic or block
}

func TestAgentTimeout(t *testing.T) {
	_, a, call := agentUnderTest(t, 100*time.Millisecond)
	c := call("RequestConfirmation", pathKeyboard, uint32(1))
	p := nextRequest(t, a)
	wantErrName(t, "unanswered confirmation", result(t, c), "org.bluez.Error.Canceled")
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Error("a timed-out request is not done")
	}
}

func TestAgentTypedInputRefused(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	for _, tc := range []struct {
		method string
		kind   RequestKind
	}{{"RequestPinCode", PinCode}, {"RequestPasskey", Passkey}} {
		c := call(tc.method, pathKeyboard)
		p := nextRequest(t, a)
		if p.Kind != tc.kind || p.Device != pathKeyboard || !isDone(p) {
			t.Errorf("%s: %+v (done %t)", tc.method, p.Request, isDone(p))
		}
		wantErrName(t, tc.method, result(t, c), "org.bluez.Error.Rejected")
	}
}

func TestAgentAuthorizeService(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	const uuid = "0000110b-0000-1000-8000-00805f9b34fb"

	// The headset is paired and trusted: through without a prompt.
	if r := result(t, call("AuthorizeService", pathHeadset, uuid)); r.Err != nil {
		t.Errorf("trusted device: %v", r.Err)
	}
	select {
	case p := <-a.Requests():
		t.Errorf("a trusted device was asked about: %+v", p.Request)
	default:
	}

	// The keyboard is paired but not trusted: asked.
	c := call("AuthorizeService", pathKeyboard, uuid)
	p := nextRequest(t, a)
	if p.Kind != AuthorizeService || p.UUID != uuid || p.Problem != nil {
		t.Errorf("request = %+v", p.Request)
	}
	p.Answer(false)
	wantErrName(t, "refused service", result(t, c), "org.bluez.Error.Rejected")

	// A device whose properties cannot be read is asked, and says why.
	c = call("AuthorizeService", dbus.ObjectPath("/org/bluez/hci0/dev_99_99_99_99_99_99"), uuid)
	p = nextRequest(t, a)
	if p.Problem == nil {
		t.Error("an unreadable device's request carries no Problem")
	}
	p.Answer(true)
	if r := result(t, c); r.Err != nil {
		t.Errorf("accepted unreadable device: %v", r.Err)
	}
}

func TestAgentDisplaySuperseded(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	first := call("DisplayPasskey", pathKeyboard, uint32(4321), uint16(0))
	p1 := nextRequest(t, a)
	if r := result(t, first); r.Err != nil {
		t.Fatalf("DisplayPasskey: %v", r.Err)
	}
	if p1.Kind != DisplayPasskey || p1.Passkey != 4321 || isDone(p1) {
		t.Errorf("first display: %+v (done %t)", p1.Request, isDone(p1))
	}

	second := call("DisplayPasskey", pathKeyboard, uint32(4321), uint16(2))
	p2 := nextRequest(t, a)
	if r := result(t, second); r.Err != nil {
		t.Fatalf("DisplayPasskey again: %v", r.Err)
	}
	if !isDone(p1) || isDone(p2) || p2.Entered != 2 {
		t.Errorf("after the second display: first done %t, second done %t entered %d", isDone(p1), isDone(p2), p2.Entered)
	}

	cancelled := call("Cancel")
	nextRequest(t, a) // the Cancelled note
	if r := result(t, cancelled); r.Err != nil {
		t.Errorf("Cancel: %v", r.Err)
	}
	if !isDone(p2) {
		t.Error("Cancel did not end the showing passkey")
	}

	pin := call("DisplayPinCode", pathKeyboard, "0000")
	p3 := nextRequest(t, a)
	if r := result(t, pin); r.Err != nil || p3.Kind != DisplayPinCode || p3.PinCode != "0000" {
		t.Errorf("DisplayPinCode: %+v, %v", p3.Request, r.Err)
	}
}

func TestAgentRelease(t *testing.T) {
	f, a, call := agentUnderTest(t, 5*time.Second)
	c := call("Release")
	if p := nextRequest(t, a); p.Kind != Released || !isDone(p) {
		t.Errorf("Release delivered %+v", p.Request)
	}
	if r := result(t, c); r.Err != nil {
		t.Errorf("Release: %v", r.Err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.recorded() {
		if call == "unregister "+string(agentPath) {
			t.Error("a released agent was unregistered")
		}
	}
}

// Closing the agent ends a request in progress, and BlueZ hears it was
// cancelled rather than waiting out the timeout.
func TestAgentCloseDuringRequest(t *testing.T) {
	_, a, call := agentUnderTest(t, 5*time.Second)
	c := call("RequestConfirmation", pathKeyboard, uint32(1))
	p := nextRequest(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	wantErrName(t, "confirmation during Close", result(t, c), "org.bluez.Error.Canceled")
	if !isDone(p) {
		t.Error("the request is not done after Close")
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
