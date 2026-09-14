package audio

import (
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jfreymuth/pulse/proto"
)

func props(kv ...string) proto.PropList {
	pl := proto.PropList{}
	for i := 0; i+1 < len(kv); i += 2 {
		pl[kv[i]] = proto.PropListString(kv[i+1])
	}
	return pl
}

func TestFraction(t *testing.T) {
	for _, tc := range []struct {
		name string
		cv   proto.ChannelVolumes
		want float64
	}{
		{"no channels", nil, 0},
		{"muted", proto.ChannelVolumes{proto.VolumeMuted, proto.VolumeMuted}, 0},
		{"full", proto.ChannelVolumes{proto.VolumeNorm, proto.VolumeNorm}, 1},
		{"quarter", proto.ChannelVolumes{proto.VolumeNorm / 4, proto.VolumeNorm / 4}, 0.25},
		{"unbalanced", proto.ChannelVolumes{proto.VolumeNorm, 0}, 0.5},
		{"amplified", proto.ChannelVolumes{proto.VolumeNorm * 3 / 2}, 1.5},
		// An invalid volume counts as silent, not as four billion percent.
		{"invalid", proto.ChannelVolumes{proto.VolumeInvalid, proto.VolumeNorm}, 0.5},
	} {
		if got := Fraction(tc.cv); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: Fraction = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func FuzzFraction(f *testing.F) {
	f.Add([]byte{0, 0, 1, 0, 0, 0, 1, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		cv := make(proto.ChannelVolumes, 0, len(raw)/4)
		for i := 0; i+3 < len(raw); i += 4 {
			cv = append(cv, proto.Volume(uint32(raw[i])<<24|uint32(raw[i+1])<<16|uint32(raw[i+2])<<8|uint32(raw[i+3])))
		}
		got := Fraction(cv)
		if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 {
			t.Fatalf("Fraction(%v) = %v", cv, got)
		}
	})
}

func TestVolumes(t *testing.T) {
	for _, tc := range []struct {
		n     int
		f     float64
		wantN int
		want  proto.Volume
	}{
		{2, 0.25, 2, proto.VolumeNorm / 4},
		{0, 0.5, 1, proto.VolumeNorm / 2}, // a device with no channels still gets a valid request
		{-3, 1, 1, proto.VolumeNorm},
		{100, 1, maxChannels, proto.VolumeNorm},
		{2, 1.7, 2, proto.VolumeNorm}, // a slider never amplifies
		{2, -1, 2, proto.VolumeMuted},
		{2, math.NaN(), 2, proto.VolumeMuted},
	} {
		cv := Volumes(tc.n, tc.f)
		if len(cv) != tc.wantN {
			t.Errorf("Volumes(%d, %v) has %d channels, want %d", tc.n, tc.f, len(cv), tc.wantN)
			continue
		}
		for _, v := range cv {
			if v != tc.want {
				t.Errorf("Volumes(%d, %v) = %v, want every channel %v", tc.n, tc.f, cv, tc.want)
				break
			}
		}
	}
	// Round trip: what a slider sets is what it reads back.
	for _, f := range []float64{0, 0.01, 0.25, 0.5, 0.99, 1} {
		if got := Fraction(Volumes(2, f)); math.Abs(got-f) > 1e-4 {
			t.Errorf("Fraction(Volumes(2, %v)) = %v", f, got)
		}
	}
}

func TestDescription(t *testing.T) {
	for _, tc := range []struct {
		name  string
		props proto.PropList
		want  string
	}{
		{"alsa_output.x", props(propDescription, "Built-in Audio Analog Stereo"), "Built-in Audio Analog Stereo"},
		{"alsa_output.x", props(), "alsa_output.x"},
		{"alsa_output.x", props(propDescription, "  "), "alsa_output.x"},
		{"alsa_output.x", props(propDescription, "Kraken\nKitty"), "Kraken Kitty"},
		// Not NUL-terminated: not a string, so the name is used rather than
		// the library's "<not a string>".
		{"alsa_output.x", proto.PropList{propDescription: proto.PropListEntry("raw")}, "alsa_output.x"},
		{"alsa_output.x", proto.PropList{propDescription: proto.PropListEntry{}}, "alsa_output.x"},
	} {
		if got := Description(tc.name, tc.props); got != tc.want {
			t.Errorf("Description(%q, %v) = %q, want %q", tc.name, tc.props, got, tc.want)
		}
	}
}

func FuzzDescription(f *testing.F) {
	f.Add("name", []byte("Built-in Audio\x00"))
	f.Add("", []byte("\xff\n\x00"))
	f.Add("x", []byte{})
	f.Fuzz(func(t *testing.T, name string, entry []byte) {
		got := Description(name, proto.PropList{propDescription: proto.PropListEntry(entry)})
		if !utf8.ValidString(got) {
			t.Fatalf("Description = %q, not valid UTF-8", got)
		}
		if strings.IndexFunc(got, unicode.IsControl) >= 0 {
			t.Fatalf("Description = %q, contains a control character", got)
		}
		if got == "<not a string>" {
			t.Fatal("the library's placeholder leaked through")
		}
	})
}

func TestHeadphones(t *testing.T) {
	for _, tc := range []struct {
		props proto.PropList
		want  bool
	}{
		{props(), false},
		{props(propFormFactor, "internal", propBus, "pci"), false},
		{props(propFormFactor, "headset"), true},
		{props(propFormFactor, "headphone"), true},
		{props(propBus, "usb"), true},
		{props(propBus, "bluetooth"), true},
		{props(propFormFactor, "speaker", propBus, "hdmi"), false},
	} {
		if got := headphones(tc.props); got != tc.want {
			t.Errorf("headphones(%v) = %t, want %t", tc.props, got, tc.want)
		}
	}
}

func TestNewState(t *testing.T) {
	server := &proto.GetServerInfoReply{
		DefaultSinkName:   "alsa_output.pci-0000_00_1f.3.analog-stereo",
		DefaultSourceName: "alsa_input.pci-0000_00_1f.3.analog-stereo",
	}
	sinks := proto.GetSinkInfoListReply{
		{
			SinkIndex: 104, SinkName: "alsa_output.usb-Razer_Razer_Kraken_Kitty_Edition-00.analog-stereo",
			ChannelVolumes: proto.ChannelVolumes{proto.VolumeNorm, proto.VolumeNorm}, MonitorSourceIndex: 201,
			Properties: props(propDescription, "Razer Kraken Kitty Edition", propBus, "usb"),
		},
		nil, // a reply list can hold nils; they are skipped
		{
			SinkIndex: 47, SinkName: "alsa_output.pci-0000_00_1f.3.analog-stereo",
			ChannelVolumes: proto.ChannelVolumes{proto.VolumeNorm / 4, proto.VolumeNorm / 4}, Mute: true,
			MonitorSourceIndex: 200,
			Properties:         props(propDescription, "Built-in Audio Analog Stereo", propFormFactor, "internal"),
		},
	}
	sources := proto.GetSourceInfoListReply{
		// A monitor marked by its class.
		{SourceIndex: 200, SourceName: "alsa_output.pci-0000_00_1f.3.analog-stereo.monitor",
			Properties: props(propClass, "monitor")},
		// A monitor recognisable only from its sink.
		{SourceIndex: 201, SourceName: "razer.monitor", Properties: props()},
		{SourceIndex: 51, SourceName: "alsa_input.pci-0000_00_1f.3.analog-stereo",
			ChannelVolumes: proto.ChannelVolumes{proto.VolumeNorm},
			Properties:     props(propDescription, "Built-in Audio Analog Stereo")},
	}

	st := newState(server, sinks, sources)
	if len(st.Outputs) != 2 || len(st.Inputs) != 1 {
		t.Fatalf("got %d outputs and %d inputs, want 2 and 1: %+v", len(st.Outputs), len(st.Inputs), st)
	}
	builtin, razer := st.Outputs[0], st.Outputs[1]
	if builtin.Description != "Built-in Audio Analog Stereo" || !builtin.Default || !builtin.Muted ||
		builtin.Headphones || math.Abs(builtin.Volume-0.25) > 1e-9 || builtin.Index != 47 {
		t.Errorf("first output = %+v", builtin)
	}
	if razer.Description != "Razer Kraken Kitty Edition" || razer.Default || !razer.Headphones || razer.Volume != 1 {
		t.Errorf("second output = %+v", razer)
	}
	if in := st.DefaultInput(); in == nil || in.Index != 51 {
		t.Errorf("DefaultInput = %+v, want index 51", in)
	}
	if out := st.DefaultOutput(); out == nil || out.Index != 47 {
		t.Errorf("DefaultOutput = %+v, want index 47", out)
	}

	empty := newState(nil, nil, nil)
	if empty.DefaultOutput() != nil || empty.DefaultInput() != nil {
		t.Error("an empty state has a default")
	}
}

func TestServerAddress(t *testing.T) {
	for _, tc := range []struct {
		pulseServer, runtimeDir, want string
		wantErr                       bool
	}{
		{"", "/run/user/1000", "/run/user/1000/pulse/native", false},
		{"", "", "", true},
		{"unix:/tmp/pulse.sock", "/run/user/1000", "/tmp/pulse.sock", false},
		{"/tmp/pulse.sock", "", "/tmp/pulse.sock", false},
		{"tcp:localhost:4713 unix:/tmp/pulse.sock", "", "/tmp/pulse.sock", false},
		{"{machineid}unix:/tmp/pulse.sock", "", "/tmp/pulse.sock", false},
		{"tcp:localhost:4713", "/run/user/1000", "", true},
		{"unix:relative", "", "", true},
	} {
		got, err := serverAddress(tc.pulseServer, tc.runtimeDir)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("serverAddress(%q, %q) = %q, %v; want %q, error %t",
				tc.pulseServer, tc.runtimeDir, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestReadCookie(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PULSE_COOKIE", filepath.Join(dir, "missing"))
	cookie, err := readCookie()
	if err != nil || len(cookie) != cookieSize {
		t.Errorf("readCookie with no file = %d bytes, %v; want %d zero bytes", len(cookie), err, cookieSize)
	}

	path := filepath.Join(dir, "cookie")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PULSE_COOKIE", path)
	if cookie, err := readCookie(); err != nil || string(cookie) != "secret" {
		t.Errorf("readCookie = %q, %v", cookie, err)
	}

	// A directory where the cookie should be is not "no cookie".
	t.Setenv("PULSE_COOKIE", dir)
	if _, err := readCookie(); err == nil {
		t.Error("readCookie on a directory succeeded")
	}
}

func TestChangesCoalesceAndClose(t *testing.T) {
	c := &Client{changes: make(chan struct{}, 1)}
	c.event(&proto.SubscribeEvent{})
	c.event(&proto.SubscribeEvent{})
	c.event(&proto.Started{}) // not a change
	if _, ok := <-c.Changes(); !ok {
		t.Fatal("Changes closed instead of reporting a change")
	}
	select {
	case <-c.Changes():
		t.Fatal("two changes were not coalesced into one")
	default:
	}

	c.event(&proto.ConnectionClosed{})
	if _, ok := <-c.Changes(); ok {
		t.Fatal("Changes still open after the connection closed")
	}
	// After closing, further events and shutdowns must not panic.
	c.event(&proto.SubscribeEvent{})
	c.shutdown()
}

type failingRW struct{ err error }

func (f failingRW) Read([]byte) (int, error)    { return 0, f.err }
func (f failingRW) Write(p []byte) (int, error) { return len(p), nil }

func TestWatchedConnReportsReadFailure(t *testing.T) {
	failed := 0
	w := &watchedConn{rw: failingRW{err: errors.New("connection reset")}, failed: func() { failed++ }}
	if _, err := w.Read(make([]byte, 4)); err == nil {
		t.Fatal("Read did not return the error")
	}
	if failed != 1 {
		t.Errorf("failed called %d times, want 1", failed)
	}
	if n, err := w.Write([]byte("ab")); n != 2 || err != nil {
		t.Errorf("Write = %d, %v", n, err)
	}
	w = &watchedConn{rw: failingRW{err: io.EOF}, failed: func() { failed++ }}
	if _, err := w.Read(nil); !errors.Is(err, io.EOF) {
		t.Errorf("Read = %v, want EOF", err)
	}
}

// shortSocketPath returns a socket path short enough for the 108-byte
// limit, which go test's own temporary directories can exceed.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s")
	if len(path) >= 108 {
		t.Skipf("temporary socket path %q is too long for a unix socket", path)
	}
	return path
}

// A server that hangs up during the handshake fails Connect promptly and
// cleanly, rather than hanging or leaving a half-open client.
func TestConnectServerHangsUp(t *testing.T) {
	path := shortSocketPath(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := ln.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Error(closeErr)
		}
	})
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return // the listener closed first; the test reports on Connect
		}
		buf := make([]byte, 64)
		if _, readErr := conn.Read(buf); readErr != nil {
			t.Logf("fake server read: %v", readErr)
		}
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("fake server close: %v", closeErr)
		}
	}()

	t.Setenv("PULSE_SERVER", "unix:"+path)
	t.Setenv("PULSE_COOKIE", filepath.Join(filepath.Dir(path), "no-cookie"))
	start := time.Now()
	c, err := Connect("test")
	if err == nil {
		t.Fatalf("Connect to a server that hangs up succeeded: %+v", c)
	}
	if time.Since(start) > Timeout {
		t.Errorf("Connect took %v, longer than the timeout", time.Since(start))
	}
}

func TestConnectNoServer(t *testing.T) {
	t.Setenv("PULSE_SERVER", "unix:"+shortSocketPath(t))
	if _, err := Connect("test"); err == nil {
		t.Error("Connect with no server listening succeeded")
	}
	t.Setenv("PULSE_SERVER", "tcp:localhost:4713")
	if _, err := Connect("test"); err == nil {
		t.Error("Connect to a network-only PULSE_SERVER succeeded")
	}
}
