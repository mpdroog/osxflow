// Package sni puts an icon in the system tray through the
// StatusNotifierItem protocol.
//
// StatusNotifierItem is the tray protocol of the XFCE panel's status tray,
// KDE, waybar and GNOME's AppIndicator extension alike, and it is D-Bus
// rather than X11 -- which is why an icon made here shows up under Wayland
// too. The item draws nothing itself: it hands the tray pixmaps and reports
// clicks, and whoever uses it decides what a click opens.
//
// There is no DBusMenu. A tray that finds none calls Activate or
// ContextMenu instead, and the caller draws its own menu.
package sni

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// The names the protocol fixes.
const (
	Interface   = "org.kde.StatusNotifierItem"
	Path        = dbus.ObjectPath("/StatusNotifierItem")
	WatcherName = "org.kde.StatusNotifierWatcher"
	WatcherPath = dbus.ObjectPath("/StatusNotifierWatcher")
)

// Pixmap is one size of an icon: ARGB32 pixels, row by row, each in
// network byte order, straight (not premultiplied) alpha. The field order
// is the D-Bus struct (iiay).
type Pixmap struct {
	Width, Height int32
	Pix           []byte
}

// ToolTip is the D-Bus struct (sa(iiay)ss).
type ToolTip struct {
	IconName string
	Icon     []Pixmap
	Title    string
	Text     string
}

// ClickKind says which of the protocol's activation calls a click was.
type ClickKind int

// Which button a tray maps to which call is the tray's business; these are
// the usual mappings.
const (
	Activate          ClickKind = iota // primary button
	SecondaryActivate                  // middle button
	ContextMenu                        // secondary button
	Scroll                             // the wheel, over the icon
)

// Click is one activation, at the screen position the tray reports, or one
// turn of the wheel.
//
// A tray that does not know the position sends 0, 0, and XFCE's status tray
// sends it in GTK's logical pixels -- half the real ones at 2x -- so a
// caller placing a window should ask the display server where the pointer
// is instead.
type Click struct {
	Kind ClickKind
	X, Y int

	// Delta and Horizontal describe a Scroll. The size and sign of Delta
	// are whatever the tray sends; the specification fixes neither.
	Delta      int
	Horizontal bool
}

// Options describes the item.
type Options struct {
	// ID is a stable name for the item, the same from one run to the next.
	ID    string
	Title string

	// Category is ApplicationStatus, Communications, SystemServices or
	// Hardware.
	Category string

	Icon    []Pixmap
	ToolTip ToolTip
}

// ErrNoWatcher means there is no tray to register with yet. It is what
// registering at login reports when the panel is slower to start, and the
// item registers again by itself once a tray appears.
var ErrNoWatcher = errors.New("no " + WatcherName + " on the session bus")

var errClosed = errors.New("the status notifier item is closed")

// registerTimeout bounds one registration call: a tray that has hung
// should cost a log line, not a stuck goroutine.
const registerTimeout = 5 * time.Second

var seq atomic.Uint64

// Item is an exported StatusNotifierItem.
type Item struct {
	conn  *dbus.Conn
	props *prop.Properties
	name  string

	clicks     chan Click
	registered chan error
	signals    chan *dbus.Signal
	done       chan struct{}

	once     sync.Once
	closeErr error
}

