package sni

// The tray's end of the protocol: the watcher items register with.
//
// A tray is two roles, the watcher that keeps the list of items and the
// host that draws them. KDE runs them apart; here one process is both, so
// the watcher always has a host.

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// ErrTrayRunning means another tray already owns WatcherName: the XFCE
// panel's status tray, waybar, or a second copy of this one.
var ErrTrayRunning = errors.New("another tray owns " + WatcherName)

// Ref is where an item lives: the bus name to call and its object path.
type Ref struct {
	Service string
	Path    dbus.ObjectPath
}

// String is the form the protocol's RegisteredStatusNotifierItems lists.
func (r Ref) String() string { return r.Service + string(r.Path) }

// ParseService works out where an item is from what it registered with and
// who sent it. The specification says a bus name, which leaves the path at
// its default; libappindicator sends a bus name with the path appended,
// and some libraries -- fyne's, which ezhours uses -- send the path alone,
// meaning "at this path on the connection calling".
func ParseService(sender, service string) (Ref, error) {
	var ref Ref
	switch {
	case service == "":
		return Ref{}, errors.New("empty service")
	case strings.HasPrefix(service, "/"):
		ref = Ref{Service: sender, Path: dbus.ObjectPath(service)}
	case strings.Contains(service, "/"):
		name, path, _ := strings.Cut(service, "/")
		ref = Ref{Service: name, Path: dbus.ObjectPath("/" + path)}
	default:
		ref = Ref{Service: service, Path: Path}
	}
	if !validBusName(ref.Service) {
		return Ref{}, fmt.Errorf("%q is not a bus name", ref.Service)
	}
	if !ref.Path.IsValid() {
		return Ref{}, fmt.Errorf("%q is not an object path", ref.Path)
	}
	return ref, nil
}

