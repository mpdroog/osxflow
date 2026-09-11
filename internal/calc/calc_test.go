package calc

import (
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

func TestEvalArithmetic(t *testing.T) {
	tests := []struct {
		expr string
		want float64
	}{
		{"1+1", 2},
		{"2 + 3 * 4", 14},
		{"(2 + 3) * 4", 20},
		{"10 - 4 - 3", 3},   // left associative
		{"100 / 10 / 2", 5}, // left associative
		{"7 % 3", 1},
		{"-5 + 3", -2},
		{"+5", 5},
		{"--5", 5},
		{"2 ^ 10", 1024},
		{"2 ^ 3 ^ 2", 512}, // right associative: 2^(3^2)
		{"-2 ^ 2", -4},     // unary binds looser than ^
		{"2 ^ -1", 0.5},
		{"1.5 * 2", 3},
		{".5 + .5", 1},
		{"1e3", 1000},
		{"1e-3", 0.001},
		{"1.5e2", 150},
		{"0xff", 255},
		{"0xFF + 1", 256},
		{"0x10 * 2", 32},
		{"   42   ", 42},
		{"((((1))))", 1},
		{"2*(3+(4*(5+6)))", 94},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr)
			if err != nil {
				t.Fatalf("Eval(%q) returned error: %v", tc.expr, err)
			}
			if !closeEnough(got, tc.want) {
				t.Errorf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestEvalFunctions(t *testing.T) {
	tests := []struct {
		expr string
		want float64
	}{
		{"sqrt(16)", 4},
		{"sqrt(2)", math.Sqrt2},
		{"cbrt(-8)", -2}, // cbrt handles what ^(1/3) cannot
		{"abs(-3)", 3},
		{"floor(1.9)", 1},
		{"ceil(1.1)", 2},
		{"round(1.5)", 2},
		{"round(-1.5)", -2},
		{"trunc(-1.9)", -1},
		{"min(3, 7)", 3},
		{"max(3, 7)", 7},
		{"hypot(3, 4)", 5},
		{"pow(2, 8)", 256},
		{"ln(e)", 1},
		{"log(1000)", 3},
		{"log2(8)", 3},
		{"exp(0)", 1},
		{"sin(0)", 0},
		{"cos(0)", 1},
		{"atan2(0, 1)", 0},
		{"pi", math.Pi},
		{"tau", 2 * math.Pi},
		{"phi", math.Phi},
		{"2 * pi", 2 * math.Pi},
		{"SQRT(16)", 4},  // names are case-insensitive
		{"Max(1, 2)", 2}, //
		{"sqrt(sqrt(16))", 2},
		{"min(max(1,2), 3)", 2},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr)
			if err != nil {
				t.Fatalf("Eval(%q) returned error: %v", tc.expr, err)
			}
			if !closeEnough(got, tc.want) {
				t.Errorf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

// TestEvalErrors checks that bad input is rejected. The message itself is
// spot-checked with a substring because these are read by the user.
func TestEvalErrors(t *testing.T) {
	tests := []struct {
		expr    string
		wantMsg string
	}{
		{"1/0", "division by zero"},
		{"1 % 0", "modulo by zero"},
		{"sqrt(-1)", "not defined for negative"},
		{"ln(0)", "only defined for positive"},
		{"ln(-1)", "only defined for positive"},
		{"log(0)", "only defined for positive"},
		{"log2(0)", "only defined for positive"},
		{"asin(2)", "between -1 and 1"},
		{"acos(-2)", "between -1 and 1"},
		{"(-8) ^ (1/3)", "not a real number"},
		{"pow(-8, 0.5)", "not a real number"},
		{"nosuchfn(1)", "unknown function"},
		{"nosuchconst", "unknown name"},
		{"sqrt", "needs arguments"},
		{"min(1)", "takes 2 arguments"},
		{"sqrt(1, 2)", "takes 1 argument"},
		{"1 $ 2", "unexpected character"},
		{"1 2", "unexpected number"},
		{"()", "expected a number"},
		{"*5", "unexpected"},
		{"0x", "not a number"},
		{"1..2", "unexpected number"}, // lexes as "1." then ".2"; two numbers in a row
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			_, err := Eval(tc.expr)
			if err == nil {
				t.Fatalf("Eval(%q) succeeded, want error", tc.expr)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("Eval(%q) error = %q, want it to contain %q", tc.expr, err, tc.wantMsg)
			}
		})
	}
}

// TestEvalIncomplete pins the distinction the UI depends on: a prefix of a
// valid expression must report ErrIncomplete so it can be shown as silence
// rather than as an error.
func TestEvalIncomplete(t *testing.T) {
	incomplete := []string{"", "   ", "1 +", "2 *", "(", "(1", "sqrt(", "sqrt(1", "min(1,", "2 ^", "-"}
	for _, expr := range incomplete {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(expr)
			if err == nil {
				t.Fatalf("Eval(%q) succeeded, want incomplete", expr)
			}
			if !errors.Is(err, ErrIncomplete) {
				t.Errorf("Eval(%q) error = %v, want ErrIncomplete", expr, err)
			}
		})
	}

	// The converse: a genuinely wrong expression must NOT be reported as
	// incomplete, or the UI stays silent about real mistakes.
	wrong := []string{"1 $ 2", "nosuchconst", "1/0", "1 2", "sqrt(-1)"}
	for _, expr := range wrong {
		t.Run("not/"+expr, func(t *testing.T) {
			_, err := Eval(expr)
			if err == nil {
				t.Fatalf("Eval(%q) succeeded, want error", expr)
			}
			if errors.Is(err, ErrIncomplete) {
				t.Errorf("Eval(%q) reported ErrIncomplete, want a hard error: %v", expr, err)
			}
		})
	}
}

func TestEvalDepthLimit(t *testing.T) {
	// Deep nesting must be refused rather than overflowing the stack: the
	// input is a live text field and this is the cheapest way for a user
	// to crash the launcher by leaning on a key.
	deep := strings.Repeat("(", 500) + "1" + strings.Repeat(")", 500)
	if _, err := Eval(deep); err == nil {
		t.Fatal("deeply nested expression succeeded, want depth error")
	} else if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error = %q, want a nesting complaint", err)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{2, "2"},
		{-2, "-2"},
		{0, "0"},
		{0.5, "0.5"},
		{1.0 / 3.0, "0.333333333333"},
		{0.1 + 0.2, "0.3"}, // the whole reason Format exists
		{1e15, "1e+15"},
		{123456789, "123456789"},
		{0.00001, "0.00001"},
		{1e-7, "1e-07"},
		{math.NaN(), "not a number"},
		{math.Inf(1), "infinity"},
		{math.Inf(-1), "-infinity"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := Format(tc.in); got != tc.want {
				t.Errorf("Format(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFormatRoundTrips guards the precision trick in Format: whatever it
// prints must parse back to something indistinguishable from the input at
// display precision.
func TestFormatRoundTrips(t *testing.T) {
	values := []float64{0, 1, -1, 0.5, 1.0 / 3.0, math.Pi, 1e10, 1e-10, -1234.5678, 2.5e18}
	for _, v := range values {
		s := Format(v)
		got, err := Eval(s)
		if err != nil {
			// Negative values re-parse through unary minus, which is fine;
			// anything that does not parse at all is the bug.
			t.Fatalf("Format(%v) = %q, which does not re-parse: %v", v, s, err)
		}
		if math.Abs(got-v) > math.Abs(v)*1e-11+1e-15 {
			t.Errorf("Format(%v) = %q, re-parsed to %v", v, s, got)
		}
	}
}

func TestLooksLikeExpr(t *testing.T) {
	yes := []string{
		"1+1", "2*3", "(1+2)", "-5+2", "1/2", "2^8", "10 % 3",
		"sqrt(2)", "pi*2", "e^2", "MAX(1,2)", ".5+.5", "+1+1",
	}
	for _, s := range yes {
		if !LooksLikeExpr(s) {
			t.Errorf("LooksLikeExpr(%q) = false, want true", s)
		}
	}

	// These must stay app searches. The hyphenated names are the ones that
	// actually bite: they contain an operator but do not start like maths.
	no := []string{
		"", "   ", "firefox", "7zip", "7", "42", "gnome-terminal",
		"libreoffice-calc", "text-editor", "notes", "vs code", "x", "pi",
	}
	for _, s := range no {
		if LooksLikeExpr(s) {
			t.Errorf("LooksLikeExpr(%q) = true, want false", s)
		}
	}
}

// FuzzEval asserts the one property that matters for a parser fed live
// keystrokes: it always terminates and never panics, whatever it is given.
func FuzzEval(f *testing.F) {
	seeds := []string{
		"1+1", "sqrt(2)", "0xff", "((((1))))", "2^3^2", "1e-3", "min(1,2)",
		"", "-", "1/0", "1 $ 2", "1..2", "e", "pi", "0x", "1e", "1e+",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		// Bound the input: the depth limit protects nesting, but a
		// megabyte of digits is a slow test, not a bug worth finding.
		if len(expr) > 256 {
			t.Skip()
		}
		v, err := Eval(expr)
		if err != nil {
			return
		}
		// A successful evaluation must produce something Format can render
		// without panicking, since that is exactly what the UI does next.
		_ = Format(v)
	})
}

// A well-formed number that does not fit is out of range, and the error
// must say so and carry strconv's sentinel rather than claim the text is
// not a number at all.
func TestLexReportsOutOfRange(t *testing.T) {
	for _, expr := range []string{"0x1ffffffffffffffff", "1e999", "2 * 1e999"} {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(expr)
			if err == nil {
				t.Fatalf("Eval(%q) succeeded, want out of range", expr)
			}
			if !errors.Is(err, strconv.ErrRange) {
				t.Errorf("Eval(%q) error = %v, want it to wrap strconv.ErrRange", expr, err)
			}
			if !strings.Contains(err.Error(), "out of range") {
				t.Errorf("Eval(%q) error = %q, want it to say out of range", expr, err)
			}
			if strings.Contains(err.Error(), "strconv.") {
				t.Errorf("Eval(%q) error = %q, want strconv's framing stripped: the user reads this", expr, err)
			}
		})
	}
}

// roundViaText is the definition roundSignificant must agree with: print
// the value at display precision and read it back.
func roundViaText(t *testing.T, v float64) float64 {
	t.Helper()
	s := strconv.FormatFloat(v, 'g', displayPrecision, 64)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("re-parsing %q: %v", s, err)
	}
	return f
}

func checkRoundSignificant(t *testing.T, v float64) {
	t.Helper()
	got, want := roundSignificant(v), roundViaText(t, v)
	if math.Float64bits(got) != math.Float64bits(want) {
		t.Errorf("roundSignificant(%v) = %v, want %v (as printed and re-parsed)", v, got, want)
	}
}

func TestRoundSignificantMatchesTheTextRoundTrip(t *testing.T) {
	edges := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.1 + 0.2, 1.0 / 3.0, math.Pi, 1e15, 1e-7,
		// Exact ties at the 13th digit, which go to the even neighbour.
		1000000000005, 1000000000015, -1000000000025, 123456789012.5,
		// Carries that change the exponent.
		999999999999.5, 9.9999999999995, 0.99999999999995,
		// Powers of ten, where Log10's estimate is most likely to be off.
		1e22, 1e23, 1e-5, 1e-300, 1e300, 999999999999999999,
		// The ends of the range.
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, 2.2250738585072014e-308,
		1 << 53, 1<<53 + 2,
	}
	for _, v := range edges {
		checkRoundSignificant(t, v)
	}

	// Random bit patterns cover every exponent; random ordinary values
	// cover the numbers people actually type.
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		v := math.Float64frombits(r.Uint64())
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		checkRoundSignificant(t, v)
	}
	for range 20000 {
		checkRoundSignificant(t, (r.Float64()-0.5)*math.Pow(10, float64(r.IntN(30)-10)))
	}
}

func TestRoundSignificantPassesNonFiniteThrough(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1)} {
		if got := roundSignificant(v); got != v {
			t.Errorf("roundSignificant(%v) = %v", v, got)
		}
	}
	if got := roundSignificant(math.NaN()); !math.IsNaN(got) {
		t.Errorf("roundSignificant(NaN) = %v", got)
	}
}

// FuzzRoundSignificant holds the exact rounding to the text round trip it
// replaced, for any finite value.
func FuzzRoundSignificant(f *testing.F) {
	for _, v := range []float64{0.1 + 0.2, 1000000000005, 999999999999.5, math.MaxFloat64, math.SmallestNonzeroFloat64, -1e-300} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v float64) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Skip()
		}
		checkRoundSignificant(t, v)
	})
}

func closeEnough(got, want float64) bool {
	if math.IsNaN(got) || math.IsNaN(want) {
		return math.IsNaN(got) && math.IsNaN(want)
	}
	return math.Abs(got-want) < 1e-9
}
