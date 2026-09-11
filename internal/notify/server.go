package notify

import (
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// The well-known name, object and interface the specification fixes.
const (
	BusName    = "org.freedesktop.Notifications"
	ObjectPath = dbus.ObjectPath("/org/freedesktop/Notifications")
	Interface  = "org.freedesktop.Notifications"
)

// SpecVersion is the version of the specification implemented.
const SpecVersion = "1.2"

// Capabilities is what GetCapabilities reports.
//
// "body-markup" is claimed although no styling is drawn; see StripMarkup
// for why that is the honest answer. "persistence" is not: nothing is kept
// once it closes.
var Capabilities = []string{
	"actions",
	"body",
	"body-markup",
	"icon-static",
	"x-canonical-private-synchronous",
}

// Handler receives the calls that change state.
//
// Its methods run on whatever goroutine drains Server.Calls, never on one
// of godbus's. godbus answers every incoming call on a goroutine of its own,
// so without that hand-off every piece of daemon state would need a lock --
// and the X connection behind the banners would be driven from several
// goroutines at once.
type Handler interface {
	Notify(req *Request) uint32
	CloseNotification(id uint32)
}

// Info is what GetServerInformation reports about the implementation.
type Info struct {
	Name    string
	Vendor  string
	Version string
}

// ErrNameTaken means another notification daemon owns the bus name.
var ErrNameTaken = errors.New("another notification daemon owns " + BusName)

var errStopped = errors.New("the notification daemon is shutting down")

// Server is the daemon's presence on the session bus.
type Server struct {
	conn  *dbus.Conn
	calls chan func()
	done  chan struct{}
	once  sync.Once
}

// Serve exports the notification interface on conn and claims the bus
// name.
//
// The name is requested without queueing, so a daemon that finds another
// one running fails at once instead of waiting silently in line behind
// it. replace asks the current owner to give the name up, which works only
// if that owner allows it. Nobody may take it from this one: a daemon
// whose notifications vanish because something else started is worse than
// one that refuses to start.
func Serve(conn *dbus.Conn, h Handler, info Info, replace bool) (*Server, error) {
	s := &Server{conn: conn, calls: make(chan func()), done: make(chan struct{})}
	obj := &object{s: s, h: h, info: info}
	// Exported before the name is claimed, so the first call to arrive
	// once it is ours has something to answer it.
	if err := conn.Export(obj, ObjectPath, Interface); err != nil {
		return nil, fmt.Errorf("exporting %s: %w", Interface, err)
	}
	if err := conn.Export(introspect.Introspectable(introspection), ObjectPath,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, fmt.Errorf("exporting introspection data: %w", err)
	}

	flags := dbus.NameFlagDoNotQueue
	if replace {
		flags |= dbus.NameFlagReplaceExisting
	}
	reply, err := conn.RequestName(BusName, flags)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", BusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, ErrNameTaken
	}
	return s, nil
}

// Calls delivers the work godbus has handed over. Receive from it and call
// what arrives, on the goroutine that owns the daemon's state.
func (s *Server) Calls() <-chan func() { return s.calls }

// Close gives up the bus name and fails any call still waiting.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.done)
		// Releasing is a courtesy: the bus drops the name anyway when the
		// connection closes, which is moments away.
		if _, err := s.conn.ReleaseName(BusName); err != nil {
			_ = err
		}
	})
}

// EmitClosed sends NotificationClosed.
func (s *Server) EmitClosed(c Closure) error {
	return s.emit("NotificationClosed", c.ID, uint32(c.Reason))
}

// EmitAction sends ActionInvoked.
func (s *Server) EmitAction(id uint32, key string) error {
	return s.emit("ActionInvoked", id, key)
}

// EmitActivationToken sends ActivationToken, which precedes ActionInvoked
// so that the application can raise a window in response without being
// stopped by focus-stealing prevention. On X11 the token is a startup
// notification id.
func (s *Server) EmitActivationToken(id uint32, token string) error {
	return s.emit("ActivationToken", id, token)
}

