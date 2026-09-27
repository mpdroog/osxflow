package xfconf

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakeXfconfd answers GetProperty and SetProperty the way xfconfd does,
// including its error for a property that was never set. It is locked
// because godbus answers each call on a goroutine of its own, and the race
// detector cannot see the ordering a reply through a socket provides.
type fakeXfconfd struct {
	mu    sync.Mutex
	props map[string]dbus.Variant
}

// readOnlyChannel is a channel the fake refuses writes to, the way xfconfd
// refuses a property locked by the administrator.
const readOnlyChannel = "locked"

func (f *fakeXfconfd) SetProperty(channel, property string, value dbus.Variant) *dbus.Error {
	if channel == readOnlyChannel {
		return &dbus.Error{
			Name: "org.xfce.Xfconf.Error.PermissionDenied",
			Body: []any{"Permission denied while modifying property \"" + property + "\" on channel \"" + channel + "\""},
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.props[channel+property] = value
	return nil
}

func (f *fakeXfconfd) GetProperty(channel, property string) (dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.props[channel+property]; ok {
		return v, nil
	}
	return dbus.Variant{}, &dbus.Error{
		Name: "org.xfce.Xfconf.Error.PropertyNotFound",
		Body: []any{"Property \"" + property + "\" does not exist on channel \"" + channel + "\""},
	}
}

func (f *fakeXfconfd) GetAllProperties(channel, base string) (map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]dbus.Variant{}
	for k, v := range f.props {
		if prop, ok := strings.CutPrefix(k, channel); ok && strings.HasPrefix(prop, base) {
			out[prop] = v
		}
	}
	return out, nil
}

// startXfconfd runs a fake xfconfd on a private bus and returns its
// connection, from which the test emits signals, and a client.
func startXfconfd(t *testing.T) (server *dbus.Conn, client *Client, clientConn *dbus.Conn) {
	t.Helper()
	addr := dbustest.Start(t)
	server = dbustest.Conn(t, addr)
	fake := &fakeXfconfd{props: map[string]dbus.Variant{
		"xfce4-notifyd/do-not-disturb": dbus.MakeVariant(true),
		"xfce4-notifyd/expire-timeout": dbus.MakeVariant(int32(7)),
	}}
	if err := server.Export(fake, ObjectPath, Interface); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RequestName(BusName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	clientConn = dbustest.Conn(t, addr)
	return server, New(clientConn), clientConn
}

func TestGetReadsAProperty(t *testing.T) {
	_, client, _ := startXfconfd(t)
	if v, err := client.Get("xfce4-notifyd", "/do-not-disturb"); err != nil || v != true {
		t.Errorf("do-not-disturb = %#v, %v; want true", v, err)
	}
	if v, err := client.Get("xfce4-notifyd", "/expire-timeout"); err != nil || v != int32(7) {
		t.Errorf("expire-timeout = %#v, %v; want int32(7)", v, err)
	}
}

func TestGetAllReadsABranch(t *testing.T) {
	_, client, _ := startXfconfd(t)
	got, err := client.GetAll("xfce4-notifyd", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["/do-not-disturb"] != true || got["/expire-timeout"] != int32(7) {
		t.Errorf("GetAll = %#v", got)
	}
	if got, err := client.GetAll("xfce4-notifyd", "/expire"); err != nil || len(got) != 1 {
		t.Errorf("GetAll /expire = %#v, %v", got, err)
	}
}

func TestGetAllWithoutXfconfdIsErrNoXfconf(t *testing.T) {
	client := New(dbustest.Conn(t, dbustest.Start(t)))
	if _, err := client.GetAll("xfce4-session", "/"); !errors.Is(err, ErrNoXfconf) {
		t.Fatalf("err = %v, want ErrNoXfconf", err)
	}
}

func TestSetWritesAProperty(t *testing.T) {
	_, client, _ := startXfconfd(t)
	// A property never set before, as presentation mode is on a desktop
	// where nobody has used it yet.
	if err := client.Set("xfce4-power-manager", "/xfce4-power-manager/presentation-mode", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := client.Get("xfce4-power-manager", "/xfce4-power-manager/presentation-mode"); err != nil || v != true {
		t.Errorf("after Set, Get = %#v, %v; want true", v, err)
	}
	// The value's Go type is the type written.
	if err := client.Set("xfce4-notifyd", "/expire-timeout", int32(3)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := client.Get("xfce4-notifyd", "/expire-timeout"); err != nil || v != int32(3) {
		t.Errorf("after Set, Get = %#v, %v; want int32(3)", v, err)
	}
}

func TestSetWithoutXfconfdIsErrNoXfconf(t *testing.T) {
	client := New(dbustest.Conn(t, dbustest.Start(t)))
	err := client.Set("xfce4-power-manager", "/xfce4-power-manager/presentation-mode", true)
	if !errors.Is(err, ErrNoXfconf) {
		t.Fatalf("err = %v, want ErrNoXfconf", err)
	}
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		t.Errorf("err = %v, want the bus's dbus.Error kept", err)
	}
}

func TestSetRefusedIsNotErrNoXfconf(t *testing.T) {
	_, client, _ := startXfconfd(t)
	err := client.Set(readOnlyChannel, "/anything", true)
	if err == nil {
		t.Fatal("Set on a locked channel succeeded")
	}
	if errors.Is(err, ErrNoXfconf) {
		t.Errorf("err = %v; a refusal from a running xfconfd is not ErrNoXfconf", err)
	}
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) || dbusErr.Name != "org.xfce.Xfconf.Error.PermissionDenied" {
		t.Errorf("err = %v, want xfconfd's PermissionDenied kept", err)
	}
}

func TestGetUnsetPropertyIsErrNotSet(t *testing.T) {
	_, client, _ := startXfconfd(t)
	if _, err := client.Get("xfce4-notifyd", "/never-set"); !errors.Is(err, ErrNotSet) {
		t.Fatalf("err = %v, want ErrNotSet", err)
	}
}

func TestGetWithoutXfconfdIsErrNoXfconf(t *testing.T) {
	client := New(dbustest.Conn(t, dbustest.Start(t)))
	_, err := client.Get("xfce4-notifyd", "/do-not-disturb")
	if !errors.Is(err, ErrNoXfconf) || errors.Is(err, ErrNotSet) {
		t.Fatalf("err = %v, want ErrNoXfconf rather than 'not set'", err)
	}
	// The bus's own error stays in the chain, for anyone who wants it.
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		t.Errorf("err = %v, want the bus's dbus.Error kept", err)
	}
}

func TestGetOtherFailuresAreNotErrNoXfconf(t *testing.T) {
	server, client, _ := startXfconfd(t)
	// A method the fake does not have: xfconfd is there, and wrong.
	if err := server.Export(nil, ObjectPath, Interface); err != nil {
		t.Fatal(err)
	}
	_, err := client.Get("xfce4-notifyd", "/do-not-disturb")
	if err == nil || errors.Is(err, ErrNoXfconf) || errors.Is(err, ErrNotSet) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}

func TestWatchDeliversChangesToOneChannel(t *testing.T) {
	server, client, _ := startXfconfd(t)
	changes, err := client.Watch("xfce4-notifyd")
	if err != nil {
		t.Fatal(err)
	}
	emit := func(member string, body ...any) {
		t.Helper()
		if emitErr := server.Emit(ObjectPath, Interface+"."+member, body...); emitErr != nil {
			t.Fatal(emitErr)
		}
	}
	emit("PropertyChanged", "xfce4-panel", "/other", dbus.MakeVariant(int32(1)))
	emit("PropertyChanged", "xfce4-notifyd", "/do-not-disturb", dbus.MakeVariant(false))
	emit("PropertyRemoved", "xfce4-notifyd", "/expire-timeout")

	want := []Change{
		{Channel: "xfce4-notifyd", Property: "/do-not-disturb", Value: false},
		{Channel: "xfce4-notifyd", Property: "/expire-timeout", Removed: true},
	}
	for _, w := range want {
		select {
		case got := <-changes:
			if got != w {
				t.Errorf("change = %+v, want %+v", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no change arrived, want %+v", w)
		}
	}
}

func TestWatchEndsWithTheConnection(t *testing.T) {
	_, client, clientConn := startXfconfd(t)
	changes, err := client.Watch("xfce4-notifyd")
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := clientConn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("a change arrived after the connection closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel stayed open after the connection closed")
	}
}

func TestWatchReportsMalformedSignals(t *testing.T) {
	server, client, _ := startXfconfd(t)
	changes, err := client.Watch("xfce4-notifyd")
	if err != nil {
		t.Fatal(err)
	}
	// A change with no value: acting on it would read as "off".
	if emitErr := server.Emit(ObjectPath, Interface+".PropertyChanged", "xfce4-notifyd", "/do-not-disturb"); emitErr != nil {
		t.Fatal(emitErr)
	}
	select {
	case got := <-changes:
		if !errors.Is(got.Err, ErrMalformed) || got.Property != "" || got.Value != nil {
			t.Errorf("change = %+v, want only an ErrMalformed error", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the malformed signal was not reported")
	}
}

func TestParse(t *testing.T) {
	good := dbus.Signal{Path: ObjectPath, Name: Interface + ".PropertyChanged",
		Body: []any{"c", "/p", dbus.MakeVariant(int32(1))}}
	got, ok, err := parse(&good, "c")
	if err != nil || !ok {
		t.Fatalf("a well-formed change: ok %t, err %v", ok, err)
	}
	if want := (Change{Channel: "c", Property: "/p", Value: int32(1)}); got != want {
		t.Errorf("change = %+v, want %+v", got, want)
	}
	removed := dbus.Signal{Path: ObjectPath, Name: Interface + ".PropertyRemoved", Body: []any{"c", "/p"}}
	if got, ok, err := parse(&removed, "c"); err != nil || !ok || !got.Removed || got.Value != nil {
		t.Errorf("removal = %+v, %t, %v; want Removed with no value", got, ok, err)
	}

	for name, sig := range map[string]dbus.Signal{
		"other object":  {Path: "/elsewhere", Name: good.Name, Body: good.Body},
		"other signal":  {Path: ObjectPath, Name: Interface + ".Something", Body: good.Body},
		"other channel": {Path: ObjectPath, Name: good.Name, Body: []any{"d", "/p", dbus.MakeVariant(1)}},
	} {
		if _, ok, err := parse(&sig, "c"); ok || err != nil {
			t.Errorf("%s: ok %t, err %v; want it ignored as foreign", name, ok, err)
		}
	}

	for name, sig := range map[string]dbus.Signal{
		"short body":         {Path: ObjectPath, Name: good.Name, Body: []any{"c"}},
		"channel not string": {Path: ObjectPath, Name: good.Name, Body: []any{1, "/p", dbus.MakeVariant(1)}},
		"property not string": {Path: ObjectPath, Name: good.Name,
			Body: []any{"c", 2, dbus.MakeVariant(1)}},
		"no value":          {Path: ObjectPath, Name: good.Name, Body: []any{"c", "/p"}},
		"value not variant": {Path: ObjectPath, Name: good.Name, Body: []any{"c", "/p", true}},
		"empty variant":     {Path: ObjectPath, Name: good.Name, Body: []any{"c", "/p", dbus.Variant{}}},
	} {
		if _, ok, err := parse(&sig, "c"); ok || !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ok %t, err %v; want ErrMalformed", name, ok, err)
		}
	}
}

// FuzzParseSignal builds property signals of every shape from the fuzzer's
// choices and checks that parse never panics and never answers both ways.
func FuzzParseSignal(f *testing.F) {
	f.Add(string(ObjectPath), Interface+".PropertyChanged", uint8(3), "c", "/p", uint8(0), int64(1))
	f.Add(string(ObjectPath), Interface+".PropertyRemoved", uint8(2), "c", "/p", uint8(0), int64(0))
	f.Add(string(ObjectPath), Interface+".PropertyChanged", uint8(3), "c", "/p", uint8(0xff), int64(-1))
	f.Add("/elsewhere", "x", uint8(1), "d", "", uint8(5), int64(7))
	f.Fuzz(func(t *testing.T, path, member string, n uint8, ch, prop string, kind uint8, i int64) {
		// kind's bits pick a wrong type for each argument in turn.
		body := []any{ch, prop, dbus.MakeVariant(i)}
		if kind&1 != 0 {
			body[0] = i
		}
		if kind&2 != 0 {
			body[1] = i
		}
		switch kind >> 2 & 3 {
		case 1:
			body[2] = i
		case 2:
			body[2] = dbus.Variant{}
		}
		sig := dbus.Signal{Path: dbus.ObjectPath(path), Name: member, Body: body[:min(int(n), len(body))]}

		got, ok, err := parse(&sig, "c")
		switch {
		case ok && err != nil:
			t.Fatalf("both a change %+v and an error %v", got, err)
		case err != nil && !errors.Is(err, ErrMalformed):
			t.Fatalf("error %v is not ErrMalformed", err)
		case ok && got.Channel != "c":
			t.Fatalf("change for channel %q delivered to a watcher of c", got.Channel)
		case ok && !got.Removed && got.Value == nil:
			t.Fatalf("change %+v has no value", got)
		case !ok && got != (Change{}):
			t.Fatalf("ignored signal still produced %+v", got)
		}
	})
}
