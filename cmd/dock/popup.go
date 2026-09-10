package main

// The stack popup: the little panel that opens above Downloads or Trash
// showing the five most recent things in it, with a way to open the folder
// itself.

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/jezek/xgb/xproto"

	"github.com/mpdroog/osxflow/internal/dock"
	"github.com/mpdroog/osxflow/internal/geom"
	"github.com/mpdroog/osxflow/internal/paint"
	"github.com/mpdroog/osxflow/internal/stack"
	"github.com/mpdroog/osxflow/internal/text"
	"github.com/mpdroog/osxflow/internal/xsurface"
)

// stackEntries is how many items a stack shows. Five is what was asked
// for, and it is about right: a stack is a glance at what just arrived,
// not a file manager.
const stackEntries = 5

// popup is one open stack.
type popup struct {
	win      xproto.Window
	surf     *xsurface.Surface
	colormap xproto.Colormap

	rows  []popupRow
	hover int

	w, h int

	// grabbed records whether the pointer grab succeeded, so the popup
	// does not try to release one it never took.
	grabbed bool
}

// popupRow is one line: a file, or the action at the bottom.
type popupRow struct {
	label string

	// target is what to open. Empty makes the row inert, which is what the
	// "Empty" placeholder is.
	target string

	// action marks the bottom row, which is drawn separated from the list
	// above it.
	action bool

	// dim marks a row that is a message rather than a file.
	dim bool
}

// openStack builds and shows the popup for a stack item.
func (d *dockApp) openStack(i int) error {
	st, ok := d.stacks[i]
	if !ok {
		return fmt.Errorf("item %d is not a stack", i)
	}
	if d.popup != nil {
		d.popup.close(d)
	}

	rows, err := stackRows(st)
	if err != nil {
		return err
	}
	p := &popup{rows: rows, hover: -1}
	if err := p.create(d, d.places[i].CentreX); err != nil {
		return err
	}
	d.popup = p
	return p.paint(d)
}

// stackRows lists a stack's contents as rows.
func stackRows(st dock.Stack) ([]popupRow, error) {
	var (
		entries []stack.Entry
		err     error
		action  string
		target  string
	)
	switch st.Kind {
	case dock.StackDownloads:
		entries, err = stack.Recent(st.Dir, stackEntries)
		action, target = "Open Downloads", st.Dir
	case dock.StackTrash:
		entries, err = stack.TrashEntries(st.Dir, stackEntries)
		action, target = "Open Trash", trashURI
	case dock.NotAStack:
		return nil, fmt.Errorf("not a stack")
	}
	if err != nil {
		// A missing or unreadable folder is worth showing rather than
		// failing on: the action row still opens it, which is how the user
		// finds out what is wrong.
		entries = nil
	}

	rows := make([]popupRow, 0, len(entries)+2)
	for _, e := range entries {
		label := e.Name
		if e.IsDir {
			label += "/"
		}
		rows = append(rows, popupRow{label: shortLabel(label), target: e.Path})
	}
	if len(rows) == 0 {
		rows = append(rows, popupRow{label: "Empty", dim: true})
	}
	rows = append(rows, popupRow{label: action, target: target, action: true})
	return rows, nil
}

// trashURI is what a file manager wants in order to show the trash with
// its restore action, rather than showing the directory the files happen
// to be stored in.
const trashURI = "trash:///"

// create makes the popup window above the dock.
func (p *popup) create(d *dockApp, centreX float64) error {
	p.w, p.h = p.measure(d)

	// Centred over the stack icon, clamped to the screen, and sitting just
	// above the dock's plate.
	x := d.winX + int(centreX) - p.w/2
	if x < 0 {
		x = 0
	}
	if x+p.w > int(d.screen.WidthInPixels) {
		x = int(d.screen.WidthInPixels) - p.w
	}
	y := int(d.screen.HeightInPixels) - d.th.winH + int(d.th.panelTop()) - int(d.th.popupGap) - p.h

	win, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a popup window id: %w", err)
	}
	p.win = win

	cmap, err := xproto.NewColormapId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a popup colormap id: %w", err)
	}
	if cmapErr := xproto.CreateColormapChecked(d.conn, xproto.ColormapAllocNone,
		cmap, d.screen.Root, d.visual.id).Check(); cmapErr != nil {
		return fmt.Errorf("creating the popup colormap: %w", cmapErr)
	}
	p.colormap = cmap

	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	values := []uint32{
		0x00000000,
		0x00000000,
		1,
		uint32(xproto.EventMaskExposure | xproto.EventMaskButtonPress | xproto.EventMaskPointerMotion),
		uint32(cmap),
	}
	err = xproto.CreateWindowChecked(d.conn, d.visual.depth, win, d.screen.Root,
		geom.I16(x), geom.I16(y), geom.U16(p.w), geom.U16(p.h), 0,
		xproto.WindowClassInputOutput, d.visual.id, mask, values).Check()
	if err != nil {
		return fmt.Errorf("creating the popup window: %w", err)
	}
	d.nameWindow(win, "dock-stack")

	p.surf, err = xsurface.New(d.conn, win, d.visual.depth, p.w, p.h)
	if err != nil {
		return err
	}
	xproto.MapWindow(d.conn, win)
	d.raise(win)
	p.grab(d)
	return nil
}

// measure sizes the popup to its longest row.
func (p *popup) measure(d *dockApp) (w, h int) {
	widest := 0.0
	for i := range p.rows {
		if got := float64(text.Width(d.faces.popup, p.rows[i].label)); got > widest {
			widest = got
		}
	}
	width := widest + 2*d.th.popupPadX
	if width < d.th.popupMinW {
		width = d.th.popupMinW
	}
	if width > d.th.popupMaxW {
		width = d.th.popupMaxW
	}
	height := 2*d.th.popupPadY + float64(len(p.rows))*d.th.popupRowH + d.th.popupSepH
	return int(width + 0.5), int(height + 0.5)
}

