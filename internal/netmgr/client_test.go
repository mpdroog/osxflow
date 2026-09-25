package netmgr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakeNM is enough of NetworkManager, on a private bus, to answer what the
// client asks. It is locked because godbus answers each call on a goroutine
// of its own.
type fakeNM struct {
	mu    sync.Mutex
	calls []string

	conn     *dbus.Conn
	root     *prop.Properties
	settings []dbus.ObjectPath
}

func (f *fakeNM) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeNM) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type fakeRoot struct{ f *fakeNM }

func (r *fakeRoot) ActivateConnection(profile, device, specific dbus.ObjectPath) (dbus.ObjectPath, *dbus.Error) {
	r.f.record("activate %s %s %s", profile, device, specific)
	return "/org/freedesktop/NetworkManager/ActiveConnection/99", nil
}

func (r *fakeRoot) DeactivateConnection(active dbus.ObjectPath) *dbus.Error {
	r.f.record("deactivate %s", active)
	return nil
}

func (r *fakeRoot) AddAndActivateConnection(settings map[string]map[string]dbus.Variant,
	device, specific dbus.ObjectPath,
) (profile, active dbus.ObjectPath, err *dbus.Error) {
	r.f.record("add %v %s %s", settings[TypeWifi]["ssid"].Value(), device, specific)
	return "/s/new", "/a/new", nil
}

type fakeSettings struct{ f *fakeNM }

func (s *fakeSettings) ListConnections() ([]dbus.ObjectPath, *dbus.Error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return slices.Clone(s.f.settings), nil
}

type fakeProfile struct {
	settings map[string]map[string]dbus.Variant
}

func (p *fakeProfile) GetSettings() (map[string]map[string]dbus.Variant, *dbus.Error) {
	return p.settings, nil
}

type fakeWireless struct{ f *fakeNM }

func (w *fakeWireless) RequestScan(map[string]dbus.Variant) *dbus.Error {
	w.f.record("scan")
	return nil
}

const (
	pathEthernet = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/1")
	pathWifi     = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/2")
	// Listed but never exported: an object that went away between the two.
	pathGoneAP     = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/99")
	pathGoneSaved  = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings/99")
	pathGoneActive = dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/98")
	pathActiveWifi = dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/1")
	pathIP4        = dbus.ObjectPath("/org/freedesktop/NetworkManager/IP4Config/1")
)

func props(t *testing.T, conn *dbus.Conn, path dbus.ObjectPath, m prop.Map) *prop.Properties {
	t.Helper()
	p, err := prop.Export(conn, path, m)
	if err != nil {
		t.Fatalf("exporting properties on %s: %v", path, err)
	}
	return p
}

func ro(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }

func export(t *testing.T, conn *dbus.Conn, v any, path dbus.ObjectPath, iface string) {
	t.Helper()
	if err := conn.Export(v, path, iface); err != nil {
		t.Fatalf("exporting %s on %s: %v", iface, path, err)
	}
}

func addProfile(t *testing.T, f *fakeNM, path dbus.ObjectPath, settings map[string]map[string]dbus.Variant) {
	t.Helper()
	export(t, f.conn, &fakeProfile{settings: settings}, path, ifaceConnection)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = append(f.settings, path)
}

func profile(id, typ, ssid string) map[string]map[string]dbus.Variant {
	s := map[string]map[string]dbus.Variant{
		"connection": {"id": dbus.MakeVariant(id), "type": dbus.MakeVariant(typ)},
	}
	if typ == TypeWifi {
		s[TypeWifi] = map[string]dbus.Variant{"ssid": dbus.MakeVariant([]byte(ssid))}
	}
	return s
}

