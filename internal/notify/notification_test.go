package notify

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const fallback = 5 * time.Second

func parse(req *Request) Notification {
	if req.Hints == nil {
		req.Hints = map[string]any{}
	}
	return Parse(req, fallback)
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