// grab takes the pointer so that a click anywhere -- including outside the
// popup -- is delivered here and can dismiss it.
//
// owner_events is false, which is what makes outside clicks arrive at all:
// with it true they would go to whichever window is under the cursor and
// never reach us. Failure is not fatal, and costs only click-away
// dismissal.
func (p *popup) grab(d *dockApp) {
	reply, err := xproto.GrabPointer(d.conn, false, p.win,
		uint16(xproto.EventMaskButtonPress|xproto.EventMaskPointerMotion),
		xproto.GrabModeAsync, xproto.GrabModeAsync,
		xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
	p.grabbed = err == nil && reply != nil && reply.Status == xproto.GrabStatusSuccess
}

// rowAt maps a y coordinate to a row index, or -1.
func (p *popup) rowAt(d *dockApp, x, y int) int {
	if x < 0 || x >= p.w || y < 0 || y >= p.h {
		return -1
	}
	top := d.th.popupPadY
	for i := range p.rows {
		if p.rows[i].action {
			top += d.th.popupSepH
		}
		if float64(y) >= top && float64(y) < top+d.th.popupRowH {
			return i
		}
		top += d.th.popupRowH
	}
	return -1
}

// motion updates the highlighted row.
func (p *popup) motion(d *dockApp, x, y int) (show, leaving bool, err error) {
	row := p.rowAt(d, x, y)
	if row == p.hover {
		return true, false, nil
	}
	p.hover = row
	return true, false, p.paint(d)
}

// click activates a row, or dismisses the popup when the click landed
// outside it.
func (p *popup) click(d *dockApp, e xproto.ButtonPressEvent) (show, leaving bool, err error) {
	x, y := int(e.EventX), int(e.EventY)
	row := p.rowAt(d, x, y)
	if row < 0 {
		p.close(d)
		d.popup = nil
		// A click outside the popup was aimed at whatever is under it, not
		// at the dock, so the dock should get out of the way too.
		return false, true, d.paint()
	}
	target := p.rows[row].target
	p.close(d)
	d.popup = nil
	if target == "" {
		return true, false, d.paint()
	}
	if openErr := openTarget(target); openErr != nil {
		fmt.Fprintln(os.Stderr, "dock:", openErr)
	}
	return false, true, d.paint()
}

// paint draws the popup.
func (p *popup) paint(d *dockApp) error {
	img := p.surf.Image()
	paint.Clear(img)
	roundedPanel(img, img.Bounds(), d.th.popupRadius, d.th.scale, colPopupBg, colPopupEdge)

	top := d.th.popupPadY
	for i := range p.rows {
		row := &p.rows[i]
		if row.action {
			// A hairline above the action row, inset like a menu separator.
			sepY := int(top + d.th.popupSepH/2)
			paint.FillBlend(img, image.Rect(int(d.th.popupPadX/2), sepY,
				p.w-int(d.th.popupPadX/2), sepY+int(d.th.popupSepH)), colSeparator)
			top += d.th.popupSepH
		}
		if i == p.hover && row.target != "" {
			inset := d.th.popupPadX / 3
			paint.RoundRect(img, image.Rect(int(inset), int(top+1),
				p.w-int(inset), int(top+d.th.popupRowH-1)), d.th.popupRadius/2, colPopupHover)
		}

		col := colPopupText
		if row.dim {
			col = colPopupDim
		}
		baseline := int(top + d.th.popupRowH*0.68)
		text.Draw(img, d.faces.popup, col, int(d.th.popupPadX), baseline,
			row.label, p.w-2*int(d.th.popupPadX))
		top += d.th.popupRowH
	}
	return p.surf.Flush()
}

func (p *popup) close(d *dockApp) {
	if p.grabbed {
		xproto.UngrabPointer(d.conn, xproto.TimeCurrentTime)
		p.grabbed = false
	}
	if p.surf != nil {
		p.surf.Close()
		p.surf = nil
	}
	if p.win != 0 {
		xproto.DestroyWindow(d.conn, p.win)
		p.win = 0
	}
	if p.colormap != 0 {
		xproto.FreeColormap(d.conn, p.colormap)
		p.colormap = 0
	}
}

// openTarget hands a path or URI to the desktop's file handler.
//
// Thunar is preferred for the trash URI because xdg-open on this desktop
// resolves to it anyway, and asking it directly avoids a shell script in
// the middle that may or may not understand a trash:/// URI.
func openTarget(target string) error {
	argv := []string{"xdg-open", target}
	if target == trashURI {
		if path, err := exec.LookPath("thunar"); err == nil {
			argv = []string{path, target}
		}
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("no handler to open %s: %w", target, err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer devNull.Close() //nolint:errcheck // the child holds its own dup

	//nolint:noctx,gosec // detached by design; the target comes from the user's own folders
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Base(target), err)
	}
	// Reap it, or every file opened from a stack leaves a zombie behind.
	go func() {
		_ = cmd.Wait() //nolint:errcheck // nothing can be done with the status
	}()
	return nil
}

// shortLabel trims a name to something a menu row can hold. The text
// drawing truncates too, but doing it here keeps the popup from being
// sized to a 200-character filename.
func shortLabel(name string) string {
	const maxRunes = 44
	rs := []rune(name)
	if len(rs) <= maxRunes {
		return name
	}
	return string(rs[:maxRunes-1]) + "…"
}
