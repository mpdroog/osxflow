package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/menu"
)

type fakeActions struct{ calls []string }

func (f *fakeActions) about()             { f.calls = append(f.calls, "about") }
func (f *fakeActions) run(argv ...string) { f.calls = append(f.calls, "run "+strings.Join(argv, " ")) }
func (f *fakeActions) tile(on bool)       { f.calls = append(f.calls, fmt.Sprint("tile ", on)) }

func (f *fakeActions) click(r *menu.Row) string {
	if r.Click == nil {
		return "inert"
	}
	f.calls = nil
	r.Click()
	return strings.Join(f.calls, "; ")
}

func labels(rows []menu.Row) string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].Label
		if rows[i].Kind == menu.Separator {
			out[i] = "--"
		}
	}
	return strings.Join(out, "|")
}

func TestMainRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wayland bool
		want    string
		clicks  map[int]string
	}{
		{
			name:    "labwc",
			wayland: true,
			want:    "About This Linux|--|Task Manager…|Tile Windows|--|Sleep|Restart|Shut Down|--|Lock Screen|Log Out MP Droog",
			clicks: map[int]string{
				0:  "about",
				2:  "run foot -T Task Manager top",
				3:  "tile true",
				5:  "run sh -c swaylock -f -c 000000 && doas /usr/local/sbin/osxflow-suspend",
				6:  "run doas /sbin/reboot",
				7:  "run doas /sbin/poweroff",
				9:  "run sh -c swaylock -f -c 000000",
				10: "run labwc --exit",
			},
		},
		{
			name:    "xfce",
			wayland: false,
			want:    "About This Linux|--|System Settings…|Task Manager…|--|Sleep|Restart|Shut Down|--|Lock Screen|Log Out MP Droog",
			clicks: map[int]string{
				0:  "about",
				2:  "run xfce4-settings-manager",
				3:  "run xfce4-taskmanager",
				5:  "run xfce4-session-logout --suspend",
				6:  "run xfce4-session-logout --reboot",
				7:  "run xfce4-session-logout --halt",
				9:  "run xflock4",
				10: "run xfce4-session-logout --logout",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeActions{}
			rows := mainRows("MP Droog", tc.wayland, false, f)
			if got := labels(rows); got != tc.want {
				t.Fatalf("rows:\n got %s\nwant %s", got, tc.want)
			}
			for i, want := range tc.clicks {
				if got := f.click(&rows[i]); got != want {
					t.Errorf("%q asked for %q, want %q", rows[i].Label, got, want)
				}
			}
			for i := range rows {
				if rows[i].KeepOpen {
					t.Errorf("%q keeps the menu open", rows[i].Label)
				}
			}
			anon := mainRows("", tc.wayland, false, f)
			if got := anon[len(anon)-1].Label; got != "Log Out" {
				t.Errorf("log out without a name = %q", got)
			}
		})
	}
}

func TestTileRowShowsAndFlipsTheState(t *testing.T) {
	f := &fakeActions{}
	rows := mainRows("", true, true, f)
	r := &rows[3]
	if r.Label != "Tile Windows" || r.Kind != menu.Toggle || !r.On {
		t.Fatalf("row 3 = %+v, want Tile Windows switched on", r)
	}
	if got := f.click(r); got != "tile false" {
		t.Errorf("clicking it on asked for %q, want tile false", got)
	}
}

func TestAboutRows(t *testing.T) {
	full := about{model: "MacBookPro14,1", system: "Linux Mint 22.1", kernel: "7.0.0-30-generic", memory: "16 GB", uptime: "3 h 12 min"}
	rows := aboutRows(&full)
	if got := labels(rows); got != "About This Linux|Linux Mint 22.1|Kernel 7.0.0-30-generic|Memory 16 GB|Up 3 h 12 min|--|OK" {
		t.Errorf("rows: %s", got)
	}
	if rows[0].Detail != "MacBookPro14,1" {
		t.Errorf("header detail = %q", rows[0].Detail)
	}
	partial := about{memory: "8 GB"}
	if got := labels(aboutRows(&partial)); got != "About This Linux|Memory 8 GB|--|OK" {
		t.Errorf("rows with only memory: %s", got)
	}
}
