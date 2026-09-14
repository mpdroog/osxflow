package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/audio"
	"github.com/mpdroog/osxflow/internal/menu"
	"github.com/mpdroog/osxflow/internal/mpris"
)

type fakeActions struct{ calls []string }

func (f *fakeActions) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeActions) setVolume(output bool, name string, v float64, final bool) {
	f.record("volume %t %s %.2f %t", output, name, v, final)
}
func (f *fakeActions) setMute(output bool, name string, mute bool) {
	f.record("mute %t %s %t", output, name, mute)
}
func (f *fakeActions) setDefault(output bool, name string) { f.record("default %t %s", output, name) }
func (f *fakeActions) playPause(bus string)                { f.record("playpause %s", bus) }
func (f *fakeActions) next(bus string)                     { f.record("next %s", bus) }
func (f *fakeActions) previous(bus string)                 { f.record("previous %s", bus) }
func (f *fakeActions) settings()                           { f.record("settings") }

func (f *fakeActions) click(fn func()) string {
	if fn == nil {
		return "inert"
	}
	f.calls = nil
	fn()
	return strings.Join(f.calls, "; ")
}

func kinds(rows []menu.Row) string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = fmt.Sprintf("%d:%s", rows[i].Kind, rows[i].Label)
	}
	return strings.Join(out, "|")
}

func row(k menu.Kind, label string) string { return fmt.Sprintf("%d:%s", k, label) }

const (
	speakers = "alsa_output.pci-0000_00_1f.3.analog-stereo"
	headset  = "alsa_output.usb-Razer_Razer_Kraken_Kitty_Edition_00000000-00.analog-stereo"
	mic      = "alsa_input.pci-0000_00_1f.3.analog-stereo"
)

// machine is this Mac as it was when soundmenu was written: built-in
// speakers at 25% and the built-in microphone at 100%.
func machine() view {
	return view{
		connected: true,
		audio: audio.State{
			Outputs: []audio.Device{{Name: speakers, Description: "Built-in Audio Analog Stereo", Volume: 0.25, Default: true}},
			Inputs:  []audio.Device{{Name: mic, Description: "Built-in Audio Analog Stereo", Volume: 1, Default: true}},
		},
	}
}

func TestBuildRows(t *testing.T) {
	v := machine()
	f := &fakeActions{}
	rows := buildRows(&v, f)
	want := strings.Join([]string{
		row(menu.Header, "Sound"),
		row(menu.Slider, ""),
		row(menu.Item, "Mute"),
		row(menu.Separator, ""),
		row(menu.Section, "Input"),
		row(menu.Slider, ""),
		row(menu.Item, "Mute microphone"),
		row(menu.Separator, ""),
		row(menu.Action, "Sound Settings…"),
	}, "|")
	if got := kinds(rows); got != want {
		t.Fatalf("rows:\n got %s\nwant %s", got, want)
	}
	if rows[0].Detail != "25%" || rows[1].Value != 0.25 || rows[5].Value != 1 {
		t.Errorf("header %q, output %v, input %v", rows[0].Detail, rows[1].Value, rows[5].Value)
	}

	rows[1].Slide(0.5, false)
	rows[5].Slide(0.3, true)
	if got := strings.Join(f.calls, "; "); got != "volume true "+speakers+" 0.50 false; volume false "+mic+" 0.30 true" {
		t.Errorf("sliders asked for: %s", got)
	}
	if got := f.click(rows[2].Click); got != "mute true "+speakers+" true" || !rows[2].KeepOpen {
		t.Errorf("mute switch: %s (keepOpen %t)", got, rows[2].KeepOpen)
	}
	if got := f.click(rows[6].Click); got != "mute false "+mic+" true" {
		t.Errorf("microphone switch: %s", got)
	}
	if got := f.click(rows[8].Click); got != "settings" {
		t.Errorf("settings: %s", got)
	}

	v.audio.Outputs[0].Muted = true
	rows = buildRows(&v, f)
	if rows[0].Detail != "Muted" || !rows[2].On || f.click(rows[2].Click) != "mute true "+speakers+" false" {
		t.Errorf("when muted: header %q, switch on %t", rows[0].Detail, rows[2].On)
	}
}

func TestBuildRowsOutputs(t *testing.T) {
	v := machine()
	v.audio.Outputs = append(v.audio.Outputs, audio.Device{Name: headset, Description: "Razer Kraken", Volume: 0.4, Headphones: true})
	f := &fakeActions{}
	rows := buildRows(&v, f)
	got := kinds(rows)
	section := row(menu.Section, "Output") + "|" + row(menu.Item, "Built-in Audio Analog Stereo") + "|" + row(menu.Item, "Razer Kraken")
	if !strings.Contains(got, section) {
		t.Fatalf("no output list in:\n%s", got)
	}
	var builtIn, razer *menu.Row
	for i := range rows {
		switch rows[i].Label {
		case "Built-in Audio Analog Stereo":
			if builtIn == nil {
				builtIn = &rows[i]
			}
		case "Razer Kraken":
			razer = &rows[i]
		}
	}
	if !builtIn.On || f.click(builtIn.Click) != "inert" {
		t.Error("the default output should be lit and inert")
	}
	if razer.On || f.click(razer.Click) != "default true "+headset || !razer.KeepOpen {
		t.Error("another output should switch the default and keep the menu open")
	}
}

