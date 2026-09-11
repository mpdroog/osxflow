package main

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/launch"
)

// The -windows table names the three expected reasons a window has no
// executable, and shows anything else in full.
func TestExeProblem(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{launch.ErrNoPID, "no pid"},
		{fmt.Errorf("reading /proc/1/exe: %w", fs.ErrNotExist), "process has exited"},
		{fmt.Errorf("reading /proc/1/exe: %w", fs.ErrPermission), "another user's process"},
		{errors.New("reading /proc/1/exe: input/output error"), "reading /proc/1/exe: input/output error"},
	}
	for _, tc := range tests {
		if got := exeProblem(tc.err); got != tc.want {
			t.Errorf("exeProblem(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// The real thing, for the case that needs no setup.
	if _, err := launch.ProcExe(0); exeProblem(err) != "no pid" {
		t.Errorf("exeProblem(ProcExe(0)) = %q", exeProblem(err))
	}
}

func TestFindApp(t *testing.T) {
	apps := []desktop.App{
		{Name: "Files", ID: "files.desktop"},
		{Name: "Firefox Web Browser", ID: "firefox.desktop"},
		{Name: "File Manager", ID: "thunar.desktop"},
		{Name: "Ghostty", ID: "ghostty.desktop"},
	}
	tests := []struct {
		query string
		want  string
	}{
		{"Ghostty", "Ghostty"},
		{"ghostty", "Ghostty"}, // exact match is case-insensitive
		{"Files", "Files"},     // exact beats the prefix match on "File Manager"
		{"File", "Files"},      // prefix: first in order wins
		{"fire", "Firefox Web Browser"},
		{"Browser", "Firefox Web Browser"}, // substring, no prefix matches
		{"host", "Ghostty"},                // substring only
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := findApp(apps, tc.query)
			if !ok {
				t.Fatalf("findApp(%q) found nothing, want %q", tc.query, tc.want)
			}
			if got.Name != tc.want {
				t.Errorf("findApp(%q) = %q, want %q", tc.query, got.Name, tc.want)
			}
		})
	}

	if _, ok := findApp(apps, "nothing here"); ok {
		t.Error("findApp matched a query that should not match")
	}
	if _, ok := findApp(nil, "anything"); ok {
		t.Error("findApp matched against an empty list")
	}
}
