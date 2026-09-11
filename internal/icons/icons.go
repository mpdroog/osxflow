// Package icons serves the application artwork the dock draws, from PNGs
// compiled into the binary.
//
// Nothing here reads the icon theme. The theme is rasterised at build time
// by tools/mkicons and the results are embedded, for two reasons. The
// first is cgo: the active theme is 13,099 SVGs and no PNGs, and rendering
// those correctly needs a real SVG implementation -- the pure-Go ones drop
// the embedded raster layers these particular icons are built from, and
// turn Firefox into a blank square. The second is durability: a dock that
// looks up icons at runtime breaks when a theme is upgraded, renamed or
// removed, and one that carries its pixels cannot.
//
// The cost is 2.2 MB of binary and a rebuild to pick up a newly installed
// application's icon. Both are the right way round for a program that
// starts once per session and runs until logout.
package icons

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"path"
	"sort"
	"strings"
	"unicode"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"

	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/text"
)

// data holds one PNG per icon, named for the desktop file id without its
// extension ("firefox.png"), plus the sys-* icons the dock needs that no
// application provides.
//
//go:embed data/*.png
var data embed.FS

// MasterSize is the size tools/mkicons rasterises at. Everything drawn is
// scaled down from this, never up, so a magnified icon stays sharp.
const MasterSize = 160

// Keys for the artwork that belongs to the dock rather than to an app.
const (
	Trash     = "sys-trash"
	TrashFull = "sys-trash-full"
	Downloads = "sys-downloads"
)

// Set decodes and scales icons on demand, keeping what it has produced.
//
// A dock draws the same handful of icons at the same handful of sizes for
// hours, so caching is what keeps magnification cheap: only the icons near
// the cursor are ever scaled at an unusual size, and each size is produced
// once.
type Set struct {
	// src is where the PNGs come from: the embedded set, or a stand-in in
	// tests that need an icon the build would never produce.
	src fs.FS

	masters map[string]*image.RGBA
	scaled  map[key]*image.RGBA
	missing map[string]bool

	// bytes is how much pixel data scaled holds, which is what the cache is
	// bounded by: an icon at 149 pixels costs nine times one at 50, so a
	// count of entries says very little about the memory in use.
	bytes int
}

type key struct {
	name string
	size int
}

// New returns an empty Set. Decoding happens on first use rather than up
// front: the binary carries 109 icons and a session shows perhaps ten of
// them, so decoding them all would cost startup time for nothing.
func New() *Set {
	return &Set{
		src:     data,
		masters: make(map[string]*image.RGBA),
		scaled:  make(map[key]*image.RGBA),
		missing: make(map[string]bool),
	}
}

// Has reports whether an icon was compiled in.
//
// It answers a yes-or-no question for callers that pass it around as one
// (cmd/notifyd hands it to its icon index), so the only failure it can
// meet other than "not there" is logged rather than returned. For an
// embedded filesystem that failure should not exist at all; if it ever
// does, it must not pass for a missing icon.
func Has(name string) bool {
	ok, err := has(data, name)
	if err != nil {
		log.Printf("icons: %v", err)
	}
	return ok
}

// has is Has with the error: absence is (false, nil), anything else is an
// error.
func has(fsys fs.FS, name string) (bool, error) {
	_, err := fs.Stat(fsys, iconPath(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("looking up icon %s: %w", name, err)
	}
	return true, nil
}

func iconPath(name string) string { return path.Join("data", name+".png") }

// Names lists every embedded icon, sorted. Used by tests to check the
// build produced what the dock expects.
func Names() ([]string, error) {
	entries, err := data.ReadDir("data")
	if err != nil {
		return nil, fmt.Errorf("listing embedded icons: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".png"))
	}
	sort.Strings(out)
	return out, nil
}

// Master returns the full-size artwork for an icon, or nil when the binary
// does not carry one.
//
// An icon that is not there is the ordinary case -- an application
// installed since the last build -- and the caller draws a placeholder
// without comment. One that is there but cannot be read or decoded is a
// build fault that would otherwise look exactly the same, so it is logged;
// the negative cache makes that once per name rather than once per frame.
func (s *Set) Master(name string) *image.RGBA {
	if img, ok := s.masters[name]; ok {
		return img
	}
	if s.missing[name] {
		return nil
	}
	raw, err := fs.ReadFile(s.src, iconPath(name))
	if err != nil {
		s.missing[name] = true
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("icons: reading %s: %v", name, err)
		}
		return nil
	}
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		// A corrupt embedded PNG is a build fault, not a runtime one, but
		// the dock still has to draw something rather than stop.
		s.missing[name] = true
		log.Printf("icons: decoding %s: %v (run `make icons`)", name, err)
		return nil
	}
	img := toRGBA(decoded)
	s.masters[name] = img
	return img
}

// At returns an icon scaled to size pixels square, or nil when there is no
// such icon. The result is cached and must not be modified.
//
// CatmullRom is used because each (icon, size) pair is produced once and
// then reused for as long as the dock runs: the cost is paid a few dozen
// times per session and buys visibly cleaner downscaling of detailed
// artwork.
//
// Use this for the sizes an icon rests at. For the sizes a magnification
// sweep passes through, use Magnified.
func (s *Set) At(name string, size int) *image.RGBA {
	return s.scale(name, size, xdraw.CatmullRom)
}

