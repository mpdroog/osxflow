package main

import (
	"slices"
	"testing"

	"github.com/mpdroog/osxflow/internal/dbusmenu"
	"github.com/mpdroog/osxflow/internal/menu"
)

func rowKinds(rows []menu.Row) []menu.Kind {
	out := make([]menu.Kind, len(rows))
	for i := range rows {
		out[i] = rows[i].Kind
	}
	return out
}

func TestMenuRows(t *testing.T) {
	root := dbusmenu.Entry{Children: []dbusmenu.Entry{
		{Separator: true, Visible: true},
		{ID: 1, Label: "Start Timer", Enabled: true, Visible: true},
		{ID: 2, Label: "Hidden", Enabled: true, Visible: false},
		{Separator: true, Visible: true},
		{Separator: true, Visible: true},
		{ID: 3, Label: "Disabled", Enabled: false, Visible: true},
		{ID: 4, Label: "Dark mode", Enabled: true, Visible: true, Toggle: "checkmark", Checked: true},
		{ID: 5, Label: "More", Enabled: true, Visible: true, Children: []dbusmenu.Entry{
			{ID: 6, Label: "Inner", Enabled: true, Visible: true},
		}},
		{Separator: true, Visible: true},
	}}
	var clicked []int32
	rows := menuRows(&root, func(id int32) { clicked = append(clicked, id) })
	want := []menu.Kind{menu.Action, menu.Separator, menu.Note, menu.Toggle, menu.Separator, menu.Section, menu.Action}
	if !slices.Equal(rowKinds(rows), want) {
		t.Fatalf("kinds = %v, want %v", rowKinds(rows), want)
	}
	if rows[0].Label != "Start Timer" || rows[2].Click != nil || !rows[3].On || rows[5].Label != "More" {
		t.Errorf("rows = %+v", rows)
	}
	rows[0].Click()
	rows[3].Click()
	rows[6].Click()
	if !slices.Equal(clicked, []int32{1, 4, 6}) {
		t.Errorf("clicked %v", clicked)
	}
}

func TestMenuRowsEmpty(t *testing.T) {
	root := dbusmenu.Entry{Children: []dbusmenu.Entry{{Separator: true, Visible: true}}}
	if rows := menuRows(&root, func(int32) {}); len(rows) != 0 {
		t.Errorf("rows = %+v", rows)
	}
}
