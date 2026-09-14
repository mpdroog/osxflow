// Package backlight reads and sets the brightness of the screen and the
// keyboard.
//
// Reading is sysfs, which anyone may read. Writing sysfs needs root, so
// setting goes through logind's SetBrightness instead, which lets the user
// of the active session change the brightness of their own machine without
// a setuid helper or a udev rule -- the same path GNOME uses, and the one
// xfce4-power-manager's pkexec helper exists to work around.
package backlight

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// DefaultRoot is where the kernel puts device classes.
const DefaultRoot = "/sys/class"

// The subsystems logind's SetBrightness accepts.
const (
	SubsystemBacklight = "backlight"
	SubsystemLEDs      = "leds"
)

// ErrNotFound means the machine has no such device: no backlight on an
// external monitor setup, no keyboard light on most keyboards.
var ErrNotFound = errors.New("no such brightness device")

// Device is one brightness control.
type Device struct {
	// Subsystem and Name identify the device the way logind's
	// SetBrightness wants it: "backlight" and "acpi_video0", or "leds" and
	// "spi::kbd_backlight".
	Subsystem string
	Name      string

	Max        int
	Brightness int

	// MinRaw is the lowest value a caller may set: 1 for a screen, since a
	// slider dragged to the end should dim it, not black it out with no way
	// to see the slider again; 0 for a keyboard, which may go dark.
	MinRaw int
}

// Fraction is the brightness as a fraction of the maximum, 0 to 1.
func (d *Device) Fraction() float64 {
	if d.Max <= 0 {
		return 0
	}
	return math.Max(0, math.Min(1, float64(d.Brightness)/float64(d.Max)))
}

// RawFor turns a fraction, as a slider gives it, into a raw value to set.
func (d *Device) RawFor(fraction float64) int {
	if math.IsNaN(fraction) {
		fraction = 0
	}
	fraction = math.Max(0, math.Min(1, fraction))
	return d.clamp(int(math.Round(fraction * float64(d.Max))))
}

// clamp keeps a raw value within [MinRaw, Max].
func (d *Device) clamp(raw int) int {
	lo := max(d.MinRaw, 0)
	hi := max(d.Max, lo)
	return min(max(raw, lo), hi)
}

// Sysfs finds and reads devices under a sysfs class directory.
type Sysfs struct {
	// Root is DefaultRoot in production, a temporary directory in tests.
	Root string
}

func (s Sysfs) root() string {
	if s.Root == "" {
		return DefaultRoot
	}
	return s.Root
}

// typeRank orders backlight interfaces by the kernel's documented
// preference (Documentation/ABI/stable/sysfs-class-backlight): "firmware
// (ACPI) interfaces should be preferred over platform interfaces, which
// should be preferred over raw interfaces". A higher rank wins.
var typeRank = map[string]int{
	"raw":      1,
	"platform": 2,
	"firmware": 3,
}

// Screen finds the screen's backlight.
//
// When there are several -- a laptop often has both an ACPI interface and
// the GPU's own -- the kernel's preference picks one. A device that could
// not be read is passed over, and why is returned alongside the one chosen;
// with none chosen, the error is that, or ErrNotFound when there was
// simply nothing there.
func (s Sysfs) Screen() (*Device, error) {
	dir := filepath.Join(s.root(), SubsystemBacklight)
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}

	var (
		best     *Device
		bestRank int
		errs     []error
	)
	// ReadDir sorts by name, so between equals the first name wins, and
	// the choice is the same on every run.
	for _, e := range entries {
		name := e.Name()
		typ, typeErr := readString(filepath.Join(dir, name, "type"))
		if typeErr != nil {
			errs = append(errs, typeErr)
			continue
		}
		rank, known := typeRank[typ]
		if !known {
			errs = append(errs, fmt.Errorf("%s: unknown backlight type %q", name, typ))
			continue
		}
		if best != nil && rank <= bestRank {
			continue
		}
		d := &Device{Subsystem: SubsystemBacklight, Name: name, MinRaw: 1}
		if loadErr := s.load(d); loadErr != nil {
			errs = append(errs, loadErr)
			continue
		}
		best, bestRank = d, rank
	}
	if best == nil {
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		return nil, ErrNotFound
	}
	return best, errors.Join(errs...)
}