// startFake puts a NetworkManager on a private bus with an Ethernet device
// and a Wi-Fi device hearing two networks, a saved Wi-Fi profile that is up
// and a saved VPN that is coming up.
func startFake(t *testing.T) (*Client, *fakeNM) {
	t.Helper()
	addr := dbustest.Start(t)
	f := &fakeNM{conn: dbustest.Conn(t, addr)}

	export(t, f.conn, &fakeRoot{f}, rootPath, ifaceRoot)
	f.root = props(t, f.conn, rootPath, prop.Map{ifaceRoot: {
		"WirelessEnabled":         {Value: true, Writable: true, Emit: prop.EmitTrue},
		"WirelessHardwareEnabled": ro(true),
		"Devices":                 ro([]dbus.ObjectPath{pathEthernet, pathWifi}),
		"PrimaryConnection":       ro(pathActiveWifi),
		"ActiveConnections": ro([]dbus.ObjectPath{
			"/org/freedesktop/NetworkManager/ActiveConnection/1",
			pathGoneActive,
			"/org/freedesktop/NetworkManager/ActiveConnection/2",
		}),
	}})

	props(t, f.conn, pathEthernet, prop.Map{ifaceDevice: {"DeviceType": ro(uint32(1))}})
	export(t, f.conn, &fakeWireless{f}, pathWifi, ifaceWireless)
	props(t, f.conn, pathWifi, prop.Map{
		ifaceDevice: {"DeviceType": ro(uint32(deviceTypeWifi))},
		ifaceWireless: {
			"AccessPoints": ro([]dbus.ObjectPath{
				"/org/freedesktop/NetworkManager/AccessPoint/3",
				"/org/freedesktop/NetworkManager/AccessPoint/1",
				pathGoneAP,
				"/org/freedesktop/NetworkManager/AccessPoint/2",
			}),
			"ActiveAccessPoint": ro(dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/1")),
		},
	})
	for path, ap := range map[dbus.ObjectPath]struct {
		ssid     string
		strength uint8
		rsn      uint32
	}{
		"/org/freedesktop/NetworkManager/AccessPoint/1": {"home", 70, 0x188},
		"/org/freedesktop/NetworkManager/AccessPoint/2": {"cafe", 90, 0},
		// Another radio of the same network, stronger but not in use.
		"/org/freedesktop/NetworkManager/AccessPoint/3": {"home", 95, 0x188},
	} {
		props(t, f.conn, path, prop.Map{ifaceAP: {
			"Ssid":     ro([]byte(ap.ssid)),
			"Strength": ro(ap.strength),
			"Flags":    ro(uint32(0)),
			"WpaFlags": ro(uint32(0)),
			"RsnFlags": ro(ap.rsn),
		}})
	}

	export(t, f.conn, &fakeSettings{f}, settingsPath, ifaceSettings)
	addProfile(t, f, "/org/freedesktop/NetworkManager/Settings/1", profile("home", TypeWifi, "home"))
	f.mu.Lock()
	f.settings = append(f.settings, pathGoneSaved)
	f.mu.Unlock()
	addProfile(t, f, "/org/freedesktop/NetworkManager/Settings/2", profile("XSN", TypeVPN, ""))

	for path, a := range map[dbus.ObjectPath]struct {
		profile dbus.ObjectPath
		typ     string
		state   uint32
	}{
		pathActiveWifi: {"/org/freedesktop/NetworkManager/Settings/1", TypeWifi, uint32(StateActivated)},
		"/org/freedesktop/NetworkManager/ActiveConnection/2": {"/org/freedesktop/NetworkManager/Settings/2", TypeVPN, uint32(StateActivating)},
	} {
		active := prop.Map{ifaceActive: {
			"Connection": ro(a.profile),
			"Type":       ro(a.typ),
			"State":      ro(a.state),
		}}
		// Only the primary connection carries an address here, which is
		// also how NetworkManager behaves: the VPN coming up has no
		// IP4Config yet.
		if path == pathActiveWifi {
			active[ifaceActive]["Ip4Config"] = ro(pathIP4)
		}
		props(t, f.conn, path, active)
	}
	props(t, f.conn, pathIP4, prop.Map{ifaceIP4: {
		"AddressData": ro([]map[string]dbus.Variant{
			{"address": dbus.MakeVariant("192.168.2.15"), "prefix": dbus.MakeVariant(uint32(24))},
		}),
	}})

	reply, err := f.conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", BusName, reply, err)
	}
	return New(dbustest.Conn(t, addr)), f
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSnapshot(t *testing.T) {
	c, _ := startFake(t)
	st, err := c.Snapshot(testContext(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if st.WifiDevice != pathWifi || !st.WifiEnabled || !st.WifiHardware || st.Wired {
		t.Errorf("device %s enabled %t hardware %t wired %t; want %s true true false",
			st.WifiDevice, st.WifiEnabled, st.WifiHardware, st.Wired, pathWifi)
	}
	if st.Address != "192.168.2.15" {
		t.Errorf("Address = %q, want the primary connection's 192.168.2.15", st.Address)
	}
	if len(st.Networks) != 2 {
		t.Fatalf("got %d networks, want 2: %+v", len(st.Networks), st.Networks)
	}
	home, cafe := &st.Networks[0], &st.Networks[1]
	if home.Name != "home" || !home.Connected() || !home.Secured || home.Saved == "" {
		t.Errorf("first network = %+v, want home, connected, secured, saved", *home)
	}
	if home.Strength != 70 {
		t.Errorf("home strength = %d, want 70 from the access point in use, not 95 from the stronger one", home.Strength)
	}
	if cafe.Name != "cafe" || cafe.Secured || cafe.Saved != "" || cafe.Strength != 90 {
		t.Errorf("second network = %+v, want cafe, open, unsaved, strength 90", *cafe)
	}
	if len(st.VPNs) != 1 || st.VPNs[0].Name != "XSN" || !st.VPNs[0].On() || st.VPNs[0].State != StateActivating {
		t.Errorf("VPNs = %+v, want XSN activating", st.VPNs)
	}
}

// A profile NetworkManager describes wrongly is left out and reported,
// and everything else still arrives.
func TestSnapshotMalformedProfile(t *testing.T) {
	c, f := startFake(t)
	addProfile(t, f, "/org/freedesktop/NetworkManager/Settings/3", map[string]map[string]dbus.Variant{
		"connection": {"id": dbus.MakeVariant(int32(7)), "type": dbus.MakeVariant(TypeVPN)},
	})
	st, err := c.Snapshot(testContext(t))
	if err == nil || !strings.Contains(err.Error(), "connection.id") {
		t.Errorf("Snapshot error = %v, want one naming connection.id", err)
	}
	if errors.Is(err, ErrNoState) {
		t.Errorf("Snapshot error %v is ErrNoState; one bad profile is not the whole state", err)
	}
	if len(st.Networks) != 2 || len(st.VPNs) != 1 {
		t.Errorf("got %d networks and %d VPNs, want 2 and 1", len(st.Networks), len(st.VPNs))
	}
}

func TestSnapshotNoNetworkManager(t *testing.T) {
	c := New(dbustest.Conn(t, dbustest.Start(t)))
	_, err := c.Snapshot(testContext(t))
	if !errors.Is(err, ErrNoState) {
		t.Errorf("Snapshot with nobody on the bus: %v, want ErrNoState", err)
	}
}

func TestActions(t *testing.T) {
	c, f := startFake(t)
	ctx := testContext(t)

	if err := c.Activate(ctx, "/s/1", pathWifi); err != nil {
		t.Errorf("Activate: %v", err)
	}
	if err := c.Activate(ctx, "/s/2", ""); err != nil {
		t.Errorf("Activate a VPN: %v", err)
	}
	if err := c.Deactivate(ctx, "/a/2"); err != nil {
		t.Errorf("Deactivate: %v", err)
	}
	if err := c.JoinOpen(ctx, []byte("cafe"), pathWifi, ""); err != nil {
		t.Errorf("JoinOpen: %v", err)
	}
	if err := c.RequestScan(ctx, pathWifi); err != nil {
		t.Errorf("RequestScan: %v", err)
	}
	want := []string{
		"activate /s/1 " + string(pathWifi) + " /",
		"activate /s/2 / /",
		"deactivate /a/2",
		"add [99 97 102 101] " + string(pathWifi) + " /",
		"scan",
	}
	if got := f.recorded(); !slices.Equal(got, want) {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}

	if err := c.SetWifi(ctx, false); err != nil {
		t.Fatalf("SetWifi: %v", err)
	}
	v, err := f.root.Get(ifaceRoot, "WirelessEnabled")
	if err != nil {
		t.Fatalf("reading WirelessEnabled back: %v", err)
	}
	if on, ok := v.Value().(bool); !ok || on {
		t.Errorf("WirelessEnabled = %v after SetWifi(false)", v)
	}
}

func TestWatch(t *testing.T) {
	c, f := startFake(t)
	changed, err := c.Watch()
	if err != nil {
		t.Fatal(err)
	}
	// The bus's own NameAcquired may already have arrived; drain it so the
	// receive below is the property change's.
	drain := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case <-changed:
		case <-drain:
			break loop
		}
	}
	f.root.SetMust(ifaceRoot, "WirelessEnabled", false)
	select {
	case _, ok := <-changed:
		if !ok {
			t.Fatal("watch closed instead of reporting a change")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported after a property changed")
	}
}

func TestGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("plain"), false},
		{fmt.Errorf("wrapped: %w", dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}), true},
		{fmt.Errorf("wrapped: %w", &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}), true},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}, false},
	} {
		if got := gone(tc.err); got != tc.want {
			t.Errorf("gone(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}

// A laptop between networks routes through nothing, and NetworkManager
// says so with the "/" path. That is an empty address and not an error:
// the menu still has networks to list.
func TestSnapshotWithNothingRouted(t *testing.T) {
	c, f := startFake(t)
	f.root.SetMust(ifaceRoot, "PrimaryConnection", noObject)

	st, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if st.Address != "" {
		t.Errorf("Address = %q, want empty with nothing routed", st.Address)
	}
	if len(st.Networks) != 2 {
		t.Errorf("got %d networks, want the usual 2", len(st.Networks))
	}
}
