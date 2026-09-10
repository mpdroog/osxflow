package calc

import "strings"

// LooksLikeExpr reports whether input should be offered to the calculator
// at all.
//
// The gate exists because every keystroke is both a possible app search
// and a possible sum, and the calculator must not win ties it has no
// business winning. Two rules do almost all the work:
//
//   - The input has to start like arithmetic (a digit, a sign, a dot or an
//     open paren) or with a known function name. This is what keeps "7"
//     from shadowing the app "7-Zip" — sorry, "7zip" still searches apps,
//     because a bare number is not a sum.
//   - It has to contain an operator or a call. A lone "2" evaluates
//     perfectly well to 2 and telling the user so is noise.
//
// Being wrong here is cheap in one direction and expensive in the other:
// a missed sum means the user types "=" or an operator and gets it, a
// false positive means an app they wanted is pushed down the list.
func LooksLikeExpr(input string) bool {
	s := strings.TrimSpace(input)
	if s == "" {
		return false
	}
	if !startsLikeMath(s) {
		return false
	}
	return strings.ContainsAny(s, "+-*/%^(")
}

func startsLikeMath(s string) bool {
	c := rune(s[0])
	if isDigit(c) || c == '(' || c == '.' || c == '-' || c == '+' {
		return true
	}
	// A leading name only counts when it is one we can evaluate, so
	// "notes-app" is not mistaken for a subtraction.
	name := leadingIdent(s)
	if name == "" {
		return false
	}
	if _, ok := functions[name]; ok {
		return true
	}
	_, ok := constants[name]
	return ok
}

func leadingIdent(s string) string {
	end := 0
	for i, c := range s {
		if i == 0 {
			if !isIdentStart(c) {
				return ""
			}
			end = len(string(c))
			continue
		}
		if !isIdentPart(c) {
			break
		}
		end = i + len(string(c))
	}
	return strings.ToLower(s[:end])
}
