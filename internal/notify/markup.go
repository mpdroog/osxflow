package notify

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StripMarkup turns a notification body into plain text.
//
// A server advertising "body-markup" may be sent a small subset of HTML:
// <b>, <i>, <u>, <a href> and <img>. This one advertises it, because the
// alternative is worse -- plenty of clients send markup whatever the
// capabilities say, and "<b>Meeting</b> in 5 minutes" should not be drawn
// with its tags showing. None of the styling is drawn, only the text: bold
// and italic bodies would need two more fonts for a difference nobody
// reads a notification for.
//
// Anything that is not a well-formed tag stays as text, so "a < b" and
// "fish & chips" survive. Entities are decoded, and <br> becomes a newline
// because some senders use it for exactly that.
func StripMarkup(s string) string {
	s = clean(s)
	if !strings.ContainsAny(s, "<&") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch s[i] {
		case '<':
			if n, isBreak := tagAt(s[i:]); n > 0 {
				if isBreak {
					b.WriteByte('\n')
				}
				i += n
				continue
			}
		case '&':
			if r, n := entityAt(s[i:]); n > 0 {
				switch {
				case r == '\t':
					b.WriteByte(' ')
				case !unwanted(r):
					b.WriteRune(r)
				}
				i += n
				continue
			}
		}
		// Copying a byte at a time keeps multi-byte runes intact: the only
		// bytes intercepted above are ASCII, which never occur inside one.
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// maxTagLen bounds how far ahead a '<' looks for its '>'. Without it, a
// body of ten thousand '<' and no '>' would be scanned to the end once for
// each of them.
const maxTagLen = 512

// tagAt reports how long the tag at the start of s is, or 0 when s does not
// start with one, and whether it is a line break.
func tagAt(s string) (n int, isBreak bool) {
	end := strings.IndexByte(s[:min(len(s), maxTagLen)], '>')
	if end < 2 {
		return 0, false
	}
	inner := s[1:end]
	if strings.ContainsRune(inner, '<') {
		return 0, false
	}
	name := strings.TrimPrefix(inner, "/")
	if name == "" {
		return 0, false
	}
	if c := name[0]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '!' {
		return 0, false
	}
	return end + 1, strings.EqualFold(strings.TrimRight(strings.TrimSpace(inner), "/ "), "br")
}

// maxEntityLen covers the longest entity understood here, "&#x10ffff;".
const maxEntityLen = 10

// entityAt decodes the character reference at the start of s, returning the
// rune and how many bytes it took, or 0 bytes when s does not start with one
// this understands.
func entityAt(s string) (r rune, n int) {
	end := strings.IndexByte(s[:min(len(s), maxEntityLen+1)], ';')
	if end < 2 {
		return 0, 0
	}
	name := s[1:end]
	switch name {
	case "amp":
		return '&', end + 1
	case "lt":
		return '<', end + 1
	case "gt":
		return '>', end + 1
	case "quot":
		return '"', end + 1
	case "apos":
		return '\'', end + 1
	case "nbsp":
		return '\u00a0', end + 1
	}
	if name[0] != '#' || len(name) < 2 {
		return 0, 0
	}
	digits, base := name[1:], 10
	if digits[0] == 'x' || digits[0] == 'X' {
		digits, base = digits[1:], 16
	}
	v, err := strconv.ParseUint(digits, base, 32)
	if err != nil || v == 0 || v > unicode.MaxRune {
		return 0, 0
	}
	r = rune(v)
	if !utf8.ValidRune(r) {
		return 0, 0
	}
	return r, end + 1
}
