package xfconf

import (
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// fakeXfconfd answers GetProperty the way xfconfd does, including its
// error for a property that was never set.
type fakeXfconfd struct {
	props map[string]dbus.Variant
}

func (f *fakeXfconfd) GetProperty(channel, property string) (dbus.Variant, *dbus.Error) {
	if v, ok := f.props[channel+property]; ok {
		return v, nil
	}
	return dbus.Variant{}, &dbus.Error{
		Name: "org.xfce.Xfconf.Error.PropertyNotFound",
		Body: []any{"Property \"" + property + "\" does not exist on channel \"" + channel + "\""},
	}
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

func TestGetUnsetPropertyIsErrNotSet(t *testing.T) {
	_, client, _ := startXfconfd(t)
	if _, err := client.Get("xfce4-notifyd", "/never-set"); !errors.Is(err, ErrNotSet) {
		t.Fatalf("err = %v, want ErrNotSet", err)
	}
}

func TestGetWithoutXfconfdFails(t *testing.T) {
	client := New(dbustest.Conn(t, dbustest.Start(t)))
	_, err := client.Get("xfce4-notifyd", "/do-not-disturb")
	if err == nil || errors.Is(err, ErrNotSet) {
		t.Fatalf("err = %v, want a real failure rather than 'not set'", err)
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

func TestParseIgnoresWhatIsNotAChange(t *testing.T) {
	good := dbus.Signal{Path: ObjectPath, Name: Interface + ".PropertyChanged",
		Body: []any{"c", "/p", dbus.MakeVariant(1)}}
	if _, ok := parse(&good, "c"); !ok {
		t.Fatal("a well-formed change was ignored")
	}
	for name, sig := range map[string]dbus.Signal{
		"other object":  {Path: "/elsewhere", Name: good.Name, Body: good.Body},
		"other signal":  {Path: ObjectPath, Name: Interface + ".Something", Body: good.Body},
		"other channel": {Path: ObjectPath, Name: good.Name, Body: []any{"d", "/p", dbus.MakeVariant(1)}},
		"short body":    {Path: ObjectPath, Name: good.Name, Body: []any{"c"}},
		"wrong types":   {Path: ObjectPath, Name: good.Name, Body: []any{1, 2}},
	} {
		if _, ok := parse(&sig, "c"); ok {
			t.Errorf("%s: parsed as a change", name)
		}
	}
}
