package calc

// The lexer. Split out from the parser because the two have genuinely
// different failure modes: the lexer rejects characters that cannot
// appear in an expression at all, the parser rejects tokens that cannot
// appear *there*. Keeping them apart is what lets the error messages say
// which of the two happened.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokIdent
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokPercent
	tokCaret
	tokLParen
	tokRParen
	tokComma
)

// String is for error messages only; the parser matches on kind.
func (k tokenKind) String() string {
	switch k {
	case tokEOF:
		return "end of expression"
	case tokNumber:
		return "number"
	case tokIdent:
		return "name"
	case tokPlus:
		return "'+'"
	case tokMinus:
		return "'-'"
	case tokStar:
		return "'*'"
	case tokSlash:
		return "'/'"
	case tokPercent:
		return "'%'"
	case tokCaret:
		return "'^'"
	case tokLParen:
		return "'('"
	case tokRParen:
		return "')'"
	case tokComma:
		return "','"
	}
	return "unknown token"
}

// token carries pos so an error can point at the offending rune rather
// than just naming it: with "2 ** 3" the useful half of the message is
// which star was the surprise.
type token struct {
	text string
	num  float64
	pos  int
	kind tokenKind
}

// lex turns src into tokens. It is deliberately total: every rune either
// becomes a token or produces an error naming its position, so the parser
// never has to consider input it has not been handed.
func lex(src string) ([]token, error) {
	var toks []token
	rs := []rune(src)
	i := 0
	for i < len(rs) {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
			continue
		case isDigit(c) || c == '.':
			tok, next, err := lexNumber(rs, i)
			if err != nil {
				return nil, err
			}
			toks, i = append(toks, tok), next
			continue
		case isIdentStart(c):
			start := i
			for i < len(rs) && isIdentPart(rs[i]) {
				i++
			}
			toks = append(toks, token{
				kind: tokIdent,
				text: strings.ToLower(string(rs[start:i])),
				pos:  start,
			})
			continue
		}

		kind, ok := punct(c)
		if !ok {
			return nil, fmt.Errorf("unexpected character %q at position %d", string(c), i+1)
		}
		toks = append(toks, token{kind: kind, text: string(c), pos: i})
		i++
	}
	return append(toks, token{kind: tokEOF, pos: len(rs)}), nil
}

func punct(c rune) (tokenKind, bool) {
	switch c {
	case '+':
		return tokPlus, true
	case '-':
		return tokMinus, true
	case '*':
		return tokStar, true
	case '/':
		return tokSlash, true
	case '%':
		return tokPercent, true
	case '^':
		return tokCaret, true
	case '(':
		return tokLParen, true
	case ')':
		return tokRParen, true
	case ',':
		return tokComma, true
	}
	return tokEOF, false
}

// lexNumber accepts decimal and hex. Hex earns its place because the
// thing you most often want a launcher to tell you is what 0xff is.
//
// It deliberately does NOT accept a leading sign: "-2" is unary minus
// applied to 2, which is the parser's job. Folding the sign in here
// makes "3-2" lex as [3][-2] and silently evaluate to 3.
func lexNumber(rs []rune, i int) (token, int, error) {
	start := i
	if rs[i] == '0' && i+1 < len(rs) && (rs[i+1] == 'x' || rs[i+1] == 'X') {
		i += 2
		for i < len(rs) && isHexDigit(rs[i]) {
			i++
		}
		text := string(rs[start:i])
		if i-start == 2 {
			return token{}, 0, fmt.Errorf("%q at position %d is not a number", text, start+1)
		}
		// 64-bit unsigned so 0xffffffffffffffff parses; the value is then
		// carried as float64 like everything else, losing precision above
		// 2^53. That is a real limit, but it is the limit of the whole
		// calculator, not of this branch.
		v, err := strconv.ParseUint(text[2:], 16, 64)
		if err != nil {
			// Only the digits were checked above, so the one way left to
			// fail is a value wider than 64 bits -- which the cause says.
			return token{}, 0, fmt.Errorf("%q at position %d is not a number: %w", text, start+1, numCause(err))
		}
		return token{kind: tokNumber, num: float64(v), text: text, pos: start}, i, nil
	}

	seenDot, seenExp := false, false
	for i < len(rs) {
		c := rs[i]
		switch {
		case isDigit(c):
			i++
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
			i++
		case (c == 'e' || c == 'E') && !seenExp && i > start && hasDigit(rs[start:i]):
			// Only an exponent if a digit or sign actually follows, so the
			// "e" in "2e" stays available as the constant e and "2e" is a
			// missing operator rather than a broken number.
			if i+1 < len(rs) && (isDigit(rs[i+1]) || ((rs[i+1] == '+' || rs[i+1] == '-') && i+2 < len(rs) && isDigit(rs[i+2]))) {
				seenExp = true
				i += 2
				continue
			}
			return finishNumber(rs, start, i)
		default:
			return finishNumber(rs, start, i)
		}
	}
	return finishNumber(rs, start, i)
}

func finishNumber(rs []rune, start, end int) (token, int, error) {
	text := string(rs[start:end])
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		// "1e999" is well formed and still fails: it is out of range, and
		// saying so beats calling a perfectly good-looking number not one.
		return token{}, 0, fmt.Errorf("%q at position %d is not a number: %w", text, start+1, numCause(err))
	}
	return token{kind: tokNumber, num: v, text: text, pos: start}, end, nil
}

// numCause strips strconv's "strconv.ParseFloat: parsing ..." framing,
// which repeats the text the message already quotes, leaving the reason
// ("value out of range"). The result still matches strconv.ErrRange and
// strconv.ErrSyntax with errors.Is.
func numCause(err error) error {
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return ne.Err
	}
	return err
}

func isDigit(c rune) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c rune) bool { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isIdentStart(c rune) bool {
	return c == '_' || unicode.IsLetter(c)
}
func isIdentPart(c rune) bool { return isIdentStart(c) || isDigit(c) }

func hasDigit(rs []rune) bool {
	for _, c := range rs {
		if isDigit(c) {
			return true
		}
	}
	return false
}
