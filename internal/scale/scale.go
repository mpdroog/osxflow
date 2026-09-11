// Package scale works out how large to draw.
//
// X11 has no notion of display scaling, so the number has to be recovered
// from wherever the desktop happened to record it. On this machine XFCE
// stores WindowScalingFactor=2 in its own settings file and sets no
// Xft.dpi at all, so the X resource database -- the usual answer -- is
// empty, and reading it alone gives a window drawn at half the size of
// everything around it.
//
// The sources are tried cheapest and most explicit first. None of them
// runs a subprocess: these tools are judged on how fast they appear, and
// spawning xfconf-query to find out how big to draw would cost more than
// the drawing.
package scale

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/xwin"
)

// DPI is the X11 default. Display scaling is applied to point sizes
// instead, so that one number controls everything.
const DPI = 96

// ErrUnset reports that a source does not configure a scale at all: the
// variable is not set, the file does not exist, the key is not in it.
// That is normal -- most desktops answer from exactly one source -- so
// Detect leaves it out of its error, except to say that nothing answered
// at all.
var ErrUnset = errors.New("scale not configured")

// Detect returns the display scale factor. The number is always usable:
// when nothing answers it is 1.
//
// A positive override wins outright, which is what the -scale flag is
// for; zero means "detect", and a negative, NaN or infinite one is
// reported and ignored. Otherwise the first source that answers wins. A source that is set
// but broken -- GDK_SCALE=two, an xsettings.xml that does not parse --
// does not stop the search: a broken higher-priority setting is no reason
// to ignore a good lower-priority one. It is not dropped either; the error
// joins every such failure, so the caller can log them. When no source
// answered, the scale is 1 and that alone is not an error.
func Detect(xu *xgbutil.XUtil, override float64) (float64, error) {
	var errs []error
	if override != 0 {
		if err := check(override); err != nil {
			errs = append(errs, fmt.Errorf("-scale %g: %w", override, err))
		} else {
			return override, nil
		}
	}
	for _, source := range []func() (float64, error){
		FromEnv,
		func() (float64, error) { return FromResources(xu) },
		FromXfconf,
	} {
		s, err := source()
		if err == nil {
			return s, errors.Join(errs...)
		}
		if !errors.Is(err, ErrUnset) {
			errs = append(errs, err)
		}
	}
	// Nothing configured a scale, which is not a fault: a desktop nobody
	// told to scale is unscaled, and 1 is simply the right answer. Saying
	// so on every start would be noise that teaches the reader to skip
	// this line -- including the day it carries a real error.
	return 1, errors.Join(errs...)
}

// check accepts only a scale that can be drawn at. strconv.ParseFloat
// happily returns NaN and Inf, and NaN slips past a plain "<= 0" test
// because every comparison with it is false.
func check(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return errors.New("not a positive finite number")
	}
	return nil
}

// FromEnv reads GDK_SCALE, which the user may have set precisely
// because automatic detection got it wrong somewhere else. Set but blank
// counts as unset: that is how a shell clears a variable for one command.
func FromEnv() (float64, error) {
	raw, ok := os.LookupEnv("GDK_SCALE")
	v := strings.TrimSpace(raw)
	if !ok || v == "" {
		return 0, fmt.Errorf("GDK_SCALE: %w", ErrUnset)
	}
	s, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("GDK_SCALE=%q: %w", raw, err)
	}
	if err := check(s); err != nil {
		return 0, fmt.Errorf("GDK_SCALE=%q: %w", raw, err)
	}
	return s, nil
}

