// Package notify is the server side of the freedesktop.org Desktop
// Notifications Specification, version 1.2, without a display.
//
// It covers everything a notification daemon does except drawing: taking
// calls on the session bus, turning their loosely typed arguments into
// something a renderer can trust, and the bookkeeping behind them -- which
// notifications are on screen, which replace which, when each one expires
// and why each one closed. cmd/notifyd draws what it is told to, which is
// what lets all of this be tested without an X server.
package notify

import (
	"image"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Urgency is the "urgency" hint.
type Urgency uint8

// The three levels the specification defines.
const (
	Low      Urgency = 0
	Normal   Urgency = 1
	Critical Urgency = 2
)

// DefaultAction is the action key the specification reserves for clicking
// the notification itself rather than one of its buttons.
const DefaultAction = "default"

// Action is one action the sender offered.
type Action struct {
	Key   string
	Label string
}

// Notification is one notification, cleaned up and ready to draw.
//
// Every string in it is valid UTF-8 with no control characters (bar the
// newlines in Body) and a bounded length, so a renderer can lay it out
// without second-guessing what a client sent.
type Notification struct {
	// ID is assigned by the Queue, never by the sender.
	ID uint32

	AppName string
	Summary string

	// Body is plain text: whatever markup the sender used is gone.
	Body string

	Icon Icon

	// Actions are in the order the sender gave them, the default one
	// included.
	Actions []Action

	Urgency Urgency

	// Timeout is how long the notification stays up once it is on screen.
	// Zero means until somebody closes it.
	Timeout time.Duration

	// Resident notifications stay up after one of their actions is
	// invoked; everything else closes.
	Resident bool

	// DesktopEntry is the sender's desktop file id without ".desktop",
	// when it gave one. It is the most reliable way to find its icon.
	DesktopEntry string

	// Category is the sender's classification ("email.arrived",
	// "device.added"). Carried for completeness; nothing draws it.
	Category string

	// Sync groups notifications that should replace each other in place
	// rather than stack up: volume and brightness popups send one per
	// keypress. Empty for everything else.
	Sync string

	// Value is a 0-100 level to draw as a bar, or -1 for none. Volume and
	// brightness popups send it.
	Value int
}

// HasAction reports whether the sender offered an action with this key.
func (n *Notification) HasAction(key string) bool {
	for i := range n.Actions {
		if n.Actions[i].Key == key {
			return true
		}
	}
	return false
}

// Buttons returns the actions to draw as buttons: all of them except the
// default, which belongs to a click on the notification itself.
func (n *Notification) Buttons() []Action {
	var out []Action
	for _, a := range n.Actions {
		if a.Key == DefaultAction {
			continue
		}
		if a.Label == "" {
			a.Label = a.Key
		}
		out = append(out, a)
	}
	return out
}

// Title is the line drawn in bold: the summary, or for a notification
// that has none, the name of the application that sent it.
func (n *Notification) Title() string {
	if n.Summary != "" {
		return n.Summary
	}
	return n.AppName
}

// Icon is every image the sender pointed at, so the renderer can take the
// best one it is able to draw. The specification ranks them in this order.
type Icon struct {
	// Data is the image-data hint, decoded: pixels sent inline.
	Data *image.RGBA

	// Path is a local image file, from image-path or app_icon.
	Path string

	// Name is an icon theme name, from image-path or app_icon.
	Name string
}

// Request is a Notify call's arguments, as they arrived.
type Request struct {
	AppName       string
	ReplacesID    uint32
	AppIcon       string
	Summary       string
	Body          string
	Actions       []string
	Hints         map[string]any
	ExpireTimeout int32
}

// Limits on what a sender can make the daemon lay out. A banner shows one
// line of title and a few of body, so these sit far above anything that
// could be drawn. They exist so that a client sending a megabyte of body
// costs one copy rather than a megabyte of text measurement per repaint.
const (
	maxNameRunes    = 100
	maxSummaryRunes = 300
	maxBodyRunes    = 2000
	maxLabelRunes   = 60
	maxActions      = 8
	maxKeyBytes     = 1024

	// maxRawBody bounds the body before its markup is removed, which is
	// the one step whose cost grows with the input rather than the output.
	maxRawBody = 64 << 10
)

// Parse turns a Notify call into a Notification. It never fails: anything
// malformed is dropped, and what is left is still worth showing.
//
// defaultTimeout is what an expire_timeout of -1 means, which is the
// server's choice.
func Parse(req *Request, defaultTimeout time.Duration) Notification {
	h := req.Hints
	n := Notification{
		AppName:      clip(singleLine(clean(req.AppName)), maxNameRunes),
		Summary:      clip(singleLine(clean(req.Summary)), maxSummaryRunes),
		Body:         clip(StripMarkup(truncateBytes(req.Body, maxRawBody)), maxBodyRunes),
		Icon:         parseIcon(req.AppIcon, h),
		Actions:      parseActions(req.Actions),
		Urgency:      Normal,
		Resident:     boolHint(h, "resident"),
		DesktopEntry: strings.TrimSuffix(clip(singleLine(stringHint(h, "desktop-entry")), maxNameRunes), ".desktop"),
		Category:     clip(singleLine(stringHint(h, "category")), maxNameRunes),
		Value:        -1,
	}
	n.Sync = syncKey(h, n.AppName)

	if u, ok := intHint(h, "urgency"); ok {
		switch u {
		case 0:
			n.Urgency = Low
		case 2:
			n.Urgency = Critical
		}
	}
	if v, ok := intHint(h, "value"); ok {
		n.Value = int(min(max(v, 0), 100))
	}
	n.Timeout = timeoutFor(req.ExpireTimeout, n.Urgency, defaultTimeout)
	return n
}

// timeoutFor applies the specification's rules for expire_timeout:
// positive is milliseconds, zero is never, and -1 is the server's choice.
//
// Part of the server's choice is that a critical notification with no
// timeout of its own stays until it is dismissed, as the specification
// recommends. One that asked for a timeout gets it.
func timeoutFor(ms int32, u Urgency, fallback time.Duration) time.Duration {
	switch {
	case ms > 0:
		return time.Duration(ms) * time.Millisecond
	case ms == 0:
		return 0
	case u == Critical:
		return 0
	}
	return fallback
}

// parseActions pairs up the flat [key, label, key, label, ...] list the
// specification uses.
func parseActions(raw []string) []Action {
	if len(raw) < 2 {
		return nil
	}
	out := make([]Action, 0, min(len(raw)/2, maxActions))
	for i := 0; i+1 < len(raw) && len(out) < maxActions; i += 2 {
		// The key is handed back verbatim in ActionInvoked, so it is never
		// altered; an unusable one is dropped instead.
		key := raw[i]
		if key == "" || len(key) > maxKeyBytes || !utf8.ValidString(key) {
			continue
		}
		out = append(out, Action{Key: key, Label: clip(singleLine(clean(raw[i+1])), maxLabelRunes)})
	}
	return out
}

// syncKey reads the synchronous hint, under any of the three names it has
// gone by.
//
// Senders often give it an empty value, meaning only "replace my previous
// one", so the application's name stands in for the group then. The prefix
// keeps the two kinds of key from ever colliding.
func syncKey(h map[string]any, appName string) string {
	for _, key := range [...]string{"x-canonical-private-synchronous", "synchronous", "private-synchronous"} {
		v, ok := h[key].(string)
		if !ok {
			continue
		}
		if v = clip(clean(v), maxNameRunes); v != "" {
			return "hint:" + v
		}
		return "app:" + appName
	}
	return ""
}

// parseIcon collects the images a notification names, best first.
func parseIcon(appIcon string, h map[string]any) Icon {
	var ic Icon
	for _, key := range [...]string{"image-data", "image_data", "icon_data"} {
		v, ok := h[key]
		if !ok {
			continue
		}
		if img, err := DecodeImageData(v); err == nil {
			ic.Data = img
			break
		}
	}
	for _, s := range [...]string{stringHint(h, "image-path"), stringHint(h, "image_path"), clean(appIcon)} {
		ic.add(s)
	}
	return ic
}

// add files one image reference as a path or a theme name, keeping
// whichever of each came first.
func (ic *Icon) add(s string) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
	case strings.HasPrefix(s, "file://"):
		if u, err := url.Parse(s); err == nil && strings.HasPrefix(u.Path, "/") && ic.Path == "" {
			ic.Path = filepath.Clean(u.Path)
		}
	case strings.HasPrefix(s, "/"):
		if ic.Path == "" {
			ic.Path = filepath.Clean(s)
		}
	case strings.ContainsAny(s, "/:"):
		// A relative path or some other URI. Relative to what is anybody's
		// guess, so neither is usable.
	default:
		if ic.Name == "" {
			ic.Name = clip(s, maxNameRunes)
		}
	}
}

