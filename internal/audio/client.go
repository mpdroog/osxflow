package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jfreymuth/pulse/proto"
)

// Timeout bounds connecting and every request. A local server answers in
// well under a millisecond; one that takes seconds is stuck, and a menu
// waiting on it would be too.
const Timeout = 2 * time.Second

// invalidIndex is PA_INVALID_INDEX: "no index, go by the name".
const invalidIndex = 0xFFFFFFFF

// cookieSize is what the server expects. With no cookie file, any cookie
// of this size is accepted by a server running with anonymous auth, which
// pipewire-pulse does for local clients.
const cookieSize = 256

// Client is a connection to the sound server.
//
// It speaks the protocol through the library's proto package rather than
// its high-level client. That client installs the connection's only event
// callback itself and uses it for its own streams, dropping the sink,
// source and server events a volume display needs.
type Client struct {
	conn    net.Conn
	proto   *proto.Client
	changes chan struct{}

	mu     sync.Mutex
	closed bool
}

// Connect connects to the user's sound server: PULSE_SERVER when it names
// a local socket, otherwise the one in XDG_RUNTIME_DIR. appName is how the
// client is listed in tools like pavucontrol.
func Connect(appName string) (*Client, error) {
	addr, err := serverAddress(os.Getenv("PULSE_SERVER"), os.Getenv("XDG_RUNTIME_DIR"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to the sound server: %w", err)
	}
	c := &Client{conn: conn, changes: make(chan struct{}, 1)}
	// The callback is set before Open, which starts the goroutine that
	// reads it; set after, it would be a data race.
	p := &proto.Client{Callback: c.event}
	p.SetTimeout(Timeout)
	p.Open(&watchedConn{rw: conn, failed: c.shutdown})
	c.proto = p

	if err := c.handshake(appName); err != nil {
		return nil, errors.Join(fmt.Errorf("connecting to the sound server at %s: %w", addr, err), c.Close())
	}
	return c, nil
}

func (c *Client) handshake(appName string) error {
	cookie, err := readCookie()
	if err != nil {
		return err
	}
	var auth proto.AuthReply
	if err := c.proto.Request(&proto.Auth{Version: c.proto.Version(), Cookie: cookie}, &auth); err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}
	c.proto.SetVersion(auth.Version)

	props := proto.PropList{
		"application.name":       proto.PropListString(appName),
		"application.process.id": proto.PropListString(strconv.Itoa(os.Getpid())),
	}
	if err := c.proto.Request(&proto.SetClientName{Props: props}, &proto.SetClientNameReply{}); err != nil {
		return fmt.Errorf("naming the client: %w", err)
	}
	mask := proto.SubscriptionMaskSink | proto.SubscriptionMaskSource | proto.SubscriptionMaskServer
	if err := c.proto.Request(&proto.Subscribe{Mask: mask}, nil); err != nil {
		return fmt.Errorf("subscribing to device changes: %w", err)
	}
	return nil
}

// serverAddress finds the socket to connect to.
//
// PULSE_SERVER is a space-separated list of addresses; the first local
// socket in it is used, written "unix:/path" or as a bare path. A list with
// only network addresses is refused rather than silently ignored: someone
// set it on purpose.
func serverAddress(pulseServer, runtimeDir string) (string, error) {
	if strings.TrimSpace(pulseServer) != "" {
		for _, s := range strings.Fields(pulseServer) {
			// "{machine-id}unix:/path" restricts an address to one machine;
			// the socket path is what matters here.
			if i := strings.IndexByte(s, '}'); strings.HasPrefix(s, "{") && i > 0 {
				s = s[i+1:]
			}
			path := strings.TrimPrefix(s, "unix:")
			if filepath.IsAbs(path) {
				return path, nil
			}
		}
		return "", fmt.Errorf("PULSE_SERVER=%q names no local socket; only unix: addresses are supported", pulseServer)
	}
	if runtimeDir == "" {
		return "", errors.New("neither PULSE_SERVER nor XDG_RUNTIME_DIR is set; cannot find the sound server")
	}
	return filepath.Join(runtimeDir, "pulse", "native"), nil
}

// readCookie reads the authentication cookie, from PULSE_COOKIE or the
// usual place. No cookie file is the ordinary case with pipewire-pulse,
// which authenticates local clients by their socket instead; the protocol
// still wants the field filled.
func readCookie() ([]byte, error) {
	path := os.Getenv("PULSE_COOKIE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("finding the pulse cookie: %w", err)
		}
		path = filepath.Join(home, ".config", "pulse", "cookie")
	}
	cookie, err := os.ReadFile(path) //nolint:gosec // the path is the user's own cookie file, from their environment
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return make([]byte, cookieSize), nil
	case err != nil:
		return nil, fmt.Errorf("reading the pulse cookie: %w", err)
	}
	return cookie, nil
}

