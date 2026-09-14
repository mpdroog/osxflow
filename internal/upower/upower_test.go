package upower

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

func TestCharging(t *testing.T) {
	for _, tc := range []struct {
		state     State
		onBattery bool
		want      bool
	}{
		{StateCharging, false, true},
		// Some firmware says charging for a moment after the cable comes out.
		{StateCharging, true, true},
		{StateDischarging, true, false},
		{StateFullyCharged, false, true},
		{StateFullyCharged, true, false},
		{StatePendingCharge, false, true},
		{StateUnknown, false, false},
		{StateEmpty, true, false},
	} {
		b := Battery{State: tc.state, OnBattery: tc.onBattery}
		if got := b.Charging(); got != tc.want {
			t.Errorf("state %d on battery %t: Charging = %t, want %t", tc.state, tc.onBattery, got, tc.want)
		}
	}
}

func TestRemaining(t *testing.T) {
	for _, tc := range []struct {
		b    Battery
		want string
	}{
		{Battery{State: StateDischarging, TimeToEmpty: 6858 * time.Second}, "1:54 remaining"},
		{Battery{State: StateDischarging, TimeToEmpty: 29 * time.Second}, "0:00 remaining"},
		{Battery{State: StateDischarging, TimeToEmpty: 30 * time.Second}, "0:01 remaining"},
		{Battery{State: StateDischarging, TimeToEmpty: 10 * time.Hour}, "10:00 remaining"},
		{Battery{State: StateDischarging}, ""},
		{Battery{State: StatePendingDischarge, TimeToEmpty: time.Hour}, "1:00 remaining"},
		{Battery{State: StateCharging, TimeToFull: 65 * time.Minute}, "1:05 until full"},
		{Battery{State: StateCharging}, "Charging"},
		{Battery{State: StateFullyCharged}, "Fully charged"},
		{Battery{State: StatePendingCharge}, "Not charging"},
		{Battery{State: StateUnknown, TimeToEmpty: time.Hour}, ""},
		{Battery{State: StateEmpty}, ""},
	} {
		if got := Remaining(&tc.b); got != tc.want {
			t.Errorf("Remaining(%+v) = %q, want %q", tc.b, got, tc.want)
		}
	}
}

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		capacity float64
		cycles   int
		want     string
	}{
		{67.2651, 424, "Health 67% · 424 cycles"},
		{67.2651, -1, "Health 67%"},
		{0, 424, "424 cycles"},
		{0, 1, "1 cycle"},
		{0, -1, ""},
		{0, 0, ""},
	} {
		b := Battery{Capacity: tc.capacity, Cycles: tc.cycles}
		if got := Health(&b); got != tc.want {
			t.Errorf("Health(capacity %v, cycles %d) = %q, want %q", tc.capacity, tc.cycles, got, tc.want)
		}
	}
}

func TestSeconds(t *testing.T) {
	if got := seconds(-5); got != 0 {
		t.Errorf("seconds(-5) = %v", got)
	}
	if got := seconds(90); got != 90*time.Second {
		t.Errorf("seconds(90) = %v", got)
	}
	if got := seconds(1 << 62); got <= 0 {
		t.Errorf("seconds(huge) = %v, overflowed", got)
	}
}

// The fake below mirrors this machine: a display device, BAT0 and ADP1.
const (
	pathBattery   = dbus.ObjectPath("/org/freedesktop/UPower/devices/battery_BAT0")
	pathLinePower = dbus.ObjectPath("/org/freedesktop/UPower/devices/line_power_ADP1")
	// Listed but never exported: a device unplugged in between.
	pathGone = dbus.ObjectPath("/org/freedesktop/UPower/devices/mouse_gone")
)

type fakeRoot struct{ devices []dbus.ObjectPath }

func (r *fakeRoot) EnumerateDevices() ([]dbus.ObjectPath, *dbus.Error) {
	return r.devices, nil
}

func ro(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }

type fakeOptions struct {
	// display replaces or adds display-device properties.
	display map[string]any
	// battery replaces or adds BAT0 properties.
	battery map[string]any
}

func exportProps(t *testing.T, conn *dbus.Conn, path dbus.ObjectPath, iface string, base, overrides map[string]any) *prop.Properties {
	t.Helper()
	m := map[string]*prop.Prop{}
	for k, v := range base {
		m[k] = ro(v)
	}
	for k, v := range overrides {
		m[k] = ro(v)
	}
	p, err := prop.Export(conn, path, prop.Map{iface: m})
	if err != nil {
		t.Fatalf("exporting %s: %v", path, err)
	}
	return p
}

