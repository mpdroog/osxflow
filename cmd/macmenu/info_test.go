package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseOSRelease(t *testing.T) {
	in := `# comment
NAME="Linux Mint"
VERSION="22.1 (Xia)"
ID=linuxmint
PRETTY_NAME='Linux Mint 22.1'
BROKEN
=novalue
EMPTY=
QUOTE_ONE="
`
	got := parseOSRelease([]byte(in))
	for k, want := range map[string]string{
		"NAME":        "Linux Mint",
		"VERSION":     "22.1 (Xia)",
		"ID":          "linuxmint",
		"PRETTY_NAME": "Linux Mint 22.1",
		"EMPTY":       "",
		"QUOTE_ONE":   `"`,
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if _, ok := got["BROKEN"]; ok {
		t.Error("a line without = was read")
	}
	if _, ok := got[""]; ok {
		t.Error("a line with an empty key was read")
	}
}

func FuzzParseOSRelease(f *testing.F) {
	f.Add([]byte("NAME=\"Linux Mint\"\nPRETTY_NAME='x'\n"))
	f.Add([]byte("=\n\"\n'"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for k, v := range parseOSRelease(b) {
			if k == "" {
				t.Fatalf("empty key with value %q", v)
			}
			if strings.ContainsAny(k, "\n") || strings.Contains(v, "\n") {
				t.Fatalf("a newline in %q=%q", k, v)
			}
		}
	})
}

func TestParseMemTotal(t *testing.T) {
	kB, err := parseMemTotal([]byte("MemFree: 1 kB\nMemTotal:       16318048 kB\n"))
	if err != nil || kB != 16318048 {
		t.Errorf("parseMemTotal = %d, %v", kB, err)
	}
	if _, err := parseMemTotal([]byte("MemFree: 1 kB\n")); !errors.Is(err, errNoMemTotal) {
		t.Errorf("without MemTotal: %v", err)
	}
	if _, err := parseMemTotal([]byte("MemTotal: lots kB\n")); err == nil {
		t.Error("a non-number MemTotal parsed")
	}
}

func FuzzParseMeminfo(f *testing.F) {
	f.Add([]byte("MemTotal:       16318048 kB\n"))
	f.Add([]byte("MemTotal: -1\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		kB, err := parseMemTotal(b)
		if err != nil && kB != 0 {
			t.Fatalf("error %v with a value %d", err, kB)
		}
		if err == nil && formatMemory(kB) == "" {
			t.Fatalf("no text for %d kB", kB)
		}
	})
}

func TestParseUptime(t *testing.T) {
	d, err := parseUptime([]byte("11520.37 45000.12\n"))
	if err != nil || d.Round(time.Second) != 11520*time.Second {
		t.Errorf("parseUptime = %v, %v", d, err)
	}
	for _, bad := range []string{"", "   ", "soon 1", "-5 0", "NaN 0", "1e30 0"} {
		if _, err := parseUptime([]byte(bad)); err == nil {
			t.Errorf("parseUptime(%q) succeeded", bad)
		}
	}
}

func FuzzParseUptime(f *testing.F) {
	f.Add([]byte("11520.37 45000.12\n"))
	f.Add([]byte("Inf 0"))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := parseUptime(b)
		if err != nil {
			return
		}
		if d < 0 {
			t.Fatalf("negative uptime %v from %q", d, b)
		}
		if formatUptime(d) == "" {
			t.Fatalf("no text for %v", d)
		}
	})
}

func TestFormat(t *testing.T) {
	for _, tc := range []struct {
		kB   uint64
		want string
	}{{16318048, "16 GB"}, {8 * 1024 * 1024, "8 GB"}, {512 * 1024, "512 MB"}, {0, "0 MB"}} {
		if got := formatMemory(tc.kB); got != tc.want {
			t.Errorf("formatMemory(%d) = %q, want %q", tc.kB, got, tc.want)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 min"},
		{12 * time.Minute, "12 min"},
		{3*time.Hour + 12*time.Minute, "3 h 12 min"},
		{24*time.Hour + 5*time.Hour, "1 day 5 h"},
		{50 * time.Hour, "2 days 2 h"},
	} {
		if got := formatUptime(tc.d); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
