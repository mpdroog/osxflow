package notify

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const fallback = 5 * time.Second

// parse is Parse for the tests that look only at the result: problems are
// covered by TestParseReportsProblems.
func parse(req *Request) Notification {
	if req.Hints == nil {
		req.Hints = map[string]any{}
	}
	n, _ := Parse(req, fallback)
	return n
}

func TestParseReportsNothingForAWellFormedRequest(t *testing.T) {
	pixel := []any{int32(1), int32(1), int32(4), true, int32(8), int32(4), []byte{1, 2, 3, 255}}
	_, problems := Parse(&Request{
		AppName: "Mail", AppIcon: "thunderbird", Summary: "s", Body: "<b>b</b>",
		Actions: []string{"default", "", "reply", "Reply"},
		Hints: map[string]any{
			"urgency": uint8(2), "value": int32(50), "resident": true, "category": "email",
			"desktop-entry": "thunderbird", "image-data": pixel, "image-path": "file:///tmp/x.png",
			"x-canonical-private-synchronous": "", "sound-name": "message", "transient": true,
		},
		ExpireTimeout: -1,
	}, fallback)
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none", problems)
	}
}

func TestParseReportsProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
		want string // in the one problem reported
		ok   func(n *Notification) bool
	}{
		{name: "urgency out of range", req: Request{Hints: map[string]any{"urgency": uint8(9)}},
			want: "urgency hint 9", ok: func(n *Notification) bool { return n.Urgency == Normal }},
		{name: "urgency of the wrong type", req: Request{Hints: map[string]any{"urgency": "2"}},
			want: `"urgency" is string`, ok: func(n *Notification) bool { return n.Urgency == Normal }},
		{name: "urgency out of int64 range", req: Request{Hints: map[string]any{"urgency": uint64(1 << 63)}},
			want: "out of range", ok: func(n *Notification) bool { return n.Urgency == Normal }},
		{name: "value clamped", req: Request{Hints: map[string]any{"value": int32(150)}},
			want: "value hint 150", ok: func(n *Notification) bool { return n.Value == 100 }},
		{name: "odd actions", req: Request{Actions: []string{"a", "A", "dangling"}},
			want: `"dangling" is ignored`, ok: func(n *Notification) bool { return len(n.Actions) == 1 }},
		{name: "empty action key", req: Request{Actions: []string{"", "A"}},
			want: "empty key", ok: func(n *Notification) bool { return len(n.Actions) == 0 }},
		{name: "long action key", req: Request{Actions: []string{strings.Repeat("k", maxKeyBytes+1), "A"}},
			want: "over the", ok: func(n *Notification) bool { return len(n.Actions) == 0 }},
		{name: "action key not UTF-8", req: Request{Actions: []string{"\xff", "A"}},
			want: "not UTF-8", ok: func(n *Notification) bool { return len(n.Actions) == 0 }},
		{name: "sync of the wrong type", req: Request{Hints: map[string]any{"synchronous": true}},
			want: `"synchronous" is bool`, ok: func(n *Notification) bool { return n.Sync == "" }},
		{name: "malformed image-data", req: Request{Hints: map[string]any{"image-data": []any{int32(1)}}},
			want: `"image-data"`, ok: func(n *Notification) bool { return n.Icon.Data == nil }},
		{name: "file URI without a path", req: Request{AppIcon: "file://x.png"},
			want: "no absolute path", ok: func(n *Notification) bool { return n.Icon.Path == "" }},
		{name: "unparseable file URI", req: Request{AppIcon: "file://%zz/x.png"},
			want: "app_icon", ok: func(n *Notification) bool { return n.Icon.Path == "" }},
		{name: "relative path", req: Request{Hints: map[string]any{"image-path": "icons/x.png"}},
			want: `"image-path"`, ok: func(n *Notification) bool { return n.Icon.Path == "" && n.Icon.Name == "" }},
		{name: "string hint of the wrong type", req: Request{Hints: map[string]any{"category": int32(1)}},
			want: `"category" is int32`, ok: func(n *Notification) bool { return n.Category == "" }},
		{name: "bool hint of the wrong type", req: Request{Hints: map[string]any{"resident": uint8(1)}},
			want: `"resident" is uint8`, ok: func(n *Notification) bool { return !n.Resident }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, problems := Parse(&tc.req, fallback)
			if len(problems) != 1 || !strings.Contains(problems[0].Error(), tc.want) {
				t.Errorf("problems = %v, want one mentioning %q", problems, tc.want)
			}
			if !tc.ok(&n) {
				t.Errorf("notification %+v does not fall back as it should", n)
			}
		})
	}
}