// startFake puts a UPower on a private bus and returns a client for it and
// the display device's properties, for changing them.
func startFake(t *testing.T, opts fakeOptions) (*Client, *prop.Properties) {
	t.Helper()
	addr := dbustest.Start(t)
	conn := dbustest.Conn(t, addr)

	if err := conn.Export(&fakeRoot{devices: []dbus.ObjectPath{pathLinePower, pathGone, pathBattery}},
		rootPath, ifaceRoot); err != nil {
		t.Fatal(err)
	}
	exportProps(t, conn, rootPath, ifaceRoot, map[string]any{"OnBattery": true}, nil)
	display := exportProps(t, conn, displayPath, ifaceDevice, map[string]any{
		"IsPresent":   true,
		"Type":        uint32(deviceTypeBattery),
		"PowerSupply": true,
		"Percentage":  45.5307,
		"State":       uint32(StateDischarging),
		"TimeToEmpty": int64(6858),
		"TimeToFull":  int64(0),
		// The display device has no health of its own.
		"Capacity":     0.0,
		"ChargeCycles": int32(0),
	}, opts.display)
	exportProps(t, conn, pathLinePower, ifaceDevice, map[string]any{
		"Type":         uint32(1),
		"PowerSupply":  true,
		"Capacity":     0.0,
		"ChargeCycles": int32(-1),
	}, nil)
	exportProps(t, conn, pathBattery, ifaceDevice, map[string]any{
		"Type":         uint32(deviceTypeBattery),
		"PowerSupply":  true,
		"Capacity":     67.2651,
		"ChargeCycles": int32(424),
	}, opts.battery)

	reply, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claiming %s: reply %d, %v", BusName, reply, err)
	}
	return New(dbustest.Conn(t, addr)), display
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSnapshot(t *testing.T) {
	c, _ := startFake(t, fakeOptions{})
	b, err := c.Snapshot(testContext(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := Battery{
		Percentage:  45.5307,
		State:       StateDischarging,
		TimeToEmpty: 6858 * time.Second,
		Capacity:    67.2651,
		Cycles:      424,
		OnBattery:   true,
	}
	if b != want {
		t.Errorf("Snapshot =\n %+v\nwant\n %+v", b, want)
	}
	if got := Remaining(&b); got != "1:54 remaining" {
		t.Errorf("Remaining = %q", got)
	}
}

func TestSnapshotNoBattery(t *testing.T) {
	c, _ := startFake(t, fakeOptions{display: map[string]any{"IsPresent": false}})
	if _, err := c.Snapshot(testContext(t)); !errors.Is(err, ErrNoBattery) {
		t.Errorf("Snapshot with no battery present: %v, want ErrNoBattery", err)
	}
	c, _ = startFake(t, fakeOptions{display: map[string]any{"Type": uint32(1)}})
	if _, err := c.Snapshot(testContext(t)); !errors.Is(err, ErrNoBattery) {
		t.Errorf("Snapshot with a display device that is line power: %v, want ErrNoBattery", err)
	}
}

func TestSnapshotNoUPower(t *testing.T) {
	c := New(dbustest.Conn(t, dbustest.Start(t)))
	b, err := c.Snapshot(testContext(t))
	if !errors.Is(err, ErrNoState) {
		t.Errorf("Snapshot with nobody on the bus: %v, want ErrNoState", err)
	}
	if b.Cycles != -1 {
		t.Errorf("Cycles = %d on an unreadable state, want -1 (unknown)", b.Cycles)
	}
}

// A display device whose summary is malformed cannot be shown at all.
func TestSnapshotMalformedDisplay(t *testing.T) {
	c, _ := startFake(t, fakeOptions{display: map[string]any{"IsPresent": "yes"}})
	if _, err := c.Snapshot(testContext(t)); !errors.Is(err, ErrNoState) {
		t.Errorf("Snapshot with IsPresent a string: %v, want ErrNoState", err)
	}
}

// A malformed part is left out and reported; the rest still arrives.
func TestSnapshotMalformedParts(t *testing.T) {
	c, _ := startFake(t, fakeOptions{
		display: map[string]any{"TimeToEmpty": "soon"},
		battery: map[string]any{"ChargeCycles": 424.0},
	})
	b, err := c.Snapshot(testContext(t))
	if err == nil || !strings.Contains(err.Error(), "TimeToEmpty") || !strings.Contains(err.Error(), "ChargeCycles") {
		t.Errorf("Snapshot error = %v, want one naming TimeToEmpty and ChargeCycles", err)
	}
	if errors.Is(err, ErrNoState) || errors.Is(err, ErrNoBattery) {
		t.Errorf("Snapshot error %v is a whole-state error; only parts were malformed", err)
	}
	if b.Percentage != 45.5307 || b.Capacity != 67.2651 || b.Cycles != -1 || b.TimeToEmpty != 0 {
		t.Errorf("Snapshot = %+v; want percentage and capacity kept, cycles unknown, no estimate", b)
	}
}

func TestSnapshotClampsPercentage(t *testing.T) {
	c, _ := startFake(t, fakeOptions{display: map[string]any{"Percentage": 104.2}})
	b, err := c.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.Percentage != 100 {
		t.Errorf("Percentage = %v, want clamped to 100", b.Percentage)
	}
}

func TestWatch(t *testing.T) {
	c, display := startFake(t, fakeOptions{})
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
	display.SetMust(ifaceDevice, "Percentage", 44.0)
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
		{dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}, true},
		{&dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}, true},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}, false},
	} {
		if got := gone(tc.err); got != tc.want {
			t.Errorf("gone(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}
