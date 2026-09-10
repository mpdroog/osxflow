// Package calc evaluates the arithmetic a launcher gets typed into it:
// one line, no variables, no state carried between evaluations.
//
// It is a recursive-descent parser rather than shunting-yard because the
// error messages matter more than the code size here. The result is shown
// live as the user types, which means most inputs the parser ever sees are
// half-finished ("12 *") and the difference between "unexpected end of
// expression" and a bare "syntax error" is the difference between a
// launcher that looks broken and one that looks patient.
package calc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxDepth bounds recursion so a pathological input cannot exhaust the
// stack and take the whole launcher with it. Nobody types 64 nested
// parentheses on purpose; the bound exists because the input is a live
// text field and every keystroke is parsed.
const maxDepth = 64

// ErrIncomplete marks an expression that is a valid prefix but is not
// finished: "2 +", "sqrt(". The UI treats it as "keep quiet and wait"
// rather than as an error worth showing, which is why it is a sentinel
// and not just another message.
var ErrIncomplete = errors.New("incomplete expression")

type parser struct {
	toks  []token
	pos   int
	depth int
}

// Eval evaluates a single arithmetic expression.
//
// Errors are wrapped with ErrIncomplete when the input merely stops early,
// so a caller rendering results as-you-type can tell "not finished" from
// "not valid".
func Eval(expr string) (float64, error) {
	if strings.TrimSpace(expr) == "" {
		return 0, fmt.Errorf("empty expression: %w", ErrIncomplete)
	}
	toks, err := lex(expr)
	if err != nil {
		return 0, err
	}
	p := &parser{toks: toks}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return 0, fmt.Errorf("unexpected %s at position %d", t.kind, t.pos+1)
	}
	return v, nil
}

func (p *parser) peek() token { return p.toks[p.pos] }

// next advances past the current token. EOF is sticky: advancing past it
// would run off the end of the slice, and every caller is already prepared
// to see EOF again.
func (p *parser) next() {
	if p.toks[p.pos].kind != tokEOF {
		p.pos++
	}
}

func (p *parser) accept(k tokenKind) bool {
	if p.peek().kind == k {
		p.pos++
		return true
	}
	return false
}

// unexpected produces the one error message the user actually reads,
// and is the only place that decides whether a failure is "incomplete"
// (the input ran out) or "wrong" (the input contradicted itself).
func (p *parser) unexpected(want string) error {
	t := p.peek()
	if t.kind == tokEOF {
		return fmt.Errorf("expression ends after %s, expected %s: %w", p.prevDesc(), want, ErrIncomplete)
	}
	return fmt.Errorf("unexpected %s at position %d, expected %s", t.kind, t.pos+1, want)
}

func (p *parser) prevDesc() string {
	if p.pos == 0 {
		return "the start"
	}
	prev := p.toks[p.pos-1]
	if prev.text != "" {
		return strconv.Quote(prev.text)
	}
	return prev.kind.String()
}

// expr := term (('+' | '-') term)*
func (p *parser) expr() (float64, error) {
	if err := p.enter(); err != nil {
		return 0, err
	}
	defer p.leave()

	lhs, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		switch {
		case p.accept(tokPlus):
			rhs, err := p.term()
			if err != nil {
				return 0, err
			}
			lhs += rhs
		case p.accept(tokMinus):
			rhs, err := p.term()
			if err != nil {
				return 0, err
			}
			lhs -= rhs
		default:
			return lhs, nil
		}
	}
}

// term := unary (('*' | '/' | '%') unary)*
func (p *parser) term() (float64, error) {
	if err := p.enter(); err != nil {
		return 0, err
	}
	defer p.leave()

	lhs, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		op := p.peek()
		switch op.kind {
		case tokStar:
			p.next()
			rhs, err := p.unary()
			if err != nil {
				return 0, err
			}
			lhs *= rhs
		case tokSlash, tokPercent:
			p.next()
			rhs, err := p.unary()
			if err != nil {
				return 0, err
			}
			// Division by zero is an error rather than +Inf. In a
			// calculator +Inf is never the answer somebody wanted, and
			// showing "+Inf" reads as a bug in the launcher.
			if rhs == 0 {
				if op.kind == tokSlash {
					return 0, fmt.Errorf("division by zero at position %d", op.pos+1)
				}
				return 0, fmt.Errorf("modulo by zero at position %d", op.pos+1)
			}
			if op.kind == tokSlash {
				lhs /= rhs
			} else {
				lhs = math.Mod(lhs, rhs)
			}
		default:
			return lhs, nil
		}
	}
}

