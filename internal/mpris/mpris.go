// Package mpris finds the media players on the session bus and sends them
// play, pause, next and previous, through the MPRIS D-Bus interface that
// Firefox, VLC, Spotify and most other players implement.
//
// Everything a player reports is its own input, not ours: a title can be
// any string, and a property can have the wrong type. Reading tolerates
// both rather than failing the whole list over one careless player.
package mpris

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// The names the MPRIS specification fixes.
const (
	// NamePrefix starts every player's bus name; what follows is the
	// player's own choice, often with an instance suffix.
	NamePrefix = "org.mpris.MediaPlayer2."
	ObjectPath = dbus.ObjectPath("/org/mpris/MediaPlayer2")

	ifaceRoot   = "org.mpris.MediaPlayer2"
	ifacePlayer = "org.mpris.MediaPlayer2.Player"

	propsGetAll = "org.freedesktop.DBus.Properties.GetAll"
)

// Playback statuses, as the specification spells them.
const (
	StatusPlaying = "Playing"
	StatusPaused  = "Paused"
	StatusStopped = "Stopped"
)

// Player is one media player at one moment.
type Player struct {
	BusName  string
	Identity string

	// Status is what the player reports, normally one of the Status
	// constants. A player that reports nothing is treated as stopped.
	Status string

	// Title and Artist are from the current track's metadata, made safe to
	// draw; empty when the player does not say.
	Title  string
	Artist string

	CanPlay       bool
	CanPause      bool
	CanGoNext     bool
	CanGoPrevious bool
}

// Client talks to media players.
type Client struct {
	conn *dbus.Conn
}

// New returns a client on conn, which should be the session bus. The
// client does not own the connection.
func New(conn *dbus.Conn) *Client {
	return &Client{conn: conn}
}

// Players lists the players on the bus, playing ones first, then paused,
// then the rest, each group by name.
//
// A player that leaves the bus while it is being read is left out without
// complaint: players come and go, and that race is ordinary. One that
// answers with an error is left out too, and the error is returned
// alongside the others.
func (c *Client) Players(ctx context.Context) ([]Player, error) {
	var names []string
	if err := c.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil, fmt.Errorf("listing bus names: %w", err)
	}
	var (
		players []Player
		errs    []error
	)
	for _, name := range names {
		if !IsPlayerName(name) {
			continue
		}
		p, err := c.player(ctx, name)
		switch {
		case gone(err):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		players = append(players, p)
	}
	Sort(players)
	return players, errors.Join(errs...)
}

// IsPlayerName reports whether a bus name is a media player's.
func IsPlayerName(name string) bool {
	return strings.HasPrefix(name, NamePrefix) && len(name) > len(NamePrefix)
}

func (c *Client) player(ctx context.Context, name string) (Player, error) {
	root, err := c.getAll(ctx, name, ifaceRoot)
	if err != nil {
		return Player{}, err
	}
	props, err := c.getAll(ctx, name, ifacePlayer)
	if err != nil {
		return Player{}, err
	}
	p := Player{
		BusName:       name,
		Identity:      Clean(stringProp(root, "Identity")),
		Status:        stringProp(props, "PlaybackStatus"),
		CanPlay:       boolProp(props, "CanPlay"),
		CanPause:      boolProp(props, "CanPause"),
		CanGoNext:     boolProp(props, "CanGoNext"),
		CanGoPrevious: boolProp(props, "CanGoPrevious"),
	}
	if p.Identity == "" {
		// Identity is required by the specification, and missing from some
		// players all the same. The bus name's own suffix is the next best
		// name: "org.mpris.MediaPlayer2.vlc" is VLC.
		p.Identity = Clean(strings.TrimPrefix(name, NamePrefix))
	}
	if md, ok := props["Metadata"]; ok {
		p.Title, p.Artist = ParseMetadata(md.Value())
	}
	return p, nil
}

func (c *Client) getAll(ctx context.Context, name, iface string) (map[string]dbus.Variant, error) {
	var props map[string]dbus.Variant
	if err := c.conn.Object(name, ObjectPath).CallWithContext(ctx, propsGetAll, 0, iface).Store(&props); err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w", iface, name, err)
	}
	return props, nil
}

// stringProp and boolProp read a property a player may have left out or
// given the wrong type. Either way the answer is the zero value: these
// describe buttons to show, and a player that does not say it can go to
// the next track gets no next button, which is what it asked for.
func stringProp(props map[string]dbus.Variant, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	s, ok := v.Value().(string)
	if !ok {
		return ""
	}
	return s
}

func boolProp(props map[string]dbus.Variant, key string) bool {
	v, ok := props[key]
	if !ok {
		return false
	}
	b, ok := v.Value().(bool)
	return ok && b
}