func TestBuildRowsCapsDevices(t *testing.T) {
	v := machine()
	for i := range maxDevices + 4 {
		v.audio.Outputs = append(v.audio.Outputs, audio.Device{Name: fmt.Sprintf("out%d", i), Description: fmt.Sprintf("Output %d", i)})
	}
	n := strings.Count(kinds(buildRows(&v, &fakeActions{})), fmt.Sprintf("%d:Output ", menu.Item))
	if n+1 != maxDevices {
		t.Errorf("listed %d extra outputs besides the default, want %d", n, maxDevices-1)
	}
}

func TestBuildRowsDisconnected(t *testing.T) {
	v := view{}
	want := strings.Join([]string{
		row(menu.Header, "Sound"), row(menu.Note, "Sound server not running"),
		row(menu.Separator, ""), row(menu.Action, "Sound Settings…"),
	}, "|")
	if got := kinds(buildRows(&v, &fakeActions{})); got != want {
		t.Errorf("disconnected:\n got %s\nwant %s", got, want)
	}
	v.connected = true
	if got := kinds(buildRows(&v, &fakeActions{})); !strings.Contains(got, "No sound output") {
		t.Errorf("connected without outputs:\n%s", got)
	}
}

func TestBuildRowsPlayer(t *testing.T) {
	v := machine()
	v.players = []mpris.Player{
		{BusName: "org.mpris.MediaPlayer2.vlc", Identity: "VLC", Status: mpris.StatusPlaying,
			Title: "Song", Artist: "Band", CanPause: true, CanPlay: true, CanGoNext: true},
		{BusName: "org.mpris.MediaPlayer2.firefox", Identity: "Firefox", Status: mpris.StatusPaused},
	}
	f := &fakeActions{}
	rows := buildRows(&v, f)
	got := kinds(rows)
	if !strings.Contains(got, row(menu.Section, "Now Playing")+"|"+row(menu.Item, "Song")+"|"+row(menu.Transport, "")) {
		t.Fatalf("no player rows in:\n%s", got)
	}
	if strings.Contains(got, "Firefox") {
		t.Error("only the first player should be shown")
	}
	var track, transport *menu.Row
	for i := range rows {
		switch {
		case rows[i].Label == "Song":
			track = &rows[i]
		case rows[i].Kind == menu.Transport:
			transport = &rows[i]
		}
	}
	if track.Detail != "Band" || !track.On || f.click(track.Click) != "playpause org.mpris.MediaPlayer2.vlc" {
		t.Errorf("track row: detail %q on %t", track.Detail, track.On)
	}
	b := transport.Buttons
	if len(b) != 3 || f.click(b[0].Click) != "inert" || f.click(b[1].Click) != "playpause org.mpris.MediaPlayer2.vlc" ||
		f.click(b[2].Click) != "next org.mpris.MediaPlayer2.vlc" {
		t.Error("transport: previous should be inert (cannot go back), play/pause and next live")
	}
}

func TestPlayerRowsLabels(t *testing.T) {
	for _, tc := range []struct {
		p             mpris.Player
		label, detail string
	}{
		{mpris.Player{Identity: "VLC", Title: "Song", Artist: "Band"}, "Song", "Band"},
		{mpris.Player{Identity: "VLC", Title: "Song"}, "Song", "VLC"},
		{mpris.Player{Identity: "VLC", Artist: "Band"}, "VLC", ""},
	} {
		rows := playerRows(&tc.p, &fakeActions{})
		if rows[0].Label != tc.label || rows[0].Detail != tc.detail {
			t.Errorf("%+v: label %q detail %q, want %q %q", tc.p, rows[0].Label, rows[0].Detail, tc.label, tc.detail)
		}
		// A stopped player that cannot play gets no live play button.
		if rows[0].Click != nil || rows[1].Buttons[1].Click != nil {
			t.Errorf("%+v: play is live though the player cannot play", tc.p)
		}
	}
}

func TestWavesFor(t *testing.T) {
	for _, tc := range []struct {
		v     float64
		muted bool
		want  int
	}{{0, false, 0}, {0.01, false, 1}, {0.33, false, 1}, {0.34, false, 2}, {0.66, false, 2}, {0.67, false, 3}, {1.5, false, 3}, {0.9, true, 0}} {
		if got := wavesFor(tc.v, tc.muted); got != tc.want {
			t.Errorf("wavesFor(%v, %t) = %d, want %d", tc.v, tc.muted, got, tc.want)
		}
	}
}