// unary := ('-' | '+') unary | power
//
// Unary minus binds looser than '^', so -2^2 is -(2^2) = -4. That is the
// convention every other calculator uses, and disagreeing with it silently
// is worse than not having the operator at all.
func (p *parser) unary() (float64, error) {
	if err := p.enter(); err != nil {
		return 0, err
	}
	defer p.leave()

	switch {
	case p.accept(tokMinus):
		v, err := p.unary()
		return -v, err
	case p.accept(tokPlus):
		return p.unary()
	}
	return p.power()
}

// power := primary ('^' unary)?
//
// Right-associative, so 2^3^2 is 2^9. The right operand is unary rather
// than power so that 2^-1 parses.
func (p *parser) power() (float64, error) {
	base, err := p.primary()
	if err != nil {
		return 0, err
	}
	if !p.accept(tokCaret) {
		return base, nil
	}
	exp, err := p.unary()
	if err != nil {
		return 0, err
	}
	v := math.Pow(base, exp)
	if math.IsNaN(v) && !math.IsNaN(base) && !math.IsNaN(exp) {
		// math.Pow(-8, 1.0/3) is NaN, not -2: the real cube root of a
		// negative number is not what Pow computes. Saying so is more
		// use than rendering "NaN".
		return 0, fmt.Errorf("%s^%s is not a real number", formatOperand(base), formatOperand(exp))
	}
	return v, nil
}

// primary := number | '(' expr ')' | ident | ident '(' args ')'
func (p *parser) primary() (float64, error) {
	if err := p.enter(); err != nil {
		return 0, err
	}
	defer p.leave()

	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.next()
		return t.num, nil

	case tokLParen:
		p.next()
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if !p.accept(tokRParen) {
			return 0, p.unexpected("')'")
		}
		return v, nil

	case tokIdent:
		p.next()
		return p.identifier(t)
	}
	return 0, p.unexpected("a number")
}

func (p *parser) identifier(t token) (float64, error) {
	// A name followed by '(' is a call; anything else is a constant. This
	// ordering means "pi(2)" is an unknown-function error rather than a
	// silently ignored argument list.
	if p.peek().kind == tokLParen {
		return p.call(t)
	}
	if v, ok := constants[t.text]; ok {
		return v, nil
	}
	if _, ok := functions[t.text]; ok {
		return 0, fmt.Errorf("%s is a function, it needs arguments: %s(...)", t.text, t.text)
	}
	return 0, fmt.Errorf("unknown name %q at position %d", t.text, t.pos+1)
}

func (p *parser) call(name token) (float64, error) {
	fn, ok := functions[name.text]
	if !ok {
		return 0, fmt.Errorf("unknown function %q at position %d", name.text, name.pos+1)
	}
	p.next() // '('

	var args []float64
	if p.peek().kind != tokRParen {
		for {
			v, err := p.expr()
			if err != nil {
				return 0, err
			}
			args = append(args, v)
			if !p.accept(tokComma) {
				break
			}
		}
	}
	if !p.accept(tokRParen) {
		return 0, p.unexpected("')'")
	}
	if len(args) != fn.arity {
		return 0, fmt.Errorf("%s takes %s, got %d", name.text, plural(fn.arity, "argument"), len(args))
	}
	return fn.call(args)
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return fmt.Errorf("expression nested more than %d deep", maxDepth)
	}
	return nil
}

func (p *parser) leave() { p.depth-- }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// formatOperand renders a number inside an error message, where a full
// 17-digit float would bury the sentence it appears in.
func formatOperand(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}