func stringHint(h map[string]any, key string) string {
	s, ok := h[key].(string)
	if !ok {
		return ""
	}
	return clean(s)
}

func boolHint(h map[string]any, key string) bool {
	b, ok := h[key].(bool)
	return ok && b
}

// intHint reads an integer hint of any width. The specification says
// "urgency" is a byte, and most senders agree, but some send an int32 and
// dropping their urgency over it would help nobody.
func intHint(h map[string]any, key string) (int64, bool) {
	switch v := h[key].(type) {
	case uint8:
		return int64(v), true
	case int16:
		return int64(v), true
	case uint16:
		return int64(v), true
	case int32:
		return int64(v), true
	case uint32:
		return int64(v), true
	case int64:
		return v, true
	case uint64:
		if v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	}
	return 0, false
}

// clean makes a sender's string safe to draw: valid UTF-8, no control
// characters other than newlines, tabs turned to spaces, and no
// bidirectional overrides. Those last are the reason this is not simply a
// control-character filter: a notification containing U+202E displays the
// text after it reversed, which is a way to make it appear to say
// something it does not.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	if !strings.ContainsFunc(s, unwanted) {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unwanted(r):
			return -1
		}
		return r
	}, s)
}

func unwanted(r rune) bool {
	if r == '\n' {
		return false
	}
	return unicode.IsControl(r) || isBidiControl(r)
}

func isBidiControl(r rune) bool {
	switch {
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	case r == '\u200e', r == '\u200f', r == '\u061c':
		return true
	}
	return false
}

func singleLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// clip shortens s to at most n runes, marking the cut with an ellipsis.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n-1 {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