// validBusName checks a unique (":1.42") or well-known ("org.kde.X") name
// by the specification's rules, loosely enough to accept every name a bus
// would have handed out: elements of [A-Za-z0-9_-], at least two, at most
// 255 bytes.
func validBusName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	unique := strings.HasPrefix(name, ":")
	elems := strings.Split(strings.TrimPrefix(name, ":"), ".")
	if len(elems) < 2 {
		return false
	}
	for _, e := range elems {
		if e == "" {
			return false
		}
		for i, c := range e {
			ok := c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !ok || (!unique && i == 0 && c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

// Watcher owns WatcherName and keeps the list of registered items.
type Watcher struct {
	conn  *dbus.Conn
	props *prop.Properties

	mu    sync.Mutex
	items []Ref

	changed chan struct{}
	errs    chan error
	signals chan *dbus.Signal
	done    chan struct{}

	once     sync.Once
	closeErr error
}

// NewWatcher takes WatcherName on conn, which should be the session bus.
// It fails with ErrTrayRunning when another tray has it.
func NewWatcher(conn *dbus.Conn) (*Watcher, error) {
	w := &Watcher{
		conn:    conn,
		changed: make(chan struct{}, 1),
		errs:    make(chan error, 8),
		signals: make(chan *dbus.Signal, 64),
		done:    make(chan struct{}),
	}
	if err := conn.Export(&watcherObject{w: w}, WatcherPath, WatcherName); err != nil {
		return nil, fmt.Errorf("exporting %s: %w", WatcherName, err)
	}
	props, err := prop.Export(conn, WatcherPath, prop.Map{WatcherName: {
		"RegisteredStatusNotifierItems":  {Value: []string{}, Emit: prop.EmitTrue},
		"IsStatusNotifierHostRegistered": fixed(true),
		"ProtocolVersion":                fixed(int32(0)),
	}})
	if err != nil {
		return nil, fmt.Errorf("exporting %s properties: %w", WatcherName, err)
	}
	w.props = props
	if introErr := conn.Export(introspect.Introspectable(watcherIntrospection), WatcherPath,
		"org.freedesktop.DBus.Introspectable"); introErr != nil {
		return nil, fmt.Errorf("exporting introspection data: %w", introErr)
	}

	// Items leave by leaving the bus; nothing else says they are gone.
	if matchErr := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
	); matchErr != nil {
		return nil, fmt.Errorf("watching for items leaving: %w", matchErr)
	}
	conn.Signal(w.signals)

	reply, err := conn.RequestName(WatcherName, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.RemoveSignal(w.signals)
		return nil, fmt.Errorf("requesting %s: %w", WatcherName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.RemoveSignal(w.signals)
		return nil, ErrTrayRunning
	}
	go w.watchOwners()
	return w, nil
}

// Items lists the registered items, oldest first.
func (w *Watcher) Items() []Ref {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.items)
}

// Changes receives a value whenever the list may have changed. Changes in
// quick succession arrive as one; read Items for where things stand.
func (w *Watcher) Changes() <-chan struct{} { return w.changed }

// Close gives up the watcher name. Calling it again does nothing and
// returns what the first call did.
func (w *Watcher) Close() error {
	w.once.Do(func() {
		close(w.done)
		w.conn.RemoveSignal(w.signals)
		reply, err := w.conn.ReleaseName(WatcherName)
		switch {
		case err != nil:
			w.closeErr = fmt.Errorf("releasing %s: %w", WatcherName, err)
		case reply != dbus.ReleaseNameReplyReleased:
			w.closeErr = fmt.Errorf("releasing %s: the bus answered %d, not released", WatcherName, reply)
		}
	})
	return w.closeErr
}

func (w *Watcher) add(ref Ref) error {
	w.mu.Lock()
	if slices.Contains(w.items, ref) {
		// Items register again whenever they think the tray restarted;
		// the second time is not news.
		w.mu.Unlock()
		return nil
	}
	w.items = append(w.items, ref)
	list := w.listLocked()
	w.mu.Unlock()
	return w.announce("StatusNotifierItemRegistered", ref, list)
}

// drop removes every item served by a name that has left the bus.
func (w *Watcher) drop(name string) error {
	w.mu.Lock()
	var gone []Ref
	w.items = slices.DeleteFunc(w.items, func(r Ref) bool {
		if r.Service == name {
			gone = append(gone, r)
			return true
		}
		return false
	})
	list := w.listLocked()
	w.mu.Unlock()
	errs := make([]error, 0, len(gone))
	for _, ref := range gone {
		errs = append(errs, w.announce("StatusNotifierItemUnregistered", ref, list))
	}
	return errors.Join(errs...)
}

func (w *Watcher) listLocked() []string {
	list := make([]string, len(w.items))
	for i, r := range w.items {
		list[i] = r.String()
	}
	return list
}

// announce tells the host -- this process, through Changes -- and anyone
// else watching on the bus.
func (w *Watcher) announce(member string, ref Ref, list []string) error {
	select {
	case w.changed <- struct{}{}:
	default:
		// One is already waiting to be read, and says as much.
	}
	// SetMust rather than Set: Set is the D-Bus method, which refuses a
	// read-only property; this is the owner changing its own.
	w.props.SetMust(WatcherName, "RegisteredStatusNotifierItems", list)
	if err := w.conn.Emit(WatcherPath, WatcherName+"."+member, ref.String()); err != nil {
		return fmt.Errorf("emitting %s: %w", member, err)
	}
	return nil
}

// watchOwners drops items whose connection has gone.
func (w *Watcher) watchOwners() {
	for {
		select {
		case sig := <-w.signals:
			if name, ok := nameLost(sig); ok {
				if err := w.drop(name); err != nil {
					w.report(err)
				}
			}
		case <-w.done:
			return
		}
	}
}

// Errors receives what went wrong outside any call the caller made:
// announcing an item that registered or left. Read it and log it; when
// nobody does, the latest few wait and the rest go to the process's log.
func (w *Watcher) Errors() <-chan error { return w.errs }

func (w *Watcher) report(err error) {
	select {
	case w.errs <- err:
	default:
		// Better a line in the log than a stalled bus goroutine.
		log.Printf("sni watcher: %v", err)
	}
}

// nameLost reports the name a NameOwnerChanged signal says has lost its
// owner.
func nameLost(sig *dbus.Signal) (string, bool) {
	if sig == nil || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
		return "", false
	}
	name, nameOK := sig.Body[0].(string)
	owner, ownerOK := sig.Body[2].(string)
	if !nameOK || !ownerOK || owner != "" {
		return "", false
	}
	return name, true
}

// watcherObject carries the D-Bus methods, separately from Watcher because
// godbus exports every exported method of what it is given.
type watcherObject struct{ w *Watcher }

// RegisterStatusNotifierItem implements the watcher method of that name.
func (o *watcherObject) RegisterStatusNotifierItem(sender dbus.Sender, service string) *dbus.Error {
	ref, err := ParseService(string(sender), service)
	if err != nil {
		return dbus.MakeFailedError(fmt.Errorf("registering %q: %w", service, err))
	}
	if addErr := o.w.add(ref); addErr != nil {
		// Registered all the same; only the announcement failed.
		o.w.report(addErr)
	}
	return nil
}

// RegisterStatusNotifierHost implements the watcher method of that name.
// This process is the host, so another one registering changes nothing
// here; it is accepted so that it does not fail.
func (o *watcherObject) RegisterStatusNotifierHost(service string) *dbus.Error {
	if err := o.w.conn.Emit(WatcherPath, WatcherName+".StatusNotifierHostRegistered"); err != nil {
		o.w.report(fmt.Errorf("emitting StatusNotifierHostRegistered for %s: %w", service, err))
	}
	return nil
}

const watcherIntrospection = `<node>
  <interface name="org.kde.StatusNotifierWatcher">
    <method name="RegisterStatusNotifierItem">
      <arg name="service" type="s" direction="in"/>
    </method>
    <method name="RegisterStatusNotifierHost">
      <arg name="service" type="s" direction="in"/>
    </method>
    <property name="RegisteredStatusNotifierItems" type="as" access="read"/>
    <property name="IsStatusNotifierHostRegistered" type="b" access="read"/>
    <property name="ProtocolVersion" type="i" access="read"/>
    <signal name="StatusNotifierItemRegistered">
      <arg type="s"/>
    </signal>
    <signal name="StatusNotifierItemUnregistered">
      <arg type="s"/>
    </signal>
    <signal name="StatusNotifierHostRegistered"/>
  </interface>` + prop.IntrospectDataString + introspect.IntrospectDataString + `</node>`