// Export puts the item on conn, which should be the session bus, and
// starts registering it with the tray.
//
// Registration is asynchronous and repeated: every time a tray (re)starts,
// the item registers with it again. Each attempt's result arrives on
// Registrations, and must be read.
func Export(conn *dbus.Conn, opts *Options) (*Item, error) {
	it := &Item{
		conn:       conn,
		name:       fmt.Sprintf("%s-%d-%d", Interface, os.Getpid(), seq.Add(1)),
		clicks:     make(chan Click),
		registered: make(chan error),
		signals:    make(chan *dbus.Signal, 16),
		done:       make(chan struct{}),
	}
	if err := conn.Export(&object{it: it}, Path, Interface); err != nil {
		return nil, fmt.Errorf("exporting %s: %w", Interface, err)
	}
	icon := opts.Icon
	if icon == nil {
		// An empty array rather than a nil one: the signature is the same
		// either way, but some trays read a missing value as a broken item.
		icon = []Pixmap{}
	}
	props, err := prop.Export(conn, Path, prop.Map{Interface: {
		"Category":   fixed(opts.Category),
		"Id":         fixed(opts.ID),
		"Title":      fixed(opts.Title),
		"Status":     fixed("Active"),
		"WindowId":   fixed(int32(0)),
		"IconName":   fixed(""),
		"IconPixmap": fixed(icon),
		"ToolTip":    fixed(opts.ToolTip),
		"ItemIsMenu": fixed(false),
	}})
	if err != nil {
		return nil, fmt.Errorf("exporting %s properties: %w", Interface, err)
	}
	it.props = props
	if introErr := conn.Export(introspect.Introspectable(introspection), Path,
		"org.freedesktop.DBus.Introspectable"); introErr != nil {
		return nil, fmt.Errorf("exporting introspection data: %w", introErr)
	}

	// Watching for a tray starts before the first registration, so one
	// that starts in between is not missed.
	if matchErr := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, WatcherName),
	); matchErr != nil {
		return nil, fmt.Errorf("watching for %s: %w", WatcherName, matchErr)
	}
	conn.Signal(it.signals)

	reply, err := conn.RequestName(it.name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", it.name, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("requesting %s: the bus answered %d, not primary owner", it.name, reply)
	}

	go it.stayRegistered()
	return it, nil
}

func fixed(v any) *prop.Prop {
	// Changes are announced with the protocol's own NewIcon and
	// NewToolTip signals, which is what trays listen for, not with
	// PropertiesChanged.
	return &prop.Prop{Value: v, Emit: prop.EmitFalse}
}

// Clicks delivers activations and scrolls. The D-Bus call one arrived on
// waits until it is received, so receive promptly.
func (it *Item) Clicks() <-chan Click { return it.clicks }

// Registrations delivers the result of every attempt to register with a
// tray: nil, ErrNoWatcher, or why the tray refused.
func (it *Item) Registrations() <-chan error { return it.registered }

// SetIcon replaces the icon. Give several sizes; the tray picks.
func (it *Item) SetIcon(icon []Pixmap) error {
	it.props.SetMust(Interface, "IconPixmap", icon)
	return it.emit("NewIcon")
}

// SetToolTip replaces the tooltip.
func (it *Item) SetToolTip(tip ToolTip) error {
	it.props.SetMust(Interface, "ToolTip", tip)
	return it.emit("NewToolTip")
}

func (it *Item) emit(member string) error {
	if err := it.conn.Emit(Path, Interface+"."+member); err != nil {
		return fmt.Errorf("emitting %s: %w", member, err)
	}
	return nil
}

// Close stops registering, fails any click still being delivered and gives
// up the bus name. Calling it again does nothing and returns what the first
// call did.
func (it *Item) Close() error {
	it.once.Do(func() {
		close(it.done)
		it.conn.RemoveSignal(it.signals)
		reply, err := it.conn.ReleaseName(it.name)
		switch {
		case err != nil:
			it.closeErr = fmt.Errorf("releasing %s: %w", it.name, err)
		case reply != dbus.ReleaseNameReplyReleased:
			it.closeErr = fmt.Errorf("releasing %s: the bus answered %d, not released", it.name, reply)
		}
	})
	return it.closeErr
}

// stayRegistered registers now and again whenever a tray takes the
// watcher name: at login after the panel starts, and after a panel restart.
func (it *Item) stayRegistered() {
	if !it.report(it.register()) {
		return
	}
	for {
		select {
		case sig := <-it.signals:
			if sig == nil || !watcherAppeared(sig) {
				continue
			}
			if !it.report(it.register()) {
				return
			}
		case <-it.done:
			return
		}
	}
}

// watcherAppeared reports whether sig says a tray just took the watcher
// name. The signal channel carries everything the connection receives, so
// most of what arrives is something else.
func watcherAppeared(sig *dbus.Signal) bool {
	if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
		return false
	}
	name, nameOK := sig.Body[0].(string)
	owner, ownerOK := sig.Body[2].(string)
	return nameOK && ownerOK && name == WatcherName && owner != ""
}

// report hands a registration result over, and says false once the item
// is closed.
func (it *Item) report(err error) bool {
	select {
	case it.registered <- err:
		return true
	case <-it.done:
		return false
	}
}

