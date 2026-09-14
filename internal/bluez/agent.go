package bluez

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// AgentCapability is what the agent tells BlueZ it can do: show a passkey
// and take a yes or no. BlueZ picks pairing methods to match, so it asks
// for confirmations rather than for typed input.
const AgentCapability = "DisplayYesNo"

// DefaultTimeout is how long a request waits for an answer before the
// agent gives up and tells BlueZ it was cancelled. It matches BlueZ's own
// agent request timeout (REQUEST_TIMEOUT in src/agent.c, 60 seconds) as
// remembered from its source, not checked against this machine's build:
// waiting longer than BlueZ would only answer a caller who has left.
const DefaultTimeout = 60 * time.Second

// RequestKind is what BlueZ asked the agent.
type RequestKind int

// The kinds of request. Only Confirm, Authorize and AuthorizeService take
// an answer; the rest are for the UI to show.
const (
	// Confirm: does the passkey shown on both devices match?
	Confirm RequestKind = iota + 1
	// Authorize: may this device pair (a "just works" pairing)?
	Authorize
	// AuthorizeService: may this device use a service (UUID)?
	AuthorizeService
	// DisplayPasskey: show Passkey to type on the device; Entered counts
	// the keys typed so far. Shown until Done.
	DisplayPasskey
	// DisplayPinCode: show PinCode to type on the device. Shown until Done.
	DisplayPinCode
	// PinCode and Passkey: BlueZ wanted typed input, which this agent
	// cannot take. It has already been refused; tell the user to pair with
	// bluetoothctl instead.
	PinCode
	Passkey
	// Released: BlueZ no longer uses this agent -- bluetoothd stopped, or
	// another agent took over. Register again to go on answering.
	Released
	// Cancelled: BlueZ called off the request that was showing.
	Cancelled
)

// NeedsAnswer reports whether a kind of request waits for Answer: whether
// a prompt for it needs buttons.
func (k RequestKind) NeedsAnswer() bool {
	return k == Confirm || k == Authorize || k == AuthorizeService
}

// Request is one thing BlueZ asked.
type Request struct {
	Kind    RequestKind
	Device  dbus.ObjectPath
	Passkey uint32
	PinCode string
	UUID    string
	Entered uint16

	// Problem is why an AuthorizeService could not be decided without
	// asking -- the device's properties could not be read. The request is
	// asked all the same; this is for the log.
	Problem error
}

// Pending is a request handed to the caller.
//
// Answer it once, from anywhere. Done closes when the request is over --
// answered, cancelled by BlueZ, superseded by the next request, timed out,
// or the agent closed -- which is the moment a prompt showing it should
// come down. For a request that takes no answer and is not shown for a
// while (PinCode, Passkey, Released, Cancelled), Done is closed already.
type Pending struct {
	Request

	answer chan bool
	done   chan struct{}

	mu       sync.Mutex
	finished bool
}

func newPending(r *Request) *Pending {
	return &Pending{Request: *r, answer: make(chan bool, 1), done: make(chan struct{})}
}

// Answer accepts or refuses the request. The first answer counts; an
// answer after Done, or to a request that takes none, does nothing.
func (p *Pending) Answer(accept bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	select {
	case p.answer <- accept:
	default:
	}
}

// Done closes when the request is over.
func (p *Pending) Done() <-chan struct{} { return p.done }

func (p *Pending) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.finished {
		p.finished = true
		close(p.done)
	}
}

var (
	errRejected = dbus.NewError("org.bluez.Error.Rejected", []any{"rejected"})
	errCanceled = dbus.NewError("org.bluez.Error.Canceled", []any{"canceled"})
)

// errDoesNotExist is what UnregisterAgent says about an agent BlueZ has
// already forgotten.
const errDoesNotExist = "org.bluez.Error.DoesNotExist"

// Agent is a registered pairing agent.
//
// BlueZ calls it on godbus's goroutines; every request is handed over on
// Requests, for the caller's main loop, and the D-Bus call waits there
// until the request is over. At most one request is live at a time: a new
// one supersedes the last, as BlueZ runs one pairing at a time.
type Agent struct {
	conn     *dbus.Conn
	path     dbus.ObjectPath
	timeout  time.Duration
	requests chan *Pending
	closed   chan struct{}

	mu       sync.Mutex
	current  *Pending
	released bool

	closeOnce sync.Once
	closeErr  error
}

// callTimeout bounds the agent's own calls to BlueZ.
const callTimeout = 5 * time.Second

