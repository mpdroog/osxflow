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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xprop"
)

// DPI is the X11 default. Display scaling is applied to point sizes
// instead, so that one number controls everything.
const DPI = 96

// Detect returns the display scale factor, or 1 when nothing says
// otherwise. A non-zero override wins outright, which is what the -scale
// flag is for.
func Detect(xu *xgbutil.XUtil, override float64) float64 {
	if override > 0 {
		return override
	}
	if s, ok := FromEnv(); ok {
		return s
	}
	if s, ok := FromResources(xu); ok {
		return s
	}
	if s, ok := FromXfconf(); ok {
		return s
	}
	return 1
}

// FromEnv reads GDK_SCALE, which the user may have set precisely
// because automatic detection got it wrong somewhere else.
func FromEnv() (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("GDK_SCALE")), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// FromResources reads Xft.dpi out of the X resource database, which
// is where a desktop that scales by changing the DPI records it.
func FromResources(xu *xgbutil.XUtil) (float64, bool) {
	if xu == nil {
		return 0, false
	}
	res, err := xprop.PropValStr(xprop.GetProperty(xu, xu.RootWin(), "RESOURCE_MANAGER"))
	if err != nil || res == "" {
		return 0, false
	}
	for _, line := range strings.Split(res, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != "Xft.dpi" {
			continue
		}
		dpi, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || dpi <= 0 {
			return 0, false
		}
		return dpi / DPI, true
	}
	return 0, false
}

// FromXfconf reads XFCE's own setting straight out of its config
// file. Going behind xfconf's back is not ideal, but the alternative is a
// D-Bus round trip or a subprocess on every launch, and the file format
// has been stable for the life of XFCE 4.
func FromXfconf() (float64, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, false
	}
	return FromXfconfFile(filepath.Join(home, ".config", "xfce4", "xfconf",
		"xfce-perchannel-xml", "xsettings.xml"))
}

// FromXfconfFile parses one xfconf channel file. Split from the
// lookup above so it can be tested against a fixture.
func FromXfconfFile(path string) (float64, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // a fixed path under the user's own config
	if err != nil {
		return 0, false
	}

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
		return 0, false
	}
	for _, group := range channel.Properties {
		for _, prop := range group.Properties {
			if prop.Name != "WindowScalingFactor" || prop.Type != "int" {
				continue
			}
			v, err := strconv.ParseFloat(prop.Value, 64)
			if err != nil || v <= 0 {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}
