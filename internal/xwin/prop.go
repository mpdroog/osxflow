package xwin

// Reading X window properties without xgbutil's xprop.
//
// xprop is fine for writing properties and wrong for reading them in two
// ways that matter here. It turns "the property is not set" into a plain
// formatted string, and it wraps real X errors with %s, so a caller cannot
// tell a window that never set _NET_WM_PID from a window that has just been
// destroyed from a broken connection -- and those want three different
// responses. And its decoders trust the reply: PropValNum checks the format
// but not the length, so a client that sets a zero-length CARDINAL makes
// xgb.Get32 index past the end of an empty slice and takes the dock down
// with it. Any client on the display can set any property on its own
// window, so that is a crash anybody can cause.
//
// So properties are fetched here with the raw request, and every decoder
// checks both the format and the length before touching the bytes.

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/geom"
)

// ErrPropUnset reports that a property is not set on the window. It is the
// normal case for most optional properties, and callers stay silent for it.
var ErrPropUnset = errors.New("property not set")

// ErrWindowGone reports that the window no longer exists. Windows are
// destroyed at any moment -- Alt+F4 while something is enumerating them is
// not exotic -- so this is expected rather than a fault. The underlying
// xproto.WindowError stays in the chain as well, for errors.As.
var ErrWindowGone = errors.New("window no longer exists")

// ErrPropMalformed reports a property whose contents do not have the shape
// its definition requires: the wrong format, too few values, a WM_CLASS
// that is not two strings. That is the fault of the client that set it,
// never of the reader, and a reader should keep whatever else it could
// read from the same window.
var ErrPropMalformed = errors.New("malformed property")

// GetProperty fetches the whole of one property.
//
// The error is ErrPropUnset (wrapped) when the property does not exist on
// the window, and wraps ErrWindowGone alongside the xproto.WindowError when
// the window does not. Any other failure comes back wrapped with %w, so the
// X error type survives for errors.As. name only labels the error.
func GetProperty(conn *xgb.Conn, win xproto.Window, atom xproto.Atom, name string) (*xproto.GetPropertyReply, error) {
	reply, err := xproto.GetProperty(conn, false, win, atom,
		xproto.GetPropertyTypeAny, 0, math.MaxUint32).Reply()
	if err != nil {
		return nil, replyError(name, win, err)
	}
	if reply == nil {
		// xgb returns a nil reply with no error only when the reply buffer
		// is missing, which a GetProperty request should never produce.
		return nil, fmt.Errorf("reading %s on window 0x%x: no reply from the X server", name, win)
	}
	if reply.Type == xproto.AtomNone {
		return nil, fmt.Errorf("%s on window 0x%x: %w", name, win, ErrPropUnset)
	}
	return reply, nil
}

// replyError wraps the error from a GetProperty reply, marking a BadWindow
// as ErrWindowGone without hiding the X error behind it.
func replyError(name string, win xproto.Window, err error) error {
	var gone xproto.WindowError
	if errors.As(err, &gone) {
		return fmt.Errorf("reading %s on window 0x%x: %w: %w", name, win, ErrWindowGone, err)
	}
	return fmt.Errorf("reading %s on window 0x%x: %w", name, win, err)
}

// Props reads properties by name, interning each atom once.
//
// The cache is not an optimisation to skip: every property read would
// otherwise cost a second round trip to the server just to turn the name
// into a number, and the dock reads six properties per window every time
// the window list changes.
type Props struct {
	conn *xgb.Conn

	mu    sync.Mutex
	atoms map[string]xproto.Atom
	names map[xproto.Atom]string
}

// NewProps returns a property reader on conn.
func NewProps(conn *xgb.Conn) *Props {
	return &Props{
		conn:  conn,
		atoms: make(map[string]xproto.Atom),
		names: make(map[xproto.Atom]string),
	}
}

// Atom interns name, creating the atom if the server does not have it yet.
func (p *Props) Atom(name string) (xproto.Atom, error) {
	p.mu.Lock()
	atom, ok := p.atoms[name]
	p.mu.Unlock()
	if ok {
		return atom, nil
	}
	if len(name) > math.MaxUint16 {
		return 0, fmt.Errorf("interning atom %q: name longer than %d bytes", name[:32], math.MaxUint16)
	}
	reply, err := xproto.InternAtom(p.conn, false, geom.U16(len(name)), name).Reply()
	if err != nil {
		return 0, fmt.Errorf("interning atom %s: %w", name, err)
	}
	if reply == nil || reply.Atom == xproto.AtomNone {
		return 0, fmt.Errorf("interning atom %s: the server returned no atom", name)
	}
	p.remember(name, reply.Atom)
	return reply.Atom, nil
}

