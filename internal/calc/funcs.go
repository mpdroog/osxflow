package calc

// The function and constant tables, plus result formatting.
//
// Every function that has a restricted domain reports going outside it as
// an error rather than returning NaN. A launcher showing "NaN" looks
// broken; one showing "sqrt is not defined for negative numbers" has
// answered the question.

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

type function struct {
	call  func([]float64) (float64, error)
	arity int
}

var constants = map[string]float64{
	"pi":  math.Pi,
	"e":   math.E,
	"tau": 2 * math.Pi,
	"phi": math.Phi,
}

// functions is populated in init because several entries refer to helpers
// declared below it, and a plain composite literal at package scope makes
// that an initialisation cycle.
var functions map[string]function

func init() {
	unary := func(f func(float64) float64) function {
		return function{arity: 1, call: func(a []float64) (float64, error) { return f(a[0]), nil }}
	}
	binary := func(f func(float64, float64) float64) function {
		return function{arity: 2, call: func(a []float64) (float64, error) { return f(a[0], a[1]), nil }}
	}
	// checked wraps a partial function with the domain test that makes its
	// failure legible.
	checked := func(ok func(float64) bool, msg string, f func(float64) float64) function {
		return function{arity: 1, call: func(a []float64) (float64, error) {
			if !ok(a[0]) {
				return 0, fmt.Errorf("%s, got %s", msg, formatOperand(a[0]))
			}
			return f(a[0]), nil
		}}
	}
	nonNegative := func(v float64) bool { return v >= 0 }
	positive := func(v float64) bool { return v > 0 }
	inUnitRange := func(v float64) bool { return v >= -1 && v <= 1 }

	functions = map[string]function{
		"sqrt":  checked(nonNegative, "sqrt is not defined for negative numbers", math.Sqrt),
		"ln":    checked(positive, "ln is only defined for positive numbers", math.Log),
		"log":   checked(positive, "log is only defined for positive numbers", math.Log10),
		"log2":  checked(positive, "log2 is only defined for positive numbers", math.Log2),
		"asin":  checked(inUnitRange, "asin is only defined between -1 and 1", math.Asin),
		"acos":  checked(inUnitRange, "acos is only defined between -1 and 1", math.Acos),
		"cbrt":  unary(math.Cbrt),
		"abs":   unary(math.Abs),
		"floor": unary(math.Floor),
		"ceil":  unary(math.Ceil),
		"round": unary(math.Round),
		"trunc": unary(math.Trunc),
		"exp":   unary(math.Exp),
		"sin":   unary(math.Sin),
		"cos":   unary(math.Cos),
		"tan":   unary(math.Tan),
		"atan":  unary(math.Atan),
		"min":   binary(math.Min),
		"max":   binary(math.Max),
		"hypot": binary(math.Hypot),
		"atan2": binary(math.Atan2),
		"pow": {arity: 2, call: func(a []float64) (float64, error) {
			v := math.Pow(a[0], a[1])
			if math.IsNaN(v) && !math.IsNaN(a[0]) && !math.IsNaN(a[1]) {
				return 0, fmt.Errorf("pow(%s, %s) is not a real number", formatOperand(a[0]), formatOperand(a[1]))
			}
			return v, nil
		}},
	}
}

// displayPrecision is the number of significant digits kept before
// trailing zeros are trimmed. 12 is chosen to hide binary-float noise —
// 0.1+0.2 renders as 0.3, not 0.30000000000000004 — while keeping enough
// digits that a deliberately long value is not quietly truncated.
const displayPrecision = 12

// Format renders a result for display: no exponent for ordinary numbers,
// no trailing zeros, and no float noise.
func Format(v float64) string {
	switch {
	case math.IsNaN(v):
		return "not a number"
	case math.IsInf(v, 1):
		return "infinity"
	case math.IsInf(v, -1):
		return "-infinity"
	}

	// Round to displayPrecision significant digits, then re-format with
	// the shortest representation that round-trips *that* value. Doing it
	// in two steps is what turns 0.30000000000000004 into "0.3" without
	// also turning 1234567890123 into "1.23456789012e+12".
	rounded := roundSignificant(v)
	if rounded == math.Trunc(rounded) && math.Abs(rounded) < 1e15 {
		return strconv.FormatFloat(rounded, 'f', -1, 64)
	}
	// 'f' for anything a person would read as a decimal, 'g' beyond that,
	// so 0.00001 stays 0.00001 and 1e30 does not print 31 digits.
	if a := math.Abs(rounded); a >= 1e-5 && a < 1e15 {
		return strconv.FormatFloat(rounded, 'f', -1, 64)
	}
	return strconv.FormatFloat(rounded, 'g', -1, 64)
}

// roundSignificant rounds v to the float64 nearest to v written with
// displayPrecision significant decimal digits.
//
// This is what printing v with FormatFloat(v, 'g', displayPrecision) and
// parsing the text back gives, computed without the text. The round trip was the
// obvious way to write it, but it makes ParseFloat part of formatting, and
// ParseFloat has an error that here could only ever be ignored. The
// arithmetic is exact rational arithmetic, not float: v*10^k in floating
// point is itself rounded, which lands a value near a tie on the wrong side
// of it and prints 0.30000000000000004 again. Two things make it agree
// with strconv exactly: ties go to the even digit, as strconv's fixed-
// precision formatting does, and the final conversion is correctly
// rounded, as ParseFloat's is. The test holds it to that.
//
// Format calls this once per keystroke on one number; big.Rat is plenty
// fast for that.
func roundSignificant(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	x := new(big.Rat).SetFloat64(math.Abs(v)) // exact: every float64 is a fraction

	// The decimal exponent of the leading digit. Log10 gets it right or
	// one off either way near a power of ten; the exact comparisons fix
	// the estimate rather than trust it.
	exp := int(math.Floor(math.Log10(math.Abs(v))))
	for x.Cmp(pow10(exp)) < 0 {
		exp--
	}
	for x.Cmp(pow10(exp+1)) >= 0 {
		exp++
	}

	// Scale so the digits to keep are the integer part, round that to an
	// integer with ties to even, and scale back.
	shift := displayPrecision - 1 - exp
	scaled := new(big.Rat).Mul(x, pow10(shift))
	q, r := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	switch c := r.Lsh(r, 1).Cmp(scaled.Denom()); {
	case c > 0, c == 0 && q.Bit(0) == 1:
		q.Add(q, big.NewInt(1))
	}
	out := new(big.Rat).Mul(new(big.Rat).SetInt(q), pow10(-shift))

	// The exactness flag is not a failure: the result is a decimal that
	// usually has no exact binary form, and nearest is what was asked for.
	// Overflow cannot happen either: rounding to 12 digits can only carry
	// past MaxFloat64 from a value that is already above it.
	f, _ := out.Float64()
	if v < 0 {
		return -f
	}
	return f
}

// pow10 is 10^n as an exact rational, for negative n too.
func pow10(n int) *big.Rat {
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(n))), nil)
	if n < 0 {
		return new(big.Rat).SetFrac(big.NewInt(1), p)
	}
	return new(big.Rat).SetInt(p)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