// Changes reports that an output, an input or the server's defaults
// changed. A burst of changes is one receive; read a Snapshot after it
// fires. The channel closes when the connection does, whichever side
// closes it.
func (c *Client) Changes() <-chan struct{} { return c.changes }

// event runs on the library's reader goroutine, for every message the
// server sends unasked. It must not block and must not make a request: the
// reply would have to be read by the goroutine that is running it.
func (c *Client) event(msg any) {
	switch msg.(type) {
	case *proto.SubscribeEvent:
		c.notify()
	case *proto.ConnectionClosed:
		c.shutdown()
	}
}

func (c *Client) notify() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.changes <- struct{}{}:
	default:
		// A change is already waiting, and it covers this one.
	}
}

// shutdown closes Changes, once. It runs when the connection fails to
// read -- the library reports only a clean end of stream as closed, and a
// server killed mid-message is not one -- and when Close is called.
func (c *Client) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.changes)
}

// Snapshot reads every output and input, and which are the defaults.
func (c *Client) Snapshot() (State, error) {
	var server proto.GetServerInfoReply
	if err := c.proto.Request(&proto.GetServerInfo{}, &server); err != nil {
		return State{}, fmt.Errorf("reading the sound server's defaults: %w", err)
	}
	var sinks proto.GetSinkInfoListReply
	if err := c.proto.Request(&proto.GetSinkInfoList{}, &sinks); err != nil {
		return State{}, fmt.Errorf("listing outputs: %w", err)
	}
	var sources proto.GetSourceInfoListReply
	if err := c.proto.Request(&proto.GetSourceInfoList{}, &sources); err != nil {
		return State{}, fmt.Errorf("listing inputs: %w", err)
	}
	return newState(&server, sinks, sources), nil
}

// SetVolume sets every channel of an output (output true) or input to
// volume, clamped to 0..1.
//
// The device is read first for its channel count: the server rejects a
// volume with a different number of channels from the device's.
func (c *Client) SetVolume(output bool, name string, volume float64) error {
	if output {
		var info proto.GetSinkInfoReply
		if err := c.proto.Request(&proto.GetSinkInfo{SinkIndex: invalidIndex, SinkName: name}, &info); err != nil {
			return fmt.Errorf("reading output %s: %w", name, err)
		}
		req := &proto.SetSinkVolume{SinkIndex: invalidIndex, SinkName: name, ChannelVolumes: Volumes(len(info.ChannelVolumes), volume)}
		if err := c.proto.Request(req, nil); err != nil {
			return fmt.Errorf("setting the volume of output %s: %w", name, err)
		}
		return nil
	}
	var info proto.GetSourceInfoReply
	if err := c.proto.Request(&proto.GetSourceInfo{SourceIndex: invalidIndex, SourceName: name}, &info); err != nil {
		return fmt.Errorf("reading input %s: %w", name, err)
	}
	req := &proto.SetSourceVolume{SourceIndex: invalidIndex, SourceName: name, ChannelVolumes: Volumes(len(info.ChannelVolumes), volume)}
	if err := c.proto.Request(req, nil); err != nil {
		return fmt.Errorf("setting the volume of input %s: %w", name, err)
	}
	return nil
}

// SetMute mutes or unmutes an output (output true) or input.
func (c *Client) SetMute(output bool, name string, mute bool) error {
	var req proto.RequestArgs = &proto.SetSourceMute{SourceIndex: invalidIndex, SourceName: name, Mute: mute}
	kind := "input"
	if output {
		req, kind = &proto.SetSinkMute{SinkIndex: invalidIndex, SinkName: name, Mute: mute}, "output"
	}
	if err := c.proto.Request(req, nil); err != nil {
		return fmt.Errorf("muting %s %s: %w", kind, name, err)
	}
	return nil
}

// SetDefault makes a device the default output (output true) or input.
// The server moves the streams that follow the default along with it.
func (c *Client) SetDefault(output bool, name string) error {
	var req proto.RequestArgs = &proto.SetDefaultSource{SourceName: name}
	kind := "input"
	if output {
		req, kind = &proto.SetDefaultSink{SinkName: name}, "output"
	}
	if err := c.proto.Request(req, nil); err != nil {
		return fmt.Errorf("making %s %s the default: %w", kind, name, err)
	}
	return nil
}

// Close ends the connection and closes Changes. Closing a connection that
// is already closed -- by an earlier Close, or by a failed Connect --
// returns nil.
func (c *Client) Close() error {
	c.shutdown()
	if err := c.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("closing the sound server connection: %w", err)
	}
	return nil
}

// watchedConn tells the client when reading fails, however it fails.
type watchedConn struct {
	rw     io.ReadWriter
	failed func()
}

func (w *watchedConn) Read(p []byte) (int, error) {
	n, err := w.rw.Read(p)
	if err != nil {
		// The error itself reaches every caller through the library, which
		// fails each request after it with this error.
		w.failed()
	}
	return n, err
}

func (w *watchedConn) Write(p []byte) (int, error) { return w.rw.Write(p) }