// AtomName returns the name of an atom. An atom the server does not know
// comes back as an xproto.AtomError in the chain; for an atom read out of a
// property, that means the client stored garbage.
func (p *Props) AtomName(atom xproto.Atom) (string, error) {
	p.mu.Lock()
	name, ok := p.names[atom]
	p.mu.Unlock()
	if ok {
		return name, nil
	}
	reply, err := xproto.GetAtomName(p.conn, atom).Reply()
	if err != nil {
		return "", fmt.Errorf("naming atom %d: %w", atom, err)
	}
	if reply == nil {
		return "", fmt.Errorf("naming atom %d: no reply from the X server", atom)
	}
	p.remember(reply.Name, atom)
	return reply.Name, nil
}

func (p *Props) remember(name string, atom xproto.Atom) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.atoms[name] = atom
	p.names[atom] = name
}

// Get fetches the property called name on win. The errors are GetProperty's,
// plus a failure to intern the name.
func (p *Props) Get(win xproto.Window, name string) (*xproto.GetPropertyReply, error) {
	atom, err := p.Atom(name)
	if err != nil {
		return nil, err
	}
	return GetProperty(p.conn, win, atom, name)
}

// checkShape verifies that a reply is in the given format and that its
// bytes cover the number of values it claims, which is what every decoder
// below needs before it can index the value safely.
func checkShape(r *xproto.GetPropertyReply, format byte) error {
	if r == nil {
		return fmt.Errorf("%w: no reply", ErrPropMalformed)
	}
	if r.Format != format {
		return fmt.Errorf("%w: format %d, want %d", ErrPropMalformed, r.Format, format)
	}
	need := uint64(r.ValueLen) * uint64(format/8)
	if uint64(len(r.Value)) < need {
		return fmt.Errorf("%w: %d bytes for %d values of format %d", ErrPropMalformed, len(r.Value), r.ValueLen, format)
	}
	return nil
}

// DecodeCard32 reads a property holding one 32-bit value, such as
// _NET_WM_PID. An empty property is malformed, not zero: a client that
// meant "no pid" would not set the property at all.
func DecodeCard32(r *xproto.GetPropertyReply) (uint32, error) {
	vals, err := DecodeCard32s(r)
	if err != nil {
		return 0, err
	}
	if len(vals) == 0 {
		return 0, fmt.Errorf("%w: no value", ErrPropMalformed)
	}
	return vals[0], nil
}

// DecodeCard32s reads a list of 32-bit values. An empty list is valid:
// _NET_CLIENT_LIST with nothing open, _NET_WM_STATE with no state set.
func DecodeCard32s(r *xproto.GetPropertyReply) ([]uint32, error) {
	if err := checkShape(r, 32); err != nil {
		return nil, err
	}
	vals := make([]uint32, r.ValueLen)
	for i := range vals {
		vals[i] = xgb.Get32(r.Value[4*i:])
	}
	return vals, nil
}

// DecodeAtoms reads a list of atoms, such as _NET_WM_WINDOW_TYPE.
func DecodeAtoms(r *xproto.GetPropertyReply) ([]xproto.Atom, error) {
	vals, err := DecodeCard32s(r)
	if err != nil {
		return nil, err
	}
	atoms := make([]xproto.Atom, len(vals))
	for i, v := range vals {
		atoms[i] = xproto.Atom(v)
	}
	return atoms, nil
}

// DecodeWindows reads a list of window ids, such as _NET_CLIENT_LIST.
func DecodeWindows(r *xproto.GetPropertyReply) ([]xproto.Window, error) {
	vals, err := DecodeCard32s(r)
	if err != nil {
		return nil, err
	}
	wins := make([]xproto.Window, len(vals))
	for i, v := range vals {
		wins[i] = xproto.Window(v)
	}
	return wins, nil
}

// DecodeString reads an 8-bit property as one string, bytes as stored.
// The encoding is the property type's business (STRING is latin-1,
// UTF8_STRING is UTF-8) and is not checked here.
func DecodeString(r *xproto.GetPropertyReply) (string, error) {
	if err := checkShape(r, 8); err != nil {
		return "", err
	}
	return string(r.Value[:r.ValueLen]), nil
}

// DecodeStrings reads an 8-bit property holding NUL-separated strings, such
// as WM_CLASS. A trailing NUL terminates the last string rather than
// starting an empty one, which is how ICCCM clients write it.
func DecodeStrings(r *xproto.GetPropertyReply) ([]string, error) {
	s, err := DecodeString(r)
	if err != nil {
		return nil, err
	}
	var strs []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			strs = append(strs, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		strs = append(strs, s[start:])
	}
	return strs, nil
}
