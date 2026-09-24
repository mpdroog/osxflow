package dbusmenu

import (
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/dbustest"
)

// wireEntry is how an entry goes on the wire: godbus sends a Go struct
// as a D-Bus struct, where a []any would go as an array of variants.
type wireEntry struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

func node(id int32, props map[string]dbus.Variant, children ...wireEntry) wireEntry {
	vs := make([]dbus.Variant, 0, len(children))
	for _, c := range children {
		vs = append(vs, dbus.MakeVariant(c))
	}
	if props == nil {
		props = map[string]dbus.Variant{}
	}
	return wireEntry{ID: id, Props: props, Children: vs}
}

// toAny is the form a received entry takes: godbus hands a struct over as
// []any, and each child variant holds the same.
func toAny(e *wireEntry) []any {
	kids := make([]dbus.Variant, 0, len(e.Children))
	for _, c := range e.Children {
		if child, ok := c.Value().(wireEntry); ok {
			kids = append(kids, dbus.MakeVariant(toAny(&child)))
		}
	}
	return []any{e.ID, e.Props, kids}
}

func label(s string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"label": dbus.MakeVariant(s)}
}

// ezhours' menu, as its fyne systray sends it.
func ezhours() wireEntry {
	return node(0, map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")},
		node(1, map[string]dbus.Variant{
			"label": dbus.MakeVariant("Start Timer"), "enabled": dbus.MakeVariant(true),
			"toggle-type": dbus.MakeVariant(""), "toggle-state": dbus.MakeVariant(int32(0)),
		}),
		node(2, map[string]dbus.Variant{"type": dbus.MakeVariant("separator")}),
		node(3, map[string]dbus.Variant{"label": dbus.MakeVariant("_Quit"), "enabled": dbus.MakeVariant(false)}),
		node(4, map[string]dbus.Variant{
			"label": dbus.MakeVariant("Dark"), "toggle-type": dbus.MakeVariant("checkmark"),
			"toggle-state": dbus.MakeVariant(int32(1)), "visible": dbus.MakeVariant(false),
		}, node(5, label("Sub"))),
	)
}

func TestDecode(t *testing.T) {
	tree := ezhours()
	root, err := Decode(toAny(&tree))
	if err != nil {
		t.Fatal(err)
	}
	if root.ID != 0 || len(root.Children) != 4 {
		t.Fatalf("root = %+v", root)
	}
	c := root.Children
	if c[0].Label != "Start Timer" || !c[0].Enabled || !c[0].Visible || c[0].Toggle != "" {
		t.Errorf("entry 1 = %+v", c[0])
	}
	if !c[1].Separator {
		t.Errorf("entry 2 = %+v", c[1])
	}
	if c[2].Label != "Quit" || c[2].Enabled {
		t.Errorf("entry 3 = %+v", c[2])
	}
	if c[3].Toggle != "checkmark" || !c[3].Checked || c[3].Visible || len(c[3].Children) != 1 || c[3].Children[0].Label != "Sub" {
		t.Errorf("entry 4 = %+v", c[3])
	}
}

func TestDecodeRejects(t *testing.T) {
	deepTree := node(0, nil)
	for range maxDepth + 2 {
		deepTree = node(0, nil, deepTree)
	}
	deep := toAny(&deepTree)
	for name, v := range map[string]any{
		"not a struct":  "x",
		"short":         []any{int32(1), map[string]dbus.Variant{}},
		"string id":     []any{"1", map[string]dbus.Variant{}, []dbus.Variant{}},
		"bad props":     []any{int32(1), map[string]string{}, []dbus.Variant{}},
		"bad children":  []any{int32(1), map[string]dbus.Variant{}, []string{}},
		"bad child":     []any{int32(1), map[string]dbus.Variant{}, []dbus.Variant{dbus.MakeVariant("x")}},
		"nested deeply": deep,
	} {
		if _, err := Decode(v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"_File":     "File",
		"Save __As": "Save _As",
		"plain":     "plain",
		"trailing_": "trailing",
		"a_b_c":     "abc",
		"___x":      "_x",
		"":          "",
		"Ünïcödé_ä": "Ünïcödéä",
	} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzLabel(f *testing.F) {
	for _, s := range []string{"_File", "__", "a_b", "", "é_"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Label(s)
		if len(got) > len(s) {
			t.Fatalf("Label(%q) = %q grew", s, got)
		}
		if strings.Count(got, "_") > strings.Count(s, "_")/2 {
			t.Fatalf("Label(%q) = %q kept too many underscores", s, got)
		}
		if !strings.Contains(s, "_") && got != s {
			t.Fatalf("Label(%q) = %q changed a label with no markers", s, got)
		}
	})
}

// fakeMenu is an item's exported menu.
type fakeMenu struct {
	shown   chan int32
	clicked chan int32
}

func (m *fakeMenu) AboutToShow(id int32) (bool, *dbus.Error) {
	m.shown <- id
	return false, nil
}

func (m *fakeMenu) GetLayout(parent, depth int32, props []string) (uint32, wireEntry, *dbus.Error) {
	return 7, ezhours(), nil
}

func (m *fakeMenu) Event(id int32, event string, data dbus.Variant, ts uint32) *dbus.Error {
	if event == "clicked" {
		m.clicked <- id
	}
	return nil
}

func TestClient(t *testing.T) {
	addr := dbustest.Start(t)
	server := dbustest.Conn(t, addr)
	m := &fakeMenu{shown: make(chan int32, 1), clicked: make(chan int32, 1)}
	const path = dbus.ObjectPath("/StatusNotifierItem/menu")
	if err := server.Export(m, path, Interface); err != nil {
		t.Fatal(err)
	}
	c := New(dbustest.Conn(t, addr), server.Names()[0], path)
	ctx := t.Context()
	if err := c.AboutToShow(ctx); err != nil {
		t.Fatal(err)
	}
	if id := <-m.shown; id != 0 {
		t.Errorf("AboutToShow(%d), want the root", id)
	}
	root, err := c.Layout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 4 || root.Children[0].Label != "Start Timer" {
		t.Errorf("Layout = %+v", root)
	}
	if err := c.Clicked(ctx, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-m.clicked:
		if id != 3 {
			t.Errorf("clicked %d, want 3", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no click arrived")
	}

	// A menu without AboutToShow is still read.
	if err := server.Export(layoutOnly{}, "/bare", Interface); err != nil {
		t.Fatal(err)
	}
	bare := New(dbustest.Conn(t, addr), server.Names()[0], "/bare")
	if err := bare.AboutToShow(ctx); err != nil {
		t.Errorf("AboutToShow on a menu without it: %v", err)
	}
	if _, err := bare.Layout(ctx); err != nil {
		t.Errorf("Layout of a menu without AboutToShow: %v", err)
	}
	missing := New(dbustest.Conn(t, addr), server.Names()[0], "/missing")
	if _, err := missing.Layout(ctx); err == nil {
		t.Error("Layout of a missing menu succeeded")
	}
}

type layoutOnly struct{}

func (layoutOnly) GetLayout(parent, depth int32, props []string) (uint32, wireEntry, *dbus.Error) {
	return 1, node(0, nil), nil
}