// Keyboard finds the keyboard's backlight: the LED whose name says
// kbd_backlight, which is the naming convention every keyboard light
// driver follows ("spi::kbd_backlight", "dell::kbd_backlight").
//
// Errors are returned as for Screen.
func (s Sysfs) Keyboard() (*Device, error) {
	dir := filepath.Join(s.root(), SubsystemLEDs)
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		if !strings.Contains(e.Name(), "kbd_backlight") {
			continue
		}
		d := &Device{Subsystem: SubsystemLEDs, Name: e.Name()}
		if loadErr := s.load(d); loadErr != nil {
			errs = append(errs, loadErr)
			continue
		}
		return d, errors.Join(errs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotFound
}

// load reads a device's maximum and its brightness.
func (s Sysfs) load(d *Device) error {
	dir := filepath.Join(s.root(), d.Subsystem, d.Name)
	maxValue, err := readValue(filepath.Join(dir, "max_brightness"))
	if err != nil {
		return err
	}
	d.Max = maxValue
	return s.Read(d)
}

// Read refreshes a device's brightness, which changes under us whenever
// the brightness keys are pressed.
//
// For a backlight, actual_brightness is read when it exists: it is what
// the hardware reports, where brightness is only what was last asked for,
// and the two differ while firmware is still fading between them.
func (s Sysfs) Read(d *Device) error {
	dir := filepath.Join(s.root(), d.Subsystem, d.Name)
	if d.Subsystem == SubsystemBacklight {
		v, err := readValue(filepath.Join(dir, "actual_brightness"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Not every driver has it; brightness is next best.
		case err != nil:
			return err
		default:
			d.Brightness = v
			return nil
		}
	}
	v, err := readValue(filepath.Join(dir, "brightness"))
	if err != nil {
		return err
	}
	d.Brightness = v
	return nil
}

func readString(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // paths are built from the sysfs class root and directory entries under it
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func readValue(path string) (int, error) {
	b, err := os.ReadFile(path) //nolint:gosec // paths are built from the sysfs class root and directory entries under it
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	v, err := parseValue(b)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// maxValueLen bounds what an error quotes: a sysfs value is a few digits,
// and anything longer is garbage not worth repeating in full.
const maxValueLen = 32

// parseValue reads a sysfs number: decimal, surrounded by whitespace --
// the kernel ends every value with a newline -- and never negative.
func parseValue(b []byte) (int, error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, errors.New("empty value")
	}
	quoted := s
	if len(quoted) > maxValueLen {
		quoted = quoted[:maxValueLen] + "…"
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		// strconv's NumError quotes the whole input again; keep only its
		// cause (ErrSyntax or ErrRange), so the error stays short.
		var numErr *strconv.NumError
		if errors.As(err, &numErr) {
			err = numErr.Err
		}
		return 0, fmt.Errorf("value %q is not a number: %w", quoted, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("value %d is negative", v)
	}
	return v, nil
}

const (
	login1Name    = "org.freedesktop.login1"
	sessionPath   = dbus.ObjectPath("/org/freedesktop/login1/session/auto")
	setBrightness = "org.freedesktop.login1.Session.SetBrightness"
)

// Setter changes brightness through logind.
type Setter struct {
	conn *dbus.Conn
}

// NewSetter returns a setter on conn, which should be the system bus.
func NewSetter(conn *dbus.Conn) *Setter {
	return &Setter{conn: conn}
}

// Set asks logind to set a device's brightness, clamped to [MinRaw, Max],
// and records the value in d once logind has accepted it.
//
// The session is "auto": logind resolves it to the caller's own, which is
// what grants the permission. A process outside the user's session -- a
// system service, a cron job -- is refused.
func (s *Setter) Set(ctx context.Context, d *Device, raw int) error {
	v := d.clamp(raw)
	if err := s.conn.Object(login1Name, sessionPath).
		CallWithContext(ctx, setBrightness, 0, d.Subsystem, d.Name, toUint32(v)).Err; err != nil {
		return fmt.Errorf("setting %s brightness to %d: %w", d.Name, v, err)
	}
	d.Brightness = v
	return nil
}

func toUint32(v int) uint32 {
	switch {
	case v < 0:
		return 0
	case v > math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(v)
}