// FromResources reads Xft.dpi out of the X resource database, which
// is where a desktop that scales by changing the DPI records it.
func FromResources(xu *xgbutil.XUtil) (float64, error) {
	if xu == nil {
		return 0, fmt.Errorf("resource Xft.dpi: no X connection: %w", ErrUnset)
	}
	r, err := xwin.NewProps(xu.Conn()).Get(xu.RootWin(), "RESOURCE_MANAGER")
	if errors.Is(err, xwin.ErrPropUnset) {
		return 0, fmt.Errorf("resource Xft.dpi: no resource database: %w", ErrUnset)
	}
	if err != nil {
		return 0, fmt.Errorf("resource Xft.dpi: %w", err)
	}
	res, err := xwin.DecodeString(r)
	if err != nil {
		return 0, fmt.Errorf("resource Xft.dpi: RESOURCE_MANAGER: %w", err)
	}
	return fromResourceString(res)
}

// fromResourceString finds Xft.dpi in the text of a resource database.
// Split from the X read above so it can be tested without a display.
func fromResourceString(res string) (float64, error) {
	for _, line := range strings.Split(res, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != "Xft.dpi" {
			continue
		}
		value = strings.TrimSpace(value)
		dpi, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("resource Xft.dpi=%q: %w", value, err)
		}
		if err := check(dpi); err != nil {
			return 0, fmt.Errorf("resource Xft.dpi=%q: %w", value, err)
		}
		return dpi / DPI, nil
	}
	return 0, fmt.Errorf("resource Xft.dpi: %w", ErrUnset)
}

// FromXfconf reads XFCE's own setting straight out of its config
// file. Going behind xfconf's back is not ideal, but the alternative is a
// D-Bus round trip or a subprocess on every launch, and the file format
// has been stable for the life of XFCE 4.
//
// A missing home directory is not "unset": it is a broken environment,
// and saying so beats quietly drawing at the wrong size.
func FromXfconf() (float64, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, fmt.Errorf("xfconf: %w", err)
	}
	return FromXfconfFile(filepath.Join(home, ".config", "xfce4", "xfconf",
		"xfce-perchannel-xml", "xsettings.xml"))
}

// FromXfconfFile parses one xfconf channel file. Split from the
// lookup above so it can be tested against a fixture.
//
// A file that does not exist, or that does not mention
// WindowScalingFactor, is ErrUnset: XFCE writes the file lazily, the
// first time a setting is changed. A file that exists but cannot be read
// or parsed -- including an empty one, which xfconf never writes -- is an
// error.
func FromXfconfFile(path string) (float64, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a fixed path under the user's own config
	if errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("xfconf: %w (%w)", ErrUnset, err)
	}
	if err != nil {
		return 0, fmt.Errorf("xfconf: %w", err)
	}
	s, err := parseXfconf(data)
	if err != nil {
		return 0, fmt.Errorf("xfconf: %s: %w", path, err)
	}
	return s, nil
}

// parseXfconf finds WindowScalingFactor in an xfconf channel document.
func parseXfconf(data []byte) (float64, error) {
	var channel struct {
		Properties []struct {
			Name       string `xml:"name,attr"`
			Properties []struct {
				Name  string `xml:"name,attr"`
				Type  string `xml:"type,attr"`
				Value string `xml:"value,attr"`
			} `xml:"property"`
		} `xml:"property"`
	}
	if err := xml.Unmarshal(data, &channel); err != nil {
		return 0, fmt.Errorf("parsing: %w", err)
	}
	for _, group := range channel.Properties {
		for _, prop := range group.Properties {
			if prop.Name != "WindowScalingFactor" {
				continue
			}
			// xfconf declares the setting an int and XFCE only offers whole
			// factors, so anything else was not written by XFCE and is not
			// guessed at.
			if prop.Type != "int" {
				return 0, fmt.Errorf("WindowScalingFactor has type %q, want int", prop.Type)
			}
			n, err := strconv.Atoi(strings.TrimSpace(prop.Value))
			if err != nil {
				return 0, fmt.Errorf("WindowScalingFactor: %w", err)
			}
			if n <= 0 {
				return 0, fmt.Errorf("WindowScalingFactor=%d: not a positive number", n)
			}
			return float64(n), nil
		}
	}
	return 0, fmt.Errorf("no WindowScalingFactor: %w", ErrUnset)
}
