// Package search ranks applications against what the user has typed.
//
// The ranking is ordinary and deliberately so. A launcher is judged on
// whether the app you wanted is first after two or three keystrokes, and
// the way to lose that is to be clever: a scorer that rewards scattered
// subsequence matches will put "LibreOffice Calc" above "Files" for the
// query "fi", because it can find f-i somewhere in it. So the tiers below
// are strictly ordered -- an exact match always beats a prefix, a prefix
// always beats a word prefix, and a subsequence match is the last resort
// it deserves to be.
package search

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/mpdroog/osxflow/internal/desktop"
)

// Tier is the kind of match that was found. Higher is better, and a
// higher tier always outranks a lower one regardless of the score within
// it.
type Tier int

const (
	NoMatch Tier = iota
	Subsequence
	Substring
	ExecName
	WordPrefix
	Prefix
	Exact
)

func (t Tier) String() string {
	switch t {
	case Exact:
		return "exact"
	case Prefix:
		return "prefix"
	case WordPrefix:
		return "word prefix"
	case ExecName:
		return "executable"
	case Substring:
		return "substring"
	case Subsequence:
		return "subsequence"
	case NoMatch:
		return "no match"
	}
	return "unknown"
}

// Result is one ranked application. App points into the caller's slice,
// which is not copied.
type Result struct {
	App  *desktop.App
	Tier Tier

	// Usage is the application's frecency score, 0 when it has never been
	// chosen or when no Ranker was supplied.
	Usage float64

	// Pinned marks the application explicitly taught for this exact query.
	Pinned bool

	// penalty breaks ties within a tier: lower sorts first. It is where
	// "shorter name" and "matched earlier in the string" live.
	penalty int
}

// Ranker supplies what the launcher has learned about which applications
// get used. A nil Ranker means no memory, which is what the first run and
// every test that does not care about usage look like.
type Ranker interface {
	// Score is an application's usage score, higher meaning used more and
	// more recently.
	Score(appID string) float64

	// Pinned is the application id explicitly chosen for this exact query
	// before, or "" if there is none.
	Pinned(query string) string
}

// Filter ranks apps against query, best first, dropping non-matches.
//
// An empty query returns everything most-used first, then in the order it
// was given -- which is alphabetical from desktop.Scan. Showing the
// applications you actually open before the alphabet is the whole point of
// the ranker.
//
// ranker may be nil.
func Filter(apps []desktop.App, query string, ranker Ranker) []Result {
	q := strings.ToLower(strings.TrimSpace(query))
	pinned := ""
	if ranker != nil {
		pinned = ranker.Pinned(query)
	}

	out := make([]Result, 0, len(apps))
	for i := range apps {
		r := Result{App: &apps[i], Tier: NoMatch}
		if q != "" {
			var ok bool
			if r, ok = score(&apps[i], q); !ok {
				continue
			}
		}
		if ranker != nil {
			r.Usage = ranker.Score(apps[i].ID)
			r.Pinned = pinned != "" && apps[i].ID == pinned
		}
		out = append(out, r)
	}

	// SliceStable so that results equal on every key keep the alphabetical
	// order they arrived in, which makes the list predictable between
	// keystrokes.
	sort.SliceStable(out, func(a, b int) bool { return less(&out[a], &out[b]) })
	return out
}

// less orders two results.
//
// The ordering is layered, and the layers are not interchangeable:
//
//  1. A pinned application first. This is the only thing allowed to beat
//     a better match, and it is safe because the user taught it by
//     choosing that application for that exact query.
//  2. Match tier. Frecency deliberately cannot cross a tier: an
//     application you use hourly should not displace an exact-name match
//     for something else, or typing a full name would stop working.
//  3. Usage within the tier. This is where frecency earns its place --
//     "fi" matches fish, Fingerprints and Firefox all as prefixes, and
//     which of them you get should be the one you keep choosing rather
//     than the shortest name.
//  4. The static penalty, for applications with no usage to separate them.
func less(a, b *Result) bool {
	if a.Pinned != b.Pinned {
		return a.Pinned
	}
	if a.Tier != b.Tier {
		return a.Tier > b.Tier
	}
	if a.Usage != b.Usage {
		return a.Usage > b.Usage
	}
	return a.penalty < b.penalty
}

func score(app *desktop.App, q string) (Result, bool) {
	name := strings.ToLower(app.Name)
	res := Result{App: app}

	switch {
	case name == q:
		res.Tier, res.penalty = Exact, 0

	case strings.HasPrefix(name, q):
		// Shorter names first: for "fi", "Files" should beat "File
		// Manager Settings".
		res.Tier, res.penalty = Prefix, len(name)

	case wordPrefix(name, q):
		res.Tier, res.penalty = WordPrefix, len(name)

	case execMatches(app, q):
		res.Tier, res.penalty = ExecName, len(name)

	case strings.Contains(name, q):
		// Matching earlier in the name is better, and is worth more than
		// being slightly shorter.
		res.Tier, res.penalty = Substring, strings.Index(name, q)*4+len(name)

	default:
		gaps, ok := subsequence(name, q)
		if !ok {
			return Result{}, false
		}
		res.Tier, res.penalty = Subsequence, gaps*4+len(name)
	}
	return res, true
}

// wordPrefix reports whether any word of name starts with q, so that
// "calc" finds "GNOME Calculator" and "web" finds "Firefox Web Browser".
func wordPrefix(name, q string) bool {
	for _, word := range splitWords(name) {
		if strings.HasPrefix(word, q) {
			return true
		}
	}
	return false
}

// splitWords breaks a name on anything that is not a letter or digit.
// Names are full of separators -- "Files", "Text Editor", "qBittorrent",
// "LibreOffice Calc" -- and splitting only on spaces misses half of them.
func splitWords(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// execMatches lets the binary's name find the app, so that "thunar" finds
// "File Manager" and "ghostty" finds a terminal named something else. It
// sits below the name tiers because the user usually means the name they
// can see.
func execMatches(app *desktop.App, q string) bool {
	names := make([]string, 0, 2)
	if app.Binary != "" {
		names = append(names, filepath.Base(app.Binary))
	}
	if len(app.Argv) > 0 {
		names = append(names, filepath.Base(app.Argv[0]))
	}
	for _, n := range names {
		if strings.HasPrefix(strings.ToLower(n), q) {
			return true
		}
	}
	return false
}

// subsequence reports whether every rune of q appears in name in order,
// and how many runes were skipped between the matched ones.
//
// The gap count is what stops this tier from being noise: "ff" matching
// "Firefox" (one gap) is useful, "ff" matching "Fallback Font Fixer"
// across twelve characters is not, and the penalty pushes it below
// anything that matched more directly.
func subsequence(name, q string) (gaps int, ok bool) {
	qr := []rune(q)
	if len(qr) == 0 {
		return 0, false
	}
	i := 0
	last := -1
	for pos, r := range []rune(name) {
		if r != qr[i] {
			continue
		}
		if last >= 0 {
			gaps += pos - last - 1
		}
		last = pos
		i++
		if i == len(qr) {
			return gaps, true
		}
	}
	return 0, false
}