func (it *Item) register() error {
	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()
	err := it.conn.Object(WatcherName, WatcherPath).
		CallWithContext(ctx, WatcherName+".RegisterStatusNotifierItem", 0, it.name).Err
	if err == nil {
		return nil
	}
	if noOwner(err) {
		return ErrNoWatcher
	}
	return fmt.Errorf("registering with %s: %w", WatcherName, err)
}

func noOwner(err error) bool {
	name := errorName(err)
	return name == "org.freedesktop.DBus.Error.ServiceUnknown" || name == "org.freedesktop.DBus.Error.NameHasNoOwner"
}

// errorName is the D-Bus error name err carries, or "" when it is not a
// D-Bus error. godbus returns dbus.Error by value from some calls and by
// pointer from others.
func errorName(err error) string {
	var dbusErr dbus.Error
	var dbusErrPtr *dbus.Error
	switch {
	case errors.As(err, &dbusErr):
		return dbusErr.Name
	case errors.As(err, &dbusErrPtr):
		return dbusErrPtr.Name
	}
	return ""
}

// FromImage converts a drawn image into a pixmap. image.RGBA holds
// premultiplied colour and the protocol wants it straight.
func FromImage(img *image.RGBA) Pixmap {
	b := img.Bounds()
	pix := make([]byte, 0, 4*b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := img.PixOffset(x, y)
			a := img.Pix[i+3]
			pix = append(pix, a,
				unpremultiply(img.Pix[i], a),
				unpremultiply(img.Pix[i+1], a),
				unpremultiply(img.Pix[i+2], a))
		}
	}
	return Pixmap{Width: toInt32(b.Dx()), Height: toInt32(b.Dy()), Pix: pix}
}

func unpremultiply(c, a uint8) uint8 {
	if a == 0 {
		return 0
	}
	v := (uint32(c)*255 + uint32(a)/2) / uint32(a)
	if v > math.MaxUint8 {
		return math.MaxUint8
	}
	return uint8(v)
}

func toInt32(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}

// object carries the D-Bus methods, separately from Item because godbus
// exports every exported method of what it is given.
type object struct{ it *Item }

// Activate implements org.kde.StatusNotifierItem.Activate.
func (o *object) Activate(x, y int32) *dbus.Error {
	return o.it.deliver(Click{Kind: Activate, X: int(x), Y: int(y)})
}

// SecondaryActivate implements org.kde.StatusNotifierItem.SecondaryActivate.
func (o *object) SecondaryActivate(x, y int32) *dbus.Error {
	return o.it.deliver(Click{Kind: SecondaryActivate, X: int(x), Y: int(y)})
}

// ContextMenu implements org.kde.StatusNotifierItem.ContextMenu.
func (o *object) ContextMenu(x, y int32) *dbus.Error {
	return o.it.deliver(Click{Kind: ContextMenu, X: int(x), Y: int(y)})
}

// Scroll implements org.kde.StatusNotifierItem.Scroll.
func (o *object) Scroll(delta int32, orientation string) *dbus.Error {
	return o.it.deliver(Click{Kind: Scroll, Delta: int(delta), Horizontal: strings.EqualFold(orientation, "horizontal")})
}

func (it *Item) deliver(c Click) *dbus.Error {
	select {
	case it.clicks <- c:
		return nil
	case <-it.done:
		return dbus.MakeFailedError(errClosed)
	}
}

const introspection = `<node>
  <interface name="org.kde.StatusNotifierItem">
    <property name="Category" type="s" access="read"/>
    <property name="Id" type="s" access="read"/>
    <property name="Title" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="WindowId" type="i" access="read"/>
    <property name="IconName" type="s" access="read"/>
    <property name="IconPixmap" type="a(iiay)" access="read"/>
    <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
    <property name="ItemIsMenu" type="b" access="read"/>
    <method name="Activate">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="SecondaryActivate">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="ContextMenu">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="Scroll">
      <arg name="delta" type="i" direction="in"/>
      <arg name="orientation" type="s" direction="in"/>
    </method>
    <signal name="NewIcon"/>
    <signal name="NewToolTip"/>
  </interface>` + prop.IntrospectDataString + introspect.IntrospectDataString + `</node>`