// Magnified returns an icon at one of the sizes magnification passes
// through, cached like At but produced with a cheaper filter.
//
// Both halves of that matter. Scaling is by a long way the most expensive
// thing a dock frame does -- five icons are within the cursor's influence
// at any moment, and at 0.4 ms each that was half the frame budget spent
// redrawing artwork that had been drawn at that exact size a moment
// earlier -- so the results are kept. Keeping them is only affordable
// because the caller rounds every magnified size onto a ladder (see
// Metrics.Quantise), which turns a continuous range into a few dozen sizes
// per icon; caching unrounded sizes really would grow without bound, which
// is why an earlier version of this did not cache at all.
//
// ApproxBiLinear rather than CatmullRom because these sizes are reached
// during an animation, where a first frame that stutters for a millisecond
// is far more visible than a filter difference on an icon that is moving
// and changing size.
func (s *Set) Magnified(name string, size int) *image.RGBA {
	return s.scale(name, size, xdraw.ApproxBiLinear)
}

func (s *Set) scale(name string, size int, quality xdraw.Scaler) *image.RGBA {
	if size <= 0 {
		return nil
	}
	k := key{name: name, size: size}
	if img, ok := s.scaled[k]; ok {
		return img
	}
	master := s.Master(name)
	if master == nil {
		return nil
	}
	s.evictIfLarge(size)
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	quality.Scale(dst, dst.Bounds(), master, master.Bounds(), xdraw.Src, nil)
	s.scaled[k] = dst
	s.bytes += len(dst.Pix)
	return dst
}

// maxScaledBytes bounds the scaled cache.
//
// The steady state is every icon in the dock at every size on the
// magnification ladder, which on a 2x display is about 15 MB for a dock of
// nine. The limit is above that on purpose: it is a stop against a
// pathological case (a very wide dock, a very large display) rather than
// something the normal run is expected to reach.
const maxScaledBytes = 24 << 20

func (s *Set) evictIfLarge(next int) {
	if s.bytes+next*next*4 <= maxScaledBytes {
		return
	}
	// Dropping everything is cruder than a least-recently-used eviction
	// and entirely sufficient: it happens almost never, and the sizes in
	// use are regenerated within one frame of the cursor moving.
	s.scaled = make(map[key]*image.RGBA)
	s.bytes = 0
}

// Fallback draws a placeholder for an application with no embedded icon --
// something installed since the last build, most likely. It is a tinted
// rounded tile with the app's first letter, which reads as "this is an
// app we do not have artwork for" rather than as a broken image.
func Fallback(label string, size int, face font.Face) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	tint := tintFor(label)
	// The radius matches the squircle proportion the WhiteSur icons use,
	// so a fallback tile sits in the row without drawing attention.
	paint.RoundRect(img, img.Bounds(), float64(size)*0.23, tint)

	if face == nil {
		return img
	}
	initial := initialOf(label)
	if initial == "" {
		return img
	}
	// Optically centred: font metrics put the baseline where a descender
	// would go, so centring on the ascent looks low.
	metrics := face.Metrics()
	baseline := size/2 + metrics.Ascent.Round()/2 - metrics.Descent.Round()/3
	// NRGBA, not RGBA: color.RGBA is premultiplied, so white at alpha 0xf0
	// written as RGBA{255, 255, 255, 0xf0} is not a colour at all, and the
	// blend overflows into a dark letter.
	text.DrawCentred(img, face, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xf0},
		size/2, baseline, initial, size)
	return img
}

func initialOf(label string) string {
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}

// tintFor picks a stable colour from the name, so an app keeps the same
// placeholder between runs instead of changing shade on every launch.
func tintFor(label string) color.RGBA {
	var h uint32 = 2166136261 // FNV-1a
	for _, b := range []byte(strings.ToLower(label)) {
		h ^= uint32(b)
		h *= 16777619
	}
	// A fixed saturation and lightness keeps every placeholder in the same
	// family; only the hue varies.
	return hsl(float64(h%360), 0.42, 0.52)
}

func hsl(hueDeg, sat, light float64) color.RGBA {
	c := (1 - absf(2*light-1)) * sat
	x := c * (1 - absf(mod(hueDeg/60, 2)-1))
	m := light - c/2
	var r, g, b float64
	switch {
	case hueDeg < 60:
		r, g, b = c, x, 0
	case hueDeg < 120:
		r, g, b = x, c, 0
	case hueDeg < 180:
		r, g, b = 0, c, x
	case hueDeg < 240:
		r, g, b = 0, x, c
	case hueDeg < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.RGBA{
		R: uint8((r + m) * 255),
		G: uint8((g + m) * 255),
		B: uint8((b + m) * 255),
		A: 0xff,
	}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func mod(a, b float64) float64 {
	for a >= b {
		a -= b
	}
	return a
}

// toRGBA returns img as *image.RGBA, without copying when it already is
// one.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.Copy(out, image.Point{}, img, b, xdraw.Src, nil)
	return out
}

// Verify reports icons the dock asked for that the build did not produce.
// cmd/dock calls it at startup so a missing icon is a line on stderr
// rather than a silent blank square.
func Verify(names []string) error {
	var (
		missing  []string
		problems []error
	)
	for _, n := range names {
		ok, err := has(data, n)
		switch {
		case err != nil:
			problems = append(problems, err)
		case !ok:
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		problems = append(problems,
			fmt.Errorf("no embedded icon for %s (run `make icons`)", strings.Join(missing, ", ")))
	}
	return errors.Join(problems...)
}