// RegisterAgent exports an agent at path on conn, the system bus, and makes
// it BlueZ's default agent, so pairing prompts come here.
func RegisterAgent(conn *dbus.Conn, path dbus.ObjectPath) (*Agent, error) {
	return registerAgent(conn, path, DefaultTimeout)
}

func registerAgent(conn *dbus.Conn, path dbus.ObjectPath, timeout time.Duration) (*Agent, error) {
	a := &Agent{
		conn:     conn,
		path:     path,
		timeout:  timeout,
		requests: make(chan *Pending),
		closed:   make(chan struct{}),
	}
	if err := conn.Export(&agentObject{a: a}, path, ifaceAgent); err != nil {
		return nil, fmt.Errorf("exporting %s: %w", ifaceAgent, err)
	}
	if err := conn.Export(introspect.Introspectable(agentIntrospection), path,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, errors.Join(fmt.Errorf("exporting agent introspection: %w", err), a.unexport())
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	manager := conn.Object(BusName, objectRoot)
	if err := manager.CallWithContext(ctx, ifaceAgentManager+".RegisterAgent", 0, path, AgentCapability).Err; err != nil {
		return nil, errors.Join(fmt.Errorf("registering the Bluetooth agent: %w", err), a.unexport())
	}
	if err := manager.CallWithContext(ctx, ifaceAgentManager+".RequestDefaultAgent", 0, path).Err; err != nil {
		return nil, errors.Join(fmt.Errorf("making the Bluetooth agent the default: %w", err), a.unregister(ctx), a.unexport())
	}
	return a, nil
}

// Requests delivers BlueZ's requests. Receive promptly: BlueZ is waiting.
func (a *Agent) Requests() <-chan *Pending { return a.requests }

// Close ends any live request, unregisters the agent and unexports it.
// An agent BlueZ has released, or a bluetoothd that has gone, is not an
// error. Calling Close again returns what the first call did.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		close(a.closed)
		a.cancelCurrent()
		a.mu.Lock()
		released := a.released
		a.mu.Unlock()
		var unregisterErr error
		if !released {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			unregisterErr = a.unregister(ctx)
			cancel()
		}
		a.closeErr = errors.Join(unregisterErr, a.unexport())
	})
	return a.closeErr
}

func (a *Agent) unregister(ctx context.Context) error {
	err := a.conn.Object(BusName, objectRoot).CallWithContext(ctx, ifaceAgentManager+".UnregisterAgent", 0, a.path).Err
	if err == nil || gone(err) || errorName(err) == errDoesNotExist {
		return nil
	}
	return fmt.Errorf("unregistering the Bluetooth agent: %w", err)
}

func (a *Agent) unexport() error {
	var errs []error
	for _, iface := range []string{ifaceAgent, "org.freedesktop.DBus.Introspectable"} {
		if err := a.conn.Export(nil, a.path, iface); err != nil {
			errs = append(errs, fmt.Errorf("unexporting %s: %w", iface, err))
		}
	}
	return errors.Join(errs...)
}

// begin makes a request the live one, ending the one before it.
func (a *Agent) begin(r *Request) *Pending {
	p := newPending(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil {
		a.current.finish()
	}
	a.current = p
	return p
}

// end finishes a request and forgets it, if it is still the live one.
func (a *Agent) end(p *Pending) {
	p.finish()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == p {
		a.current = nil
	}
}

func (a *Agent) cancelCurrent() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil {
		a.current.finish()
		a.current = nil
	}
}

// ask hands over a request that takes an answer and waits for it.
func (a *Agent) ask(r *Request) *dbus.Error {
	p := a.begin(r)
	defer a.end(p)
	timer := time.NewTimer(a.timeout)
	defer timer.Stop()

	select {
	case a.requests <- p:
	case <-p.done:
		return errCanceled
	case <-a.closed:
		return errCanceled
	case <-timer.C:
		return errCanceled
	}
	select {
	case accept := <-p.answer:
		if accept {
			return nil
		}
		return errRejected
	case <-p.done:
		return errCanceled
	case <-a.closed:
		return errCanceled
	case <-timer.C:
		return errCanceled
	}
}

