package notify

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakeHandler records calls. It is locked because the race detector cannot
// see the ordering a reply travelling through a socket provides.
type fakeHandler struct {
	mu     sync.Mutex
	reqs   []*Request
	closed []uint32
	next   uint32
}

func (f *fakeHandler) Notify(req *Request) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	f.next++
	return f.next
}

func (f *fakeHandler) CloseNotification(id uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
}

func (f *fakeHandler) requests() []*Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeHandler) closedIDs() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closed)
}

var testInfo = Info{Name: "test", Vendor: "osxflow", Version: "0"}

// serve starts a server on a private bus, with a goroutine standing in for
// the daemon's main loop, and returns a client connection to the same bus.
func serve(t *testing.T) (*Server, *fakeHandler, *dbus.Conn) {
	t.Helper()
	addr := dbustest.Start(t)
	h := &fakeHandler{}
	srv, err := Serve(dbustest.Conn(t, addr), h, testInfo, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	go func() {
		for {
			select {
			case call := <-srv.Calls():
				call()
			case <-t.Context().Done():
				return
			}
		}
	}()
	return srv, h, dbustest.Conn(t, addr)
}

func daemon(client *dbus.Conn) dbus.BusObject { return client.Object(BusName, ObjectPath) }

// imageData is how a client sends the (iiibiiay) struct: godbus encodes a
// Go struct as a D-Bus one.
type imageData struct {
	W, H, Stride int32
	Alpha        bool
	BPS, Chans   int32
	Data         []byte
}

func TestNotifyReachesTheHandler(t *testing.T) {
	_, h, client := serve(t)
	hints := map[string]dbus.Variant{
		"urgency":    dbus.MakeVariant(byte(2)),
		"image-data": dbus.MakeVariant(imageData{1, 1, 4, true, 8, 4, []byte{9, 9, 9, 255}}),
	}
	var id uint32
	err := daemon(client).Call(Interface+".Notify", 0,
		"Mail", uint32(0), "thunderbird", "Subject", "Body", []string{"default", ""}, hints, int32(-1)).Store(&id)
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Errorf("id = %d, want what the handler returned", id)
	}
	reqs := h.requests()
	if len(reqs) != 1 {
		t.Fatalf("handler saw %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.AppName != "Mail" || req.AppIcon != "thunderbird" || req.Summary != "Subject" || req.ExpireTimeout != -1 {
		t.Errorf("request = %+v", req)
	}
	// The hints arrive as godbus decodes them, which is what Parse expects.
	if n := Parse(req, time.Second); n.Urgency != Critical || n.Icon.Data == nil {
		t.Errorf("parsed urgency %d, image %t; want critical with an image", n.Urgency, n.Icon.Data != nil)
	}
}

func TestCloseNotificationReachesTheHandler(t *testing.T) {
	_, h, client := serve(t)
	if err := daemon(client).Call(Interface+".CloseNotification", 0, uint32(7)).Err; err != nil {
		t.Fatal(err)
	}
	if got := h.closedIDs(); !slices.Equal(got, []uint32{7}) {
		t.Fatalf("handler closed %v, want [7]", got)
	}
}

func TestCapabilitiesAndServerInformation(t *testing.T) {
	_, _, client := serve(t)
	var caps []string
	if err := daemon(client).Call(Interface+".GetCapabilities", 0).Store(&caps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"actions", "body", "body-markup"} {
		if !slices.Contains(caps, want) {
			t.Errorf("capabilities %v lack %q", caps, want)
		}
	}
	var name, vendor, version, spec string
	if err := daemon(client).Call(Interface+".GetServerInformation", 0).Store(&name, &vendor, &version, &spec); err != nil {
		t.Fatal(err)
	}
	if name != testInfo.Name || vendor != testInfo.Vendor || version != testInfo.Version || spec != SpecVersion {
		t.Errorf("server information = %q %q %q %q", name, vendor, version, spec)
	}
}

func TestIntrospectionDescribesTheInterface(t *testing.T) {
	_, _, client := serve(t)
	var xml string
	if err := daemon(client).Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="Notify"`, `name="NotificationClosed"`, `name="ActivationToken"`} {
		if !strings.Contains(xml, want) {
			t.Errorf("introspection data lacks %s", want)
		}
	}
}

func TestSignalsReachClients(t *testing.T) {
	srv, _, client := serve(t)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(Interface)); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 8)
	client.Signal(signals)

	if err := srv.EmitActivationToken(7, "token"); err != nil {
		t.Fatal(err)
	}
	if err := srv.EmitAction(7, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := srv.EmitClosed(Closure{ID: 7, Reason: ReasonDismissed}); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		member string
		body   []any
	}{
		{"ActivationToken", []any{uint32(7), "token"}},
		{"ActionInvoked", []any{uint32(7), "reply"}},
		{"NotificationClosed", []any{uint32(7), uint32(ReasonDismissed)}},
	}
	for _, w := range want {
		sig := nextSignal(t, signals)
		if sig.Name != Interface+"."+w.member || !slices.Equal(sig.Body, w.body) {
			t.Errorf("signal %s %v, want %s %v", sig.Name, sig.Body, w.member, w.body)
		}
	}
}

// nextSignal returns the next signal from the notification object,
// skipping the bus's own (NameAcquired and the like).
func nextSignal(t *testing.T, signals <-chan *dbus.Signal) *dbus.Signal {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case sig := <-signals:
			if sig.Path == ObjectPath {
				return sig
			}
		case <-timeout:
			t.Fatal("no signal arrived")
		}
	}
}

func TestASecondDaemonIsRefused(t *testing.T) {
	addr := dbustest.Start(t)
	first, err := Serve(dbustest.Conn(t, addr), &fakeHandler{}, testInfo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for _, replace := range []bool{false, true} {
		if _, err := Serve(dbustest.Conn(t, addr), &fakeHandler{}, testInfo, replace); !errors.Is(err, ErrNameTaken) {
			t.Errorf("second daemon (replace %t): err = %v, want ErrNameTaken", replace, err)
		}
	}
}

func TestCallsFailOnceClosed(t *testing.T) {
	addr := dbustest.Start(t)
	srv, err := Serve(dbustest.Conn(t, addr), &fakeHandler{}, testInfo, false)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	srv.Close() // twice is harmless
	if callErr := daemon(dbustest.Conn(t, addr)).Call(Interface+".CloseNotification", 0, uint32(1)).Err; callErr == nil {
		t.Fatal("a call succeeded after the server closed")
	}
}
