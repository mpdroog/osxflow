// Package audio reads and sets output and input volumes through the
// PulseAudio protocol, which PipeWire speaks too through pipewire-pulse.
//
// It is the slice a sound menu needs: the outputs and inputs, which is the
// default, their volume and mute, and changing those. Nothing here plays
// or records sound.
//
// This file is the pure half: turning the server's replies into devices,
// testable without a server. client.go talks to the server.
package audio

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/jfreymuth/pulse/proto"
)

// Device is one output (a sink) or input (a source).
type Device struct {
	Index uint32

	// Name is the server's identifier, which is what changing the device
	// needs. Description is what a person reads: "Built-in Audio Analog
	// Stereo" rather than "alsa_output.pci-0000_00_1f.3.analog-stereo".
	Name        string
	Description string

	// Volume is the average of the channels, 1.0 meaning 100%. It can be
	// above 1: the server allows amplification.
	Volume float64
	Muted  bool

	Default bool

	// Headphones marks a device worn rather than sitting in the room: a
	// headset or headphones by form factor, or anything on Bluetooth or
	// USB. It only chooses a glyph, so a USB speaker drawn as headphones
	// costs nothing.
	Headphones bool
}

// State is every output and input at one moment, each list sorted by
// description.
type State struct {
	Outputs []Device

	// Inputs leaves out monitor sources, which are the server's recordings
	// of each output rather than microphones.
	Inputs []Device
}

// DefaultOutput returns the default output, or nil when there is none.
func (s *State) DefaultOutput() *Device { return findDefault(s.Outputs) }

// DefaultInput returns the default input, or nil when there is none.
func (s *State) DefaultInput() *Device { return findDefault(s.Inputs) }

func findDefault(devices []Device) *Device {
	for i := range devices {
		if devices[i].Default {
			return &devices[i]
		}
	}
	return nil
}

// Property names from the server's property lists.
const (
	propDescription = "device.description"
	propFormFactor  = "device.form_factor"
	propBus         = "device.bus"
	propClass       = "device.class"
)

// maxChannels is PA_CHANNELS_MAX. The server refuses more, so a device
// that claims more is given this many rather than a request that fails.
const maxChannels = 32

// Fraction turns a device's channel volumes into one level, 1.0 meaning
// 100%: the average, as pavucontrol shows it.
//
// A device with no channels is at 0 rather than a division by zero, and a
// channel with an invalid volume counts as silent.
func Fraction(cv proto.ChannelVolumes) float64 {
	if len(cv) == 0 {
		return 0
	}
	var sum float64
	for _, v := range cv {
		sum += v.Norm()
	}
	return sum / float64(len(cv))
}

// Volumes sets n channels all to fraction f, clamped to 0..1 -- a slider
// never amplifies -- with n clamped to what the server accepts.
func Volumes(n int, f float64) proto.ChannelVolumes {
	n = max(1, min(n, maxChannels))
	if math.IsNaN(f) {
		f = 0
	}
	v := proto.NormVolume(math.Max(0, math.Min(1, f)))
	cv := make(proto.ChannelVolumes, n)
	for i := range cv {
		cv[i] = v
	}
	return cv
}

// propString reads a string property. A property list entry is a
// NUL-terminated byte string; one that is not is not a string, and reads as
// absent rather than as the library's "<not a string>" placeholder.
func propString(props proto.PropList, key string) (string, bool) {
	e, ok := props[key]
	if !ok || len(e) == 0 || e[len(e)-1] != 0 {
		return "", false
	}
	return clean(string(e[:len(e)-1])), true
}

// Description is what to call a device: its description property when it
// has a usable one, otherwise its name.
func Description(name string, props proto.PropList) string {
	if d, ok := propString(props, propDescription); ok && d != "" {
		return d
	}
	return clean(name)
}

// clean makes a server string safe to draw on one line. Descriptions come
// from ALSA card names and Bluetooth device names, which the device itself
// supplies.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func headphones(props proto.PropList) bool {
	if ff, ok := propString(props, propFormFactor); ok && (ff == "headset" || ff == "headphone") {
		return true
	}
	bus, ok := propString(props, propBus)
	return ok && (bus == "bluetooth" || bus == "usb")
}

func isMonitor(props proto.PropList) bool {
	class, ok := propString(props, propClass)
	return ok && class == "monitor"
}

// newState builds a State from the three replies a snapshot takes.
func newState(server *proto.GetServerInfoReply, sinks proto.GetSinkInfoListReply, sources proto.GetSourceInfoListReply) State {
	st := State{
		Outputs: make([]Device, 0, len(sinks)),
		Inputs:  make([]Device, 0, len(sources)),
	}
	// A sink's monitor is recognisable from the sink's side too, which
	// covers a server that does not set device.class on it.
	monitors := make(map[uint32]bool, len(sinks))
	for _, s := range sinks {
		if s == nil {
			continue
		}
		monitors[s.MonitorSourceIndex] = true
		st.Outputs = append(st.Outputs, Device{
			Index:       s.SinkIndex,
			Name:        s.SinkName,
			Description: Description(s.SinkName, s.Properties),
			Volume:      Fraction(s.ChannelVolumes),
			Muted:       s.Mute,
			Default:     server != nil && s.SinkName == server.DefaultSinkName,
			Headphones:  headphones(s.Properties),
		})
	}
	for _, s := range sources {
		if s == nil || isMonitor(s.Properties) || monitors[s.SourceIndex] {
			continue
		}
		st.Inputs = append(st.Inputs, Device{
			Index:       s.SourceIndex,
			Name:        s.SourceName,
			Description: Description(s.SourceName, s.Properties),
			Volume:      Fraction(s.ChannelVolumes),
			Muted:       s.Mute,
			Default:     server != nil && s.SourceName == server.DefaultSourceName,
			Headphones:  headphones(s.Properties),
		})
	}
	sortDevices(st.Outputs)
	sortDevices(st.Inputs)
	return st
}

func sortDevices(devices []Device) {
	sort.SliceStable(devices, func(i, j int) bool {
		a, b := &devices[i], &devices[j]
		if a.Description != b.Description {
			return a.Description < b.Description
		}
		return a.Name < b.Name
	})
}
