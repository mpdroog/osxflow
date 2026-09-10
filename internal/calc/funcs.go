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
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', displayPrecision, 64), 64)
	if err != nil {
		rounded = v
	}
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