func (s *Server) emit(member string, values ...any) error {
	if err := s.conn.Emit(ObjectPath, Interface+"."+member, values...); err != nil {
		return fmt.Errorf("emitting %s: %w", member, err)
	}
	return nil
}

// run executes f on the goroutine draining Calls and waits for it.
func (s *Server) run(f func()) *dbus.Error {
	finished := make(chan struct{})
	select {
	case s.calls <- func() { defer close(finished); f() }:
	case <-s.done:
		return dbus.MakeFailedError(errStopped)
	}
	select {
	case <-finished:
		return nil
	case <-s.done:
		return dbus.MakeFailedError(errStopped)
	}
}

// object carries the D-Bus methods. It is separate from Server because
// godbus exports every exported method of what it is given, and Server's
// own methods are not for the bus.
type object struct {
	s    *Server
	h    Handler
	info Info
}

// Notify implements org.freedesktop.Notifications.Notify.
func (o *object) Notify(appName string, replacesID uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, expireTimeout int32,
) (uint32, *dbus.Error) {
	req := &Request{
		AppName:       appName,
		ReplacesID:    replacesID,
		AppIcon:       appIcon,
		Summary:       summary,
		Body:          body,
		Actions:       actions,
		Hints:         make(map[string]any, len(hints)),
		ExpireTimeout: expireTimeout,
	}
	for k, v := range hints {
		req.Hints[k] = v.Value()
	}
	var id uint32
	if err := o.s.run(func() { id = o.h.Notify(req) }); err != nil {
		return 0, err
	}
	return id, nil
}

// CloseNotification implements org.freedesktop.Notifications.CloseNotification.
func (o *object) CloseNotification(id uint32) *dbus.Error {
	return o.s.run(func() { o.h.CloseNotification(id) })
}

// GetCapabilities implements org.freedesktop.Notifications.GetCapabilities.
func (o *object) GetCapabilities() ([]string, *dbus.Error) {
	return Capabilities, nil
}

// GetServerInformation implements
// org.freedesktop.Notifications.GetServerInformation.
func (o *object) GetServerInformation() (name, vendor, version, specVersion string, err *dbus.Error) {
	return o.info.Name, o.info.Vendor, o.info.Version, SpecVersion, nil
}

const introspection = `<node>
  <interface name="org.freedesktop.Notifications">
    <method name="GetCapabilities">
      <arg direction="out" name="capabilities" type="as"/>
    </method>
    <method name="Notify">
      <arg direction="in" name="app_name" type="s"/>
      <arg direction="in" name="replaces_id" type="u"/>
      <arg direction="in" name="app_icon" type="s"/>
      <arg direction="in" name="summary" type="s"/>
      <arg direction="in" name="body" type="s"/>
      <arg direction="in" name="actions" type="as"/>
      <arg direction="in" name="hints" type="a{sv}"/>
      <arg direction="in" name="expire_timeout" type="i"/>
      <arg direction="out" name="id" type="u"/>
    </method>
    <method name="CloseNotification">
      <arg direction="in" name="id" type="u"/>
    </method>
    <method name="GetServerInformation">
      <arg direction="out" name="name" type="s"/>
      <arg direction="out" name="vendor" type="s"/>
      <arg direction="out" name="version" type="s"/>
      <arg direction="out" name="spec_version" type="s"/>
    </method>
    <signal name="NotificationClosed">
      <arg name="id" type="u"/>
      <arg name="reason" type="u"/>
    </signal>
    <signal name="ActionInvoked">
      <arg name="id" type="u"/>
      <arg name="action_key" type="s"/>
    </signal>
    <signal name="ActivationToken">
      <arg name="id" type="u"/>
      <arg name="activation_token" type="s"/>
    </signal>
  </interface>` + introspect.IntrospectDataString + `</node>`