func TestParseKeepsImageDataErrors(t *testing.T) {
	_, problems := Parse(&Request{Hints: map[string]any{"image-data": "not a struct"}}, fallback)
	if len(problems) != 1 || !errors.Is(problems[0], ErrImageData) {
		t.Fatalf("problems = %v, want one wrapping ErrImageData", problems)
	}
}

func TestParseReportsTooManyActionsOnce(t *testing.T) {
	raw := make([]string, 0, 2*(maxActions+3))
	for i := range maxActions + 3 {
		raw = append(raw, fmt.Sprint("k", i), "label")
	}
	_, problems := Parse(&Request{Actions: raw}, fallback)
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "only the first") {
		t.Fatalf("problems = %v, want one saying the extra actions were dropped", problems)
	}
}

func TestParseFallsBackToTheNextImageData(t *testing.T) {
	pixel := []any{int32(1), int32(1), int32(4), true, int32(8), int32(4), []byte{1, 2, 3, 255}}
	n, problems := Parse(&Request{Hints: map[string]any{"image-data": []any{}, "icon_data": pixel}}, fallback)
	if n.Icon.Data == nil {
		t.Error("the usable icon_data was not taken after image-data failed")
	}
	if len(problems) != 1 {
		t.Errorf("problems = %v, want the broken image-data reported", problems)
	}
}

// FuzzParse throws arbitrary requests at Parse and checks the promises it
// makes to the renderer, whatever came in: clean, bounded text, levels in
// range, and every problem a real error.
func FuzzParse(f *testing.F) {
	f.Add("Mail", "firefox", "Subject", "<b>Body</b>", "default\x00\x00reply\x00Reply", "urgency", uint8(0), int64(2), "x", int32(-1))
	f.Add("", "file:///tmp/a.png", "", "&#x41;", "odd", "value", uint8(0), int64(500), "", int32(0))
	f.Add("\u202e", "icons/x", "\x00", "", "", "synchronous", uint8(1), int64(0), "vol", int32(5000))
	f.Add("a", "b", "c", "d", "\x00", "image-data", uint8(3), int64(1), "", int32(7))
	f.Fuzz(func(t *testing.T, appName, appIcon, summary, body, actions, hintKey string, hintKind uint8, hintInt int64, hintStr string, timeout int32) {
		var hint any
		switch hintKind % 5 {
		case 0:
			hint = int32(hintInt)
		case 1:
			hint = hintStr
		case 2:
			hint = hintInt != 0
		case 3:
			hint = []any{int32(hintInt), int32(hintInt), int32(hintInt), true, int32(8), int32(4), []byte(hintStr)}
		case 4:
			hint = uint64(hintInt)
		}
		req := &Request{
			AppName: appName, AppIcon: appIcon, Summary: summary, Body: body,
			Actions: strings.Split(actions, "\x00"), ExpireTimeout: timeout,
			Hints: map[string]any{hintKey: hint},
		}
		n, problems := Parse(req, fallback)

		for _, s := range append([]string{n.AppName, n.Summary, n.Body, n.DesktopEntry, n.Category, n.Sync, n.Icon.Name}, labels(n.Actions)...) {
			if !utf8.ValidString(s) {
				t.Fatalf("invalid UTF-8 in %q", s)
			}
			for _, r := range s {
				if unwanted(r) {
					t.Fatalf("control character %U in %q", r, s)
				}
			}
		}
		switch {
		case utf8.RuneCountInString(n.AppName) > maxNameRunes, utf8.RuneCountInString(n.Summary) > maxSummaryRunes,
			utf8.RuneCountInString(n.Body) > maxBodyRunes:
			t.Fatalf("text over its limit: %d/%d/%d runes", utf8.RuneCountInString(n.AppName),
				utf8.RuneCountInString(n.Summary), utf8.RuneCountInString(n.Body))
		case len(n.Actions) > maxActions:
			t.Fatalf("%d actions kept", len(n.Actions))
		case n.Urgency > Critical:
			t.Fatalf("urgency %d", n.Urgency)
		case n.Value < -1 || n.Value > 100:
			t.Fatalf("value %d", n.Value)
		case n.Timeout < 0:
			t.Fatalf("timeout %v", n.Timeout)
		case n.Icon.Path != "" && !strings.HasPrefix(n.Icon.Path, "/"):
			t.Fatalf("icon path %q is not absolute", n.Icon.Path)
		}
		for _, a := range n.Actions {
			if a.Key == "" || len(a.Key) > maxKeyBytes || !utf8.ValidString(a.Key) || utf8.RuneCountInString(a.Label) > maxLabelRunes {
				t.Fatalf("unusable action %+v kept", a)
			}
		}
		for i, p := range problems {
			if p == nil {
				t.Fatalf("problem %d is nil", i)
			}
		}
	})
}

func labels(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Label)
	}
	return out
}