// ParseMetadata picks the title and artist out of a player's Metadata
// property value.
//
// xesam:artist is a list of strings by the specification and a single
// string from some players; both work. Anything else, or anything missing,
// comes back empty.
func ParseMetadata(value any) (title, artist string) {
	md, ok := value.(map[string]dbus.Variant)
	if !ok {
		return "", ""
	}
	if v, found := md["xesam:title"]; found {
		if s, isString := v.Value().(string); isString {
			title = Clean(s)
		}
	}
	if v, found := md["xesam:artist"]; found {
		switch a := v.Value().(type) {
		case []string:
			parts := make([]string, 0, len(a))
			for _, s := range a {
				if s = Clean(s); s != "" {
					parts = append(parts, s)
				}
			}
			artist = strings.Join(parts, ", ")
		case string:
			artist = Clean(a)
		}
	}
	return title, artist
}

// Clean makes a player's string safe to draw on one line: invalid UTF-8
// becomes U+FFFD, control characters (a newline in a title, say) become
// spaces, and the ends are trimmed.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// Sort orders players playing first, then paused, then the rest, each
// group by Identity and then bus name.
func Sort(players []Player) {
	sort.SliceStable(players, func(i, j int) bool {
		a, b := &players[i], &players[j]
		if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
			return ra < rb
		}
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		return a.BusName < b.BusName
	})
}

func statusRank(status string) int {
	switch status {
	case StatusPlaying:
		return 0
	case StatusPaused:
		return 1
	}
	return 2
}

// PlayPause toggles playback.
func (c *Client) PlayPause(ctx context.Context, busName string) error {
	return c.call(ctx, busName, "PlayPause")
}

// Next skips to the next track.
func (c *Client) Next(ctx context.Context, busName string) error {
	return c.call(ctx, busName, "Next")
}

// Previous goes back to the previous track.
func (c *Client) Previous(ctx context.Context, busName string) error {
	return c.call(ctx, busName, "Previous")
}

func (c *Client) call(ctx context.Context, busName, method string) error {
	if !IsPlayerName(busName) {
		return fmt.Errorf("%s: %q is not a media player's bus name", method, busName)
	}
	if err := c.conn.Object(busName, ObjectPath).CallWithContext(ctx, ifacePlayer+"."+method, 0).Err; err != nil {
		return fmt.Errorf("%s on %s: %w", method, busName, err)
	}
	return nil
}

// Watch reports that the set of players, or something about one, changed:
// a player appearing or leaving, or any property changing on a player's
// object. Read Players after it fires.
//
// A burst of changes -- a new track changes the metadata and the status
// together -- becomes one receive. The channel closes when the connection
// does. The connection's signals are shared with whatever else uses it, so
// only the ones described above count.
func (c *Client) Watch() (<-chan struct{}, error) {
	if err := c.conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg0Namespace(strings.TrimSuffix(NamePrefix, ".")),
	); err != nil {
		return nil, fmt.Errorf("watching for media players: %w", err)
	}
	if err := c.conn.AddMatchSignal(
		dbus.WithMatchObjectPath(ObjectPath),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
	); err != nil {
		return nil, fmt.Errorf("watching media player properties: %w", err)
	}
	signals := make(chan *dbus.Signal, 32)
	c.conn.Signal(signals)
	changed := make(chan struct{}, 1)
	go func() {
		defer close(changed)
		for sig := range signals {
			if !Relevant(sig) {
				continue
			}
			select {
			case changed <- struct{}{}:
			default:
				// A change is already waiting, and it covers this one.
			}
		}
	}()
	return changed, nil
}

// Relevant reports whether a signal is one Watch reports: a player's name
// changing hands, or a property changing on the MPRIS object.
func Relevant(sig *dbus.Signal) bool {
	if sig == nil {
		return false
	}
	switch sig.Name {
	case "org.freedesktop.DBus.NameOwnerChanged":
		if len(sig.Body) == 0 {
			return false
		}
		name, ok := sig.Body[0].(string)
		return ok && IsPlayerName(name)
	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		if sig.Path != ObjectPath || len(sig.Body) == 0 {
			return false
		}
		iface, ok := sig.Body[0].(string)
		return ok && (iface == ifacePlayer || iface == ifaceRoot)
	}
	return false
}

// gone reports whether err says the player is no longer there: its name
// has no owner any more, or it stopped exporting the object between being
// listed and being read.
func gone(err error) bool {
	if err == nil {
		return false
	}
	var name string
	var dbusErr dbus.Error
	var dbusErrPtr *dbus.Error
	switch {
	case errors.As(err, &dbusErr):
		name = dbusErr.Name
	case errors.As(err, &dbusErrPtr):
		name = dbusErrPtr.Name
	default:
		return false
	}
	// GLib, which most players are built on, answers a call on a path it
	// does not export with UnknownMethod ("Object does not exist at path"),
	// not UnknownObject. A player just starting hits that: Celluloid, opened
	// from Thunar, owns its bus name a moment before it exports its player
	// object, and is left out here until its next property change.
	switch name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.NoSuchObject":
		return true
	}
	return false
}
