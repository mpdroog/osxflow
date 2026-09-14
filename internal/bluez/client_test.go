package bluez

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakeBluez is enough of bluetoothd, on a private bus, to answer the
// client and drive the agent. It is locked because godbus answers each
// call on a goroutine of its own.
type fakeBluez struct {
	mu      sync.Mutex
	calls   []string
	objects objects

	conn    *dbus.Conn
	adapter *prop.Properties
}

func (f *fakeBluez) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeBluez) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type fakeObjectManager struct{ f *fakeBluez }

func (m *fakeObjectManager) GetManagedObjects() (objects, *dbus.Error) {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	return m.f.objects, nil
}

type fakeDevice struct {
	f    *fakeBluez
	path dbus.ObjectPath
}

func (d *fakeDevice) Connect() *dbus.Error {
	d.f.record("connect " + string(d.path))
	return nil
}

func (d *fakeDevice) Disconnect() *dbus.Error {
	d.f.record("disconnect " + string(d.path))
	return nil
}

type fakeAgentManager struct{ f *fakeBluez }

func (m *fakeAgentManager) RegisterAgent(path dbus.ObjectPath, capability string) *dbus.Error {
	m.f.record("register " + string(path) + " " + capability)
	return nil
}

func (m *fakeAgentManager) RequestDefaultAgent(path dbus.ObjectPath) *dbus.Error {
	m.f.record("default " + string(path))
	return nil
}

func (m *fakeAgentManager) UnregisterAgent(path dbus.ObjectPath) *dbus.Error {
	m.f.record("unregister " + string(path))
	return nil
}

const (
	pathHci0     = dbus.ObjectPath("/org/bluez/hci0")
	pathHeadset  = dbus.ObjectPath("/org/bluez/hci0/dev_AA_AA_AA_AA_AA_AA")
	pathKeyboard = dbus.ObjectPath("/org/bluez/hci0/dev_BB_BB_BB_BB_BB_BB")
)

// startFake runs a fake bluetoothd with sampleObjects' tree and returns a
// client connection to the same bus.
func startFake(t *testing.T) (*fakeBluez, *dbus.Conn) {
	t.Helper()
	addr := dbustest.Start(t)
	f := &fakeBluez{conn: dbustest.Conn(t, addr), objects: sampleObjects()}

	export := func(v any, path dbus.ObjectPath, iface string) {
		t.Helper()
		if err := f.conn.Export(v, path, iface); err != nil {
			t.Fatalf("exporting %s on %s: %v", iface, path, err)
		}
	}
	export(&fakeObjectManager{f}, rootPath, "org.freedesktop.DBus.ObjectManager")
	export(&fakeAgentManager{f}, objectRoot, ifaceAgentManager)
	for _, p := range []dbus.ObjectPath{pathHeadset, pathKeyboard} {
		export(&fakeDevice{f: f, path: p}, p, ifaceDevice)
	}

	var err error
	f.adapter, err = prop.Export(f.conn, pathHci0, prop.Map{ifaceAdapter: {
		"Alias":   {Value: "mbp", Emit: prop.EmitTrue},
		"Powered": {Value: true, Writable: true, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		path    dbus.ObjectPath
		trusted bool
	}{{pathHeadset, true}, {pathKeyboard, false}} {
		if _, exportErr := prop.Export(f.conn, d.path, prop.Map{ifaceDevice: {
			"Paired":  {Value: true, Emit: prop.EmitTrue},
			"Trusted": {Value: d.trusted, Emit: prop.EmitTrue},
		}}); exportErr != nil {
			t.Fatal(exportErr)
		}
	}

	reply, err := f.conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", BusName, reply, err)
	}
	return f, dbustest.Conn(t, addr)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSnapshot(t *testing.T) {
	_, conn := startFake(t)
	st, err := New(conn).Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Adapter == nil || st.Adapter.Path != pathHci0 {
		t.Fatalf("adapter = %+v", st.Adapter)
	}
	if len(st.Devices) != 2 || st.Devices[0].Name != "Kraken" || st.Devices[0].Battery != 80 {
		t.Errorf("devices = %+v", st.Devices)
	}
}

func TestSnapshotNoBluez(t *testing.T) {
	c := New(dbustest.Conn(t, dbustest.Start(t)))
	if _, err := c.Snapshot(testContext(t)); !errors.Is(err, ErrNoState) {
		t.Errorf("Snapshot with no bluetoothd: %v, want ErrNoState", err)
	}
}

func TestActions(t *testing.T) {
	f, conn := startFake(t)
	c := New(conn)
	ctx := testContext(t)

	if err := c.Connect(ctx, pathKeyboard); err != nil {
		t.Errorf("Connect: %v", err)
	}
	if err := c.Disconnect(ctx, pathHeadset); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	want := []string{"connect " + string(pathKeyboard), "disconnect " + string(pathHeadset)}
	if got := f.recorded(); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}

	if err := c.SetPowered(ctx, pathHci0, false); err != nil {
		t.Fatalf("SetPowered: %v", err)
	}
	got, dbusErr := f.adapter.Get(ifaceAdapter, "Powered")
	if dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if on, ok := got.Value().(bool); !ok || on {
		t.Errorf("Powered = %v after SetPowered(false)", got)
	}

	if err := c.Connect(ctx, "/org/bluez/hci0/dev_99_99_99_99_99_99"); err == nil {
		t.Error("connecting a device that does not exist succeeded")
	}
}

func waitChange(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-changed:
		if !ok {
			t.Fatal("watch closed instead of reporting a change")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
}

// drain empties the channel of anything already waiting -- the bus's own
// NameAcquired is filtered, but a stray earlier change would not be.
func drain(changed <-chan struct{}) {
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case <-changed:
		case <-deadline:
			return
		}
	}
}

func TestWatch(t *testing.T) {
	f, conn := startFake(t)
	changed, err := New(conn).Watch()
	if err != nil {
		t.Fatal(err)
	}
	drain(changed)

	f.adapter.SetMust(ifaceAdapter, "Powered", false)
	waitChange(t, changed)

	drain(changed)
	if err := f.conn.Emit(rootPath, sigInterfacesAdded,
		dbus.ObjectPath("/org/bluez/hci0/dev_12_34_56_78_9A_BC"),
		map[string]map[string]dbus.Variant{ifaceDevice: {"Paired": v(true)}}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changed)
}