func TestParseCleansText(t *testing.T) {
	n := parse(&Request{
		AppName:       "Mail\n",
		Summary:       "New\tmessage\x07 from \u202eevil\nsecond line",
		Body:          "<b>Hi</b> &amp; welcome\r\nline two\x00",
		ExpireTimeout: -1,
	})
	if n.AppName != "Mail" {
		t.Errorf("AppName = %q, want %q", n.AppName, "Mail")
	}
	if want := "New message from evil second line"; n.Summary != want {
		t.Errorf("Summary = %q, want %q", n.Summary, want)
	}
	if want := "Hi & welcome\nline two"; n.Body != want {
		t.Errorf("Body = %q, want %q", n.Body, want)
	}
}

func TestParseReplacesInvalidUTF8(t *testing.T) {
	n := parse(&Request{Summary: "caf\xe9", Body: "\xff\xfe"})
	if !utf8.ValidString(n.Summary) || !utf8.ValidString(n.Body) {
		t.Fatalf("invalid UTF-8 survived: %q %q", n.Summary, n.Body)
	}
}

func TestParseClipsLongText(t *testing.T) {
	n := parse(&Request{
		Summary: strings.Repeat("s", 5000),
		Body:    strings.Repeat("body ", 1<<16),
	})
	if got := utf8.RuneCountInString(n.Summary); got != maxSummaryRunes {
		t.Errorf("summary is %d runes, want it clipped to %d", got, maxSummaryRunes)
	}
	if !strings.HasSuffix(n.Summary, "…") {
		t.Errorf("clipped summary %q… does not say so", n.Summary[:20])
	}
	if got := utf8.RuneCountInString(n.Body); got > maxBodyRunes {
		t.Errorf("body is %d runes, over %d", got, maxBodyRunes)
	}
}

func TestTitleFallsBackToTheAppName(t *testing.T) {
	n := parse(&Request{AppName: "Backup"})
	if n.Title() != "Backup" {
		t.Errorf("Title() = %q, want the app name", n.Title())
	}
	n = parse(&Request{AppName: "Backup", Summary: "Done"})
	if n.Title() != "Done" {
		t.Errorf("Title() = %q, want the summary", n.Title())
	}
}

func TestTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ms      int32
		urgency any
		want    time.Duration
	}{
		{"server default", -1, uint8(1), fallback},
		{"explicit", 2500, uint8(1), 2500 * time.Millisecond},
		{"never", 0, uint8(1), 0},
		{"critical default is sticky", -1, uint8(2), 0},
		{"critical honours an explicit timeout", 3000, uint8(2), 3 * time.Second},
		{"any other negative is the default", -7, uint8(1), fallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := parse(&Request{ExpireTimeout: tc.ms, Hints: map[string]any{"urgency": tc.urgency}})
			if n.Timeout != tc.want {
				t.Errorf("Timeout = %v, want %v", n.Timeout, tc.want)
			}
		})
	}
}

func TestUrgency(t *testing.T) {
	for _, tc := range []struct {
		hint any
		want Urgency
	}{
		{uint8(0), Low},
		{uint8(1), Normal},
		{uint8(2), Critical},
		{int32(2), Critical}, // wrong type per the spec, but unambiguous
		{uint8(9), Normal},
		{"2", Normal},
		{nil, Normal},
	} {
		n := parse(&Request{Hints: map[string]any{"urgency": tc.hint}})
		if n.Urgency != tc.want {
			t.Errorf("urgency hint %#v gave %d, want %d", tc.hint, n.Urgency, tc.want)
		}
	}
}

func TestActions(t *testing.T) {
	n := parse(&Request{Actions: []string{"default", "", "reply", "Reply", "archive", "", "dangling"}})
	if len(n.Actions) != 3 {
		t.Fatalf("Actions = %+v, want three pairs and the odd one out dropped", n.Actions)
	}
	if !n.HasAction(DefaultAction) || !n.HasAction("reply") || n.HasAction("dangling") {
		t.Errorf("HasAction disagrees with %+v", n.Actions)
	}
	buttons := n.Buttons()
	want := []Action{{Key: "reply", Label: "Reply"}, {Key: "archive", Label: "archive"}}
	if !slices.Equal(buttons, want) {
		t.Errorf("Buttons() = %+v, want %+v: the default is not a button, and an unlabelled one shows its key", buttons, want)
	}
}

func TestActionsAreBounded(t *testing.T) {
	raw := make([]string, 0, 40)
	for i := range 20 {
		raw = append(raw, "k"+strings.Repeat("x", i), "label")
	}
	raw = append([]string{strings.Repeat("k", maxKeyBytes+1), "too long"}, raw...)
	n := parse(&Request{Actions: raw})
	if len(n.Actions) != maxActions {
		t.Fatalf("%d actions kept, want %d", len(n.Actions), maxActions)
	}
	if n.Actions[0].Label == "too long" {
		t.Error("an action with an oversized key was kept")
	}
}

