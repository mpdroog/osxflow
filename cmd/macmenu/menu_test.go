package main

import (
	"strings"
	"testing"

	"github.com/mpdroog/osxflow/internal/menu"
)

type fakeActions struct{ calls []string }

func (f *fakeActions) about()             { f.calls = append(f.calls, "about") }
func (f *fakeActions) run(argv ...string) { f.calls = append(f.calls, "run "+strings.Join(argv, " ")) }

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
	f := &fakeActions{}
	rows := mainRows("MP Droog", f)
	want := "About This Linux|--|Task Manager…|--|Sleep|Restart|Shut Down|--|Lock Screen|Log Out MP Droog"
	if got := labels(rows); got != want {
		t.Fatalf("rows:\n got %s\nwant %s", got, want)
	}
	for i, want := range map[int]string{
		0: "about",
		2: "run foot -T Task Manager top",
		4: "run sh -c swaylock -f -c 000000 && doas /usr/local/sbin/osxflow-suspend",
		5: "run doas /sbin/reboot",
		6: "run doas /sbin/poweroff",
		8: "run sh -c swaylock -f -c 000000",
		9: "run labwc --exit",
	} {
		if got := f.click(&rows[i]); got != want {
			t.Errorf("%q asked for %q, want %q", rows[i].Label, got, want)
		}
	}
	for i := range rows {
		if rows[i].KeepOpen {
			t.Errorf("%q keeps the menu open", rows[i].Label)
		}
	}
	if got := mainRows("", f)[9].Label; got != "Log Out" {
		t.Errorf("log out without a name = %q", got)
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
