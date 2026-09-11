package dock

import "math"

// Anim eases a value toward a target, framerate-independently.
//
// The step is exponential smoothing rather than a fixed increment per
// frame: it reaches the target in the same wall-clock time whether frames
// arrive every 8 ms or every 40, which matters because X delivers motion
// events at whatever rate the pointer and the server feel like. A linear
// animation driven by frame count runs at a different speed on a busy
// machine, and that is exactly when the dock is being asked to appear.
type Anim struct {
	// Value is the current position, Target where it is heading.
	Value, Target float64

	// Tau is the time constant in seconds: after Tau the value has closed
	// about 63% of the remaining distance. Smaller is snappier.
	Tau float64
}

// Step advances the animation by dt seconds and reports whether the value
// changed enough to be worth redrawing.
func (a *Anim) Step(dt float64) bool {
	if dt <= 0 {
		return false
	}
	diff := a.Target - a.Value
	if math.Abs(diff) < epsilon {
		if a.Value != a.Target {
			a.Value = a.Target
			return true
		}
		return false
	}
	if a.Tau <= 0 {
		a.Value = a.Target
		return true
	}
	a.Value += diff * (1 - math.Exp(-dt/a.Tau))
	// Land exactly on the target once within epsilon. From here Done says
	// settled and the caller stops stepping, so a value left a hair short
	// stays short for good -- and through the reveal's easing, which is
	// steepest at the hidden end, that hair parked a "hidden" dock forty
	// pixels up the screen, on top of its own reveal trigger.
	if math.Abs(a.Target-a.Value) < epsilon {
		a.Value = a.Target
	}
	return true
}

// Done reports whether the animation has settled, which is what lets the
// dock stop asking for frames and go back to sleep.
func (a *Anim) Done() bool { return math.Abs(a.Target-a.Value) < epsilon }

// epsilon is a twentieth of a pixel: below this nothing on screen can
// change, so continuing to animate would burn frames for no visible
// reason.
const epsilon = 0.05

// Reveal is the dock's slide in and out.
//
// The hide is deliberately slower than the reveal (and starts only after a
// delay, which the caller applies). Appearing should feel immediate
// because the user asked for it by moving the pointer; disappearing should
// not snatch the dock away from someone who is still deciding where to
// click.
type Reveal struct {
	anim Anim
}

// NewReveal returns a hidden dock.
func NewReveal() *Reveal {
	return &Reveal{anim: Anim{Value: 0, Target: 0, Tau: revealTau}}
}

const (
	revealTau = 0.038
	hideTau   = 0.095
)

// Show and Hide set where the dock is heading.
func (r *Reveal) Show() {
	r.anim.Target = 1
	r.anim.Tau = revealTau
}

func (r *Reveal) Hide() {
	r.anim.Target = 0
	r.anim.Tau = hideTau
}

// Step advances the slide and reports whether anything moved.
func (r *Reveal) Step(dt float64) bool { return r.anim.Step(dt) }

// Amount is 0 when fully hidden and 1 when fully shown.
func (r *Reveal) Amount() float64 { return r.anim.Value }

// Settled reports whether the slide has finished.
func (r *Reveal) Settled() bool { return r.anim.Done() }

// Hidden reports whether the dock is entirely off screen, which is when
// the window can be unmapped.
func (r *Reveal) Hidden() bool { return r.anim.Target == 0 && r.anim.Done() }

// Offset converts the reveal amount into how far down the panel is pushed,
// given its height. Fully hidden is one panel height below its resting
// place, which puts it off the bottom of the screen.
//
// The curve is eased rather than linear so the panel decelerates as it
// arrives instead of stopping dead.
func (r *Reveal) Offset(panelHeight float64) float64 {
	return (1 - easeOutCubic(r.anim.Value)) * panelHeight
}

func easeOutCubic(t float64) float64 {
	switch {
	case t <= 0:
		return 0
	case t >= 1:
		return 1
	}
	u := 1 - t
	return 1 - u*u*u
}