func TestValueHint(t *testing.T) {
	for _, tc := range []struct {
		hints map[string]any
		want  int
	}{
		{map[string]any{}, -1},
		{map[string]any{"value": int32(40)}, 40},
		{map[string]any{"value": uint32(40)}, 40},
		{map[string]any{"value": int32(150)}, 100},
		{map[string]any{"value": int32(-3)}, 0},
		{map[string]any{"value": "40"}, -1},
	} {
		if n := parse(&Request{Hints: tc.hints}); n.Value != tc.want {
			t.Errorf("hints %v gave value %d, want %d", tc.hints, n.Value, tc.want)
		}
	}
}

func TestSyncHint(t *testing.T) {
	for _, tc := range []struct {
		hints map[string]any
		want  string
	}{
		{map[string]any{}, ""},
		{map[string]any{"x-canonical-private-synchronous": "volume"}, "hint:volume"},
		{map[string]any{"synchronous": "volume"}, "hint:volume"},
		{map[string]any{"x-canonical-private-synchronous": ""}, "app:Mixer"},
		{map[string]any{"x-canonical-private-synchronous": true}, ""},
	} {
		if n := parse(&Request{AppName: "Mixer", Hints: tc.hints}); n.Sync != tc.want {
			t.Errorf("hints %v gave sync %q, want %q", tc.hints, n.Sync, tc.want)
		}
	}
}

func TestDesktopEntryHint(t *testing.T) {
	n := parse(&Request{Hints: map[string]any{"desktop-entry": "org.gnome.Calendar.desktop"}})
	if n.DesktopEntry != "org.gnome.Calendar" {
		t.Errorf("DesktopEntry = %q, want the suffix trimmed", n.DesktopEntry)
	}
}

func TestIcons(t *testing.T) {
	pixel := []any{int32(1), int32(1), int32(4), true, int32(8), int32(4), []byte{1, 2, 3, 255}}
	for _, tc := range []struct {
		name     string
		appIcon  string
		hints    map[string]any
		wantPath string
		wantName string
		wantData bool
	}{
		{name: "theme name", appIcon: "firefox", wantName: "firefox"},
		{name: "absolute path", appIcon: "/usr/share/pixmaps/x.png", wantPath: "/usr/share/pixmaps/x.png"},
		{name: "file URI", appIcon: "file:///tmp/a%20b.png", wantPath: "/tmp/a b.png"},
		{name: "relative path is unusable", appIcon: "icons/x.png"},
		{name: "other URI is unusable", appIcon: "https://example.com/x.png"},
		{
			name: "image-path outranks app_icon", appIcon: "firefox",
			hints:    map[string]any{"image-path": "/tmp/avatar.png"},
			wantPath: "/tmp/avatar.png", wantName: "firefox",
		},
		{
			name: "image-path may be a name", appIcon: "thunderbird",
			hints:    map[string]any{"image-path": "mail-unread"},
			wantName: "mail-unread",
		},
		{name: "image-data", hints: map[string]any{"image-data": pixel}, wantData: true},
		{name: "image_data, the 1.1 spelling", hints: map[string]any{"image_data": pixel}, wantData: true},
		{name: "icon_data, the 1.0 spelling", hints: map[string]any{"icon_data": pixel}, wantData: true},
		{name: "malformed image-data is ignored", hints: map[string]any{"image-data": []any{int32(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := parse(&Request{AppIcon: tc.appIcon, Hints: tc.hints})
			if n.Icon.Path != tc.wantPath || n.Icon.Name != tc.wantName || (n.Icon.Data != nil) != tc.wantData {
				t.Errorf("Icon = {Path:%q Name:%q Data:%t}, want {Path:%q Name:%q Data:%t}",
					n.Icon.Path, n.Icon.Name, n.Icon.Data != nil, tc.wantPath, tc.wantName, tc.wantData)
			}
		})
	}
}

func TestCleanRemovesBidiOverrides(t *testing.T) {
	if got := clean("pay\u202eevil\u2066x\u200fy"); got != "payevilxy" {
		t.Errorf("clean = %q, want every direction control gone", got)
	}
}

func TestClip(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"ααααα", 3, "αα…"},
		{"hello", 0, ""},
	} {
		if got := clip(tc.s, tc.n); got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

func TestTruncateBytesKeepsRunesWhole(t *testing.T) {
	if got := truncateBytes("aαb", 2); got != "a" {
		t.Errorf("truncateBytes = %q, want the half rune dropped", got)
	}
	if got := truncateBytes("abc", 10); got != "abc" {
		t.Errorf("truncateBytes = %q, want it untouched", got)
	}
}
