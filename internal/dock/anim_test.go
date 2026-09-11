package dock

import (
	"math"
	"testing"
)

func TestAnimApproachesTarget(t *testing.T) {
	a := Anim{Value: 0, Target: 1, Tau: 0.05}
	for i := 0; i < 1000 && !a.Done(); i++ {
		a.Step(0.008)
	}
	if !a.Done() {
		t.Fatalf("animation never settled; value %g", a.Value)
	}
	if math.Abs(a.Value-1) > epsilon {
		t.Errorf("settled at %g, want 1", a.Value)
	}
}

// TestAnimIsFramerateIndependent is the property the exponential step was
// chosen for: the dock must take the same wall-clock time to appear on a
// busy machine as on an idle one, and a busy machine is exactly when it
// gets asked.
func TestAnimIsFramerateIndependent(t *testing.T) {
	run := func(dt float64) float64 {
		a := Anim{Value: 0, Target: 1, Tau: 0.05}
		for elapsed := 0.0; elapsed < 0.1-1e-12; elapsed += dt {
			a.Step(dt)
		}
		return a.Value
	}
	fast, slow := run(0.002), run(0.02)
	if math.Abs(fast-slow) > 0.02 {
		t.Errorf("after 100ms: %g at 500Hz but %g at 50Hz; the speed depends on the framerate",
			fast, slow)
	}
}

func TestAnimStepReportsChange(t *testing.T) {
	a := Anim{Value: 0, Target: 0, Tau: 0.05}
	if a.Step(0.008) {
		t.Error("Step reported a change with nothing to do")
	}
	if a.Step(0) {
		t.Error("Step reported a change for a zero time delta")
	}
	a.Target = 1
	if !a.Step(0.008) {
		t.Error("Step reported no change while moving toward a new target")
	}
}

func TestAnimZeroTauSnaps(t *testing.T) {
	a := Anim{Value: 0, Target: 1, Tau: 0}
	if !a.Step(0.008) || a.Value != 1 {
		t.Errorf("with no time constant the value should snap; got %g", a.Value)
	}
}

func TestRevealStartsHidden(t *testing.T) {
	r := NewReveal()
	if !r.Hidden() {
		t.Error("a new dock should be hidden")
	}
	if r.Amount() != 0 {
		t.Errorf("Amount = %g, want 0", r.Amount())
	}
}

func TestRevealShowsAndHides(t *testing.T) {
	r := NewReveal()
	r.Show()
	for i := 0; i < 1000 && !r.Settled(); i++ {
		r.Step(0.008)
	}
	if !r.Settled() || math.Abs(r.Amount()-1) > epsilon {
		t.Fatalf("after showing, Amount = %g", r.Amount())
	}
	if r.Hidden() {
		t.Error("Hidden reported true for a fully shown dock")
	}

	r.Hide()
	for i := 0; i < 1000 && !r.Settled(); i++ {
		r.Step(0.008)
	}
	if !r.Hidden() {
		t.Errorf("after hiding, Amount = %g and Hidden is false", r.Amount())
	}
}

// TestRevealHidesMoreSlowlyThanItShows encodes a deliberate asymmetry:
// appearing is a response to something the user just did, disappearing
// should not snatch the dock away from someone still deciding.
func TestRevealHidesMoreSlowlyThanItShows(t *testing.T) {
	steps := func(setup func(*Reveal), done func(*Reveal) bool) int {
		r := NewReveal()
		if done(r) {
			r.Show()
			for i := 0; i < 10000 && !r.Settled(); i++ {
				r.Step(0.001)
			}
		}
		setup(r)
		n := 0
		for ; n < 10000 && !r.Settled(); n++ {
			r.Step(0.001)
		}
		return n
	}
	show := steps(func(r *Reveal) { r.Show() }, func(*Reveal) bool { return false })
	hide := steps(func(r *Reveal) { r.Hide() }, func(*Reveal) bool { return true })
	if hide <= show {
		t.Errorf("hide took %d steps and show took %d; hiding should be the gentler one", hide, show)
	}
}

func TestRevealOffset(t *testing.T) {
	r := NewReveal()
	const height float64 = 200
	if got := r.Offset(height); math.Abs(got-height) > 1e-9 {
		t.Errorf("hidden offset = %g, want the full %g so the panel is off screen", got, height)
	}
	r.Show()
	for i := 0; i < 1000 && !r.Settled(); i++ {
		r.Step(0.008)
	}
	// Settled means "within epsilon of the target", not "exactly on it", so
	// the resting offset is a fraction of a pixel rather than zero. What
	// matters is that it rounds to the same pixel.
	if got := r.Offset(height); math.Abs(got) > 0.5 {
		t.Errorf("shown offset = %g, want under half a pixel", got)
	}
}

// The hidden end is where the easing is steepest, so a reveal that settles
// merely near zero leaves a band of the window on screen -- over the
// trigger, where it swallows the pointer the trigger should have seen.
func TestRevealHidesFullyOffScreen(t *testing.T) {
	r := NewReveal()
	const height float64 = 293
	r.Show()
	for i := 0; i < 1000 && !r.Settled(); i++ {
		r.Step(0.008)
	}
	r.Hide()
	for i := 0; i < 1000 && !r.Settled(); i++ {
		r.Step(0.008)
	}
	if !r.Hidden() {
		t.Fatal("reveal never settled hidden")
	}
	if got := r.Offset(height); got != height {
		t.Errorf("hidden offset = %g, want exactly %g; %g px stays on screen",
			got, height, height-got)
	}
}

func TestEaseOutCubicClamps(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-1, 0}, {0, 0}, {1, 1}, {2, 1},
	} {
		if got := easeOutCubic(tc.in); got != tc.want {
			t.Errorf("easeOutCubic(%g) = %g, want %g", tc.in, got, tc.want)
		}
	}
	// Eased means it decelerates: the first half covers more than half.
	if easeOutCubic(0.5) <= 0.5 {
		t.Error("easeOutCubic(0.5) should be past halfway; the curve is meant to decelerate")
	}
}
