// Package upower reads the battery's state from UPower, over the system
// bus.
//
// It is the slice a battery menu needs: how full, charging or not, how
// long until empty or full, and how worn the battery is. Nothing here knows
// about a display, so the same package serves an X11 menu today and a
// Wayland one if that ever comes.
//
// This file is the pure half: the types and the words a menu shows for
// them, tested without a bus. client.go talks to the bus.
package upower

import (
	"fmt"
	"strings"
	"time"
)

// State is UPower's UpDeviceState.
type State uint32

// The states a battery reports.
const (
	StateUnknown State = iota
	StateCharging
	StateDischarging
	StateEmpty
	StateFullyCharged
	StatePendingCharge
	StatePendingDischarge
)

// Battery is the machine's battery at one moment.
type Battery struct {
	// Percentage is how full, 0 to 100.
	Percentage float64
	State      State

	// TimeToEmpty and TimeToFull are UPower's estimates, zero while it has
	// none -- which is the case for a minute or so after the power cable
	// goes in or out.
	TimeToEmpty time.Duration
	TimeToFull  time.Duration

	// Capacity is the battery's health: its full charge as a percentage of
	// its design capacity. 0 when unknown.
	Capacity float64

	// Cycles is the charge cycle count, -1 when unknown.
	Cycles int

	// OnBattery is whether the machine is running from the battery rather
	// than the power adapter.
	OnBattery bool
}

// Charging reports whether the battery is on the charger: charging, or
// holding off charging while plugged in. A battery that is full, or that
// the firmware has decided not to top up yet, is still on the charger, and
// an icon should say so with its bolt.
func (b *Battery) Charging() bool {
	if b.State == StateCharging {
		return true
	}
	return !b.OnBattery && (b.State == StateFullyCharged || b.State == StatePendingCharge)
}

// Remaining says in words how long the battery has left, or how long until
// it is full: "1:54 remaining", "1:05 until full". It is "" while
// discharging without an estimate yet, since saying nothing beats a guess.
func Remaining(b *Battery) string {
	switch b.State {
	case StateCharging:
		if b.TimeToFull > 0 {
			return clock(b.TimeToFull) + " until full"
		}
		return "Charging"
	case StateFullyCharged:
		return "Fully charged"
	case StatePendingCharge:
		// Plugged in and not charging: firmware holding a battery below
		// full to spare it, or a charger too weak to keep up.
		return "Not charging"
	case StateDischarging, StatePendingDischarge:
		if b.TimeToEmpty > 0 {
			return clock(b.TimeToEmpty) + " remaining"
		}
	case StateUnknown, StateEmpty:
	}
	return ""
}

// Health says how worn the battery is: "Health 67% · 424 cycles". A part
// that is unknown is left out, and with both unknown it is "".
func Health(b *Battery) string {
	parts := make([]string, 0, 2)
	if b.Capacity > 0 {
		parts = append(parts, fmt.Sprintf("Health %.0f%%", b.Capacity))
	}
	switch {
	case b.Cycles == 1:
		parts = append(parts, "1 cycle")
	case b.Cycles > 1:
		parts = append(parts, fmt.Sprintf("%d cycles", b.Cycles))
	}
	return strings.Join(parts, " · ")
}

// clock formats a duration as hours and minutes, rounded to the minute.
func clock(d time.Duration) string {
	minutes := int64((d + 30*time.Second) / time.Minute)
	return fmt.Sprintf("%d:%02d", minutes/60, minutes%60)
}
