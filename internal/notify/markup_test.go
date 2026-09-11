package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripMarkup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"<b>bold</b> and <i>it</i> and <u>u</u>", "bold and it and u"},
		{`<a href="https://x.org/?a=1&amp;b=2">link</a>`, "link"},
		{`<img src="x.png" alt="pic"/>after`, "after"},
		{"<!-- note -->kept", "kept"},
		{"a < b and c > d", "a < b and c > d"},
		{"<3 you", "<3 you"},
		{"x <b <i>y</i>", "x <b y"},
		{"<>", "<>"},
		{"< b>", "< b>"},
		{"fish & chips", "fish & chips"},
		{"&lt;tag&gt; &quot;q&quot; &apos;a&apos; &amp;", `<tag> "q" 'a' &`},
		{"&#65;&#x42;&#X43;", "ABC"},
		{"a&nbsp;b", "a\u00a0b"},
		{"&#0; &#xD800; &#x110000; &bogus; &#; &#xZZ;", "&#0; &#xD800; &#x110000; &bogus; &#; &#xZZ;"},
		{"line<br>next<br/>last<BR />", "line\nnext\nlast\n"},
		{"&#9;tab", " tab"},
		{"&#7;bell", "bell"},
		{"&#x202e;rev", "rev"},
		{"&#10;", "\n"},
	} {
		if got := StripMarkup(tc.in); got != tc.want {
			t.Errorf("StripMarkup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripMarkupIsLinearOnUnclosedTags(t *testing.T) {
	// Each '<' looks at most maxTagLen bytes ahead, so this is a few
	// megabytes of scanning rather than the tens of gigabytes an unbounded
	// search would do.
	s := strings.Repeat("<", 1<<16)
	if got := StripMarkup(s); got != s {
		t.Fatal("a run of '<' was not kept as text")
	}
}

// FuzzStripMarkup checks the promises StripMarkup makes about arbitrary
// input: valid UTF-8 out, no control characters but newlines, text with
// nothing to strip left alone, and never longer than what came in.
func FuzzStripMarkup(f *testing.F) {
	for _, s := range []string{
		"<b>x</b>", "a < b", "&amp;&#x41;", "<br>", "\x00\u202e", "<<<>>>&&&;;;", "&#x10ffff;", "<a href='&'>",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := StripMarkup(s)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out: %q", out)
		}
		for _, r := range out {
			if unwanted(r) {
				t.Fatalf("control character %U survived in %q", r, out)
			}
		}
		cleaned := clean(s)
		if !strings.ContainsAny(s, "<&") && out != cleaned {
			t.Fatalf("text without markup changed: %q -> %q", cleaned, out)
		}
		if len(out) > len(cleaned) {
			t.Fatalf("output %d bytes, longer than the %d that went in", len(out), len(cleaned))
		}
	})
}