// tell hands over a request that takes no answer. A lasting one (a passkey
// to show) stays live until BlueZ cancels it or the next request replaces
// it; the rest are over as soon as they are handed over.
func (a *Agent) tell(r *Request, lasting bool) {
	var p *Pending
	if lasting {
		p = a.begin(r)
	} else {
		p = newPending(r)
		p.finish()
	}
	timer := time.NewTimer(a.timeout)
	defer timer.Stop()
	select {
	case a.requests <- p:
		return
	case <-a.closed:
	case <-timer.C:
	}
	// Nobody took it; it is not showing anywhere.
	a.end(p)
}

// agentObject carries the D-Bus methods, apart from Agent because godbus
// exports every exported method of what it is given.
type agentObject struct{ a *Agent }

// Release implements org.bluez.Agent1.Release.
func (o *agentObject) Release() *dbus.Error {
	o.a.mu.Lock()
	o.a.released = true
	o.a.mu.Unlock()
	o.a.cancelCurrent()
	o.a.tell(&Request{Kind: Released}, false)
	return nil
}

// RequestPinCode implements org.bluez.Agent1.RequestPinCode: typed input,
// refused.
func (o *agentObject) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	o.a.tell(&Request{Kind: PinCode, Device: device}, false)
	return "", errRejected
}

// DisplayPinCode implements org.bluez.Agent1.DisplayPinCode.
func (o *agentObject) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	o.a.tell(&Request{Kind: DisplayPinCode, Device: device, PinCode: pincode}, true)
	return nil
}

// RequestPasskey implements org.bluez.Agent1.RequestPasskey: typed input,
// refused.
func (o *agentObject) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	o.a.tell(&Request{Kind: Passkey, Device: device}, false)
	return 0, errRejected
}

// DisplayPasskey implements org.bluez.Agent1.DisplayPasskey. BlueZ calls it
// again as each key is typed on the device; each call replaces the last.
func (o *agentObject) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	o.a.tell(&Request{Kind: DisplayPasskey, Device: device, Passkey: passkey, Entered: entered}, true)
	return nil
}

// RequestConfirmation implements org.bluez.Agent1.RequestConfirmation.
func (o *agentObject) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return o.a.ask(&Request{Kind: Confirm, Device: device, Passkey: passkey})
}

// RequestAuthorization implements org.bluez.Agent1.RequestAuthorization.
func (o *agentObject) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	return o.a.ask(&Request{Kind: Authorize, Device: device})
}

// AuthorizeService implements org.bluez.Agent1.AuthorizeService: a trusted,
// paired device is let through; anything else is asked.
func (o *agentObject) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var props map[string]dbus.Variant
	err := o.a.conn.Object(BusName, device).CallWithContext(ctx, propsGetAll, 0, ifaceDevice).Store(&props)
	r := &Request{Kind: AuthorizeService, Device: device, UUID: uuid}
	switch {
	case err != nil:
		// Asked rather than refused: the user can still say yes, and the
		// reason travels with the request for the log.
		r.Problem = fmt.Errorf("reading %s to authorize %s: %w", device, uuid, err)
	case autoAuthorize(props):
		return nil
	}
	return o.a.ask(r)
}

// Cancel implements org.bluez.Agent1.Cancel.
func (o *agentObject) Cancel() *dbus.Error {
	o.a.cancelCurrent()
	o.a.tell(&Request{Kind: Cancelled}, false)
	return nil
}

const agentIntrospection = `<node>
  <interface name="org.bluez.Agent1">
    <method name="Release"/>
    <method name="RequestPinCode">
      <arg name="device" type="o" direction="in"/>
      <arg name="pincode" type="s" direction="out"/>
    </method>
    <method name="DisplayPinCode">
      <arg name="device" type="o" direction="in"/>
      <arg name="pincode" type="s" direction="in"/>
    </method>
    <method name="RequestPasskey">
      <arg name="device" type="o" direction="in"/>
      <arg name="passkey" type="u" direction="out"/>
    </method>
    <method name="DisplayPasskey">
      <arg name="device" type="o" direction="in"/>
      <arg name="passkey" type="u" direction="in"/>
      <arg name="entered" type="q" direction="in"/>
    </method>
    <method name="RequestConfirmation">
      <arg name="device" type="o" direction="in"/>
      <arg name="passkey" type="u" direction="in"/>
    </method>
    <method name="RequestAuthorization">
      <arg name="device" type="o" direction="in"/>
    </method>
    <method name="AuthorizeService">
      <arg name="device" type="o" direction="in"/>
      <arg name="uuid" type="s" direction="in"/>
    </method>
    <method name="Cancel"/>
  </interface>` + introspect.IntrospectDataString + `</node>`
