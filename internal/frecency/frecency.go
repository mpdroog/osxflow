// Package frecency remembers which applications get chosen, so that the
// ones you actually use rise to the top.
//
// The score is a single exponentially-decayed counter per application
// rather than a count plus a timestamp. That is what makes "frequency" and
// "recency" one number instead of two competing ones: each use adds 1 to a
// value that is continuously halving, so something used constantly last
// year loses to something used twice this week without any special casing.
//
//	on use:   score = score*decay(now-last) + 1
//	on read:  score = score*decay(now-last)
//
// Nothing here touches the filesystem or the clock; the caller passes the
// time in. That keeps the whole of the scoring testable, which matters
// because decay bugs are invisible until a month has gone by.
package frecency

import (
	"math"
	"strings"
	"time"
)

// HalfLife is how long a single use takes to count for half as much.
//
// Four weeks is chosen so that a tool used daily stays comfortably on top,
// a tool used for one project keeps its place for about a month after that
// project ends, and something opened once by accident is gone in a
// fortnight.
const HalfLife = 28 * 24 * time.Hour

// pruneBelow is the score under which an entry is not worth keeping. With
// the half-life above, 0.02 is reached roughly six months after a single
// use.
const pruneBelow = 0.02

// maxEntries bounds the file. It is far above the number of applications
// on a normal system, so it only ever triggers on entries that are already
// decayed to nothing.
const maxEntries = 1000

// Entry is what is remembered about one application.
type Entry struct {
	// Last is when Score was last brought up to date, not when the
	// application was last used -- they are the same thing only because
	// Score is only ever updated on use.
	Last time.Time `json:"last"`

	Score float64 `json:"score"`
}

// Store is the remembered usage. The zero value is not usable; use New or
// Load.
type Store struct {
	// Apps is keyed by desktop file id, which is stable across upgrades in
	// a way that a name or an executable path is not.
	Apps map[string]Entry `json:"apps"`

	// Queries maps an exact query string to the desktop id chosen for it.
	//
	// This is the deliberate half of the memory, and the only thing
	// allowed to override the match tiers: typing "ff" and picking Firefox
	// teaches that pairing outright, where the scoring alone could not,
	// because "ff" matches Firefox only as a scattered subsequence and
	// LibreOffice as a substring.
	Queries map[string]string `json:"queries"`

	path string
}

// New returns an empty store that will save to path.
func New(path string) *Store {
	return &Store{
		Apps:    map[string]Entry{},
		Queries: map[string]string{},
		path:    path,
	}
}

// Record notes that appID was chosen for query.
func (s *Store) Record(query, appID string, now time.Time) {
	if appID == "" {
		return
	}
	e := s.Apps[appID]
	s.Apps[appID] = Entry{Score: e.decayed(now) + 1, Last: now}

	// Only non-empty queries are remembered. Picking something from the
	// unfiltered list says which app you wanted, not which letters mean
	// it, and recording "" would pin the first thing you ever launched to
	// the top of the empty query forever.
	if q := normalise(query); q != "" {
		s.Queries[q] = appID
	}
}

// Score is appID's decayed usage score at now.
func (s *Store) Score(appID string, now time.Time) float64 {
	return s.Apps[appID].decayed(now)
}

// Pinned is the application explicitly chosen for this exact query before,
// or "" if there is none.
//
// Matching is exact rather than by prefix on purpose. Prefix matching
// would mean teaching "ff" also rearranges "f", which is the kind of
// action-at-a-distance that makes a launcher feel unpredictable.
func (s *Store) Pinned(query string) string {
	return s.Queries[normalise(query)]
}

// Forget removes an application, for when an entry is stale or wrong.
func (s *Store) Forget(appID string) {
	delete(s.Apps, appID)
	for q, id := range s.Queries {
		if id == appID {
			delete(s.Queries, q)
		}
	}
}

// decayed applies the half-life to an entry's score.
func (e Entry) decayed(now time.Time) float64 {
	if e.Score == 0 || e.Last.IsZero() {
		return 0
	}
	age := now.Sub(e.Last)
	if age <= 0 {
		// A clock that went backwards, or two uses in the same instant.
		// Neither is worth modelling; the score simply does not decay.
		return e.Score
	}
	return e.Score * math.Pow(0.5, age.Hours()/HalfLife.Hours())
}

// prune drops entries that have decayed to nothing, and any query pointing
// at an application that is no longer remembered.
//
// It runs before saving rather than on a timer, so the file cannot grow
// without bound between launches, and so a store held in memory is never
// mutated behind the caller's back.
func (s *Store) prune(now time.Time) {
	for id, e := range s.Apps {
		if e.decayed(now) < pruneBelow {
			delete(s.Apps, id)
		}
	}
	if len(s.Apps) > maxEntries {
		s.dropLowest(now, len(s.Apps)-maxEntries)
	}
	for q, id := range s.Queries {
		if _, ok := s.Apps[id]; !ok {
			delete(s.Queries, q)
		}
	}
}

// dropLowest removes the n weakest entries.
func (s *Store) dropLowest(now time.Time, n int) {
	for range n {
		var worstID string
		worst := math.Inf(1)
		for id, e := range s.Apps {
			if v := e.decayed(now); v < worst {
				worstID, worst = id, v
			}
		}
		if worstID == "" {
			return
		}
		delete(s.Apps, worstID)
	}
}

// normalise makes query matching insensitive to case and surrounding
// space, so that "FF " and "ff" are the same lesson.
func normalise(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}
