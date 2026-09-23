package main

// The stack popup: the little panel that opens above Downloads or Trash
// showing the five most recent things in it, with a way to open the folder
// itself.

import (
	"errors"
	"fmt"
	"image"
	"io/fs"
	"log"
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
		// Straight away, not once the new one is up: if building it fails,
		// a closed popup left in d.popup has no surface and no window, and
		// the next motion or expose event would paint into it.
		d.popup = nil
	}

	rows, err := stackRows(st)
	if err != nil {
		return err
	}
	p := &popup{rows: rows, hover: -1}
	if err := p.create(d, d.places[i].CentreX); err != nil {
		// Whatever create got as far as making would otherwise stay on the
		// server until the dock exits.
		p.close(d)
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
		name    string
		action  string
		target  string
	)
	switch st.Kind {
	case dock.StackDownloads:
		entries, err = stack.Recent(st.Dir, stackEntries)
		name, action, target = "Downloads", "Open Downloads", st.Dir
	case dock.StackTrash:
		entries, err = stack.TrashEntries(st.Dir, stackEntries)
		name, action, target = "Trash", "Open Trash", trashURI
	case dock.NotAStack:
		return nil, errors.New("not a stack")
	}
	if errors.Is(err, fs.ErrNotExist) {
		// A Downloads folder that has not been created yet is empty, the
		// same way a trash that was never used is -- not unreadable.
		err = nil
	}
	if err != nil {
		// An unreadable folder is worth showing rather than failing on: the
		// action row still opens it, which is how the user finds out what
		// is wrong. The entries that could be read, if any, are still
		// shown; the detail goes to the log, since a menu row has no room
		// for it.
		log.Printf("listing %s: %v", name, err)
	}

	rows := make([]popupRow, 0, len(entries)+2)
	for _, e := range entries {
		label := e.Name
		if e.IsDir {
			label += "/"
		}
		rows = append(rows, popupRow{label: shortLabel(label), target: e.Path})
	}
	switch {
	case len(rows) > 0:
	case err != nil:
		// Not "Empty": that would be a claim about a folder nobody could
		// read.
		rows = append(rows, popupRow{label: "Can't read folder", dim: true})
	default:
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
	if x+p.w > d.mon.X+d.mon.W {
		x = d.mon.X + d.mon.W - p.w
	}
	y := d.mon.Bottom() - d.th.winH + int(d.th.panelTop()) - int(d.th.popupGap) - p.h

	win, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return fmt.Errorf("allocating a popup window id: %w", err)
	}

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
	// Only now: close destroys p.win, and a window that was never created
	// is a BadWindow to report rather than something to clean up.
	p.win = win
	if nameErr := d.nameWindow(win, "dock-stack"); nameErr != nil {
		log.Printf("warning: %v", nameErr)
	}

	p.surf, err = xsurface.New(d.conn, win, d.visual.depth, p.w, p.h)
	if err != nil {
		return err
	}
	if mapErr := xproto.MapWindowChecked(d.conn, win).Check(); mapErr != nil {
		return fmt.Errorf("mapping the popup: %w", mapErr)
	}
	// Checked, unlike the dock's raise on visibility changes: this is once
	// per click, and a popup left underneath another window is a failure
	// worth naming where it happened.
	if raiseErr := d.raiseNow(win); raiseErr != nil {
		log.Printf("raising the popup: %v", raiseErr)
	}
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
// dismissal, which is why it is logged rather than returned.
func (p *popup) grab(d *dockApp) {
	reply, err := xproto.GrabPointer(d.conn, false, p.win,
		uint16(xproto.EventMaskButtonPress|xproto.EventMaskPointerMotion),
		xproto.GrabModeAsync, xproto.GrabModeAsync,
		xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
	switch {
	case err != nil:
		log.Printf("grabbing pointer for stack: %v", err)
	case reply == nil:
		log.Print("grabbing pointer for stack: no reply from the X server")
	case reply.Status != xproto.GrabStatusSuccess:
		log.Printf("grabbing pointer for stack: %s", grabStatus(reply.Status))
	default:
		p.grabbed = true
	}
}

// grabStatus names a GrabPointer status, which X reports as a bare number.
func grabStatus(status byte) string {
	switch status {
	case xproto.GrabStatusAlreadyGrabbed:
		return "another client has the pointer grabbed"
	case xproto.GrabStatusInvalidTime:
		return "invalid time"
	case xproto.GrabStatusNotViewable:
		return "the popup is not viewable"
	case xproto.GrabStatusFrozen:
		return "the pointer is frozen by another grab"
	}
	return fmt.Sprintf("status %d", status)
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
		log.Print(openErr)
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

// close releases what the popup holds on the server. It is safe on a popup
// that create only got part of the way through.
//
// The requests are checked: this happens once per click, and there is no
// later moment at which a failure here could be matched to its cause. A
// failure is logged rather than returned, because the popup is going away
// either way and every caller has something more useful to do next.
func (p *popup) close(d *dockApp) {
	if p.grabbed {
		if err := xproto.UngrabPointerChecked(d.conn, xproto.TimeCurrentTime).Check(); err != nil {
			log.Printf("releasing the pointer grab: %v", err)
		}
		p.grabbed = false
	}
	if p.surf != nil {
		if err := p.surf.Close(); err != nil {
			log.Printf("releasing the popup surface: %v", err)
		}
		p.surf = nil
	}
	if p.win != 0 {
		if err := xproto.DestroyWindowChecked(d.conn, p.win).Check(); err != nil {
			log.Printf("destroying the popup: %v", err)
		}
		p.win = 0
	}
	if p.colormap != 0 {
		if err := xproto.FreeColormapChecked(d.conn, p.colormap).Check(); err != nil {
			log.Printf("freeing the popup colormap: %v", err)
		}
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
		path, err := exec.LookPath("thunar")
		if err != nil {
			// No thunar is the ordinary case on another desktop, and
			// xdg-open is the fallback for exactly that; anything else is
			// worth a line before falling back.
			if !errors.Is(err, exec.ErrNotFound) {
				log.Printf("looking for thunar: %v", err)
			}
		} else {
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
	// The child holds its own duplicate once started; this is only the
	// dock's copy, and it goes whether or not the start worked.
	defer func() {
		if closeErr := devNull.Close(); closeErr != nil {
			log.Printf("closing %s: %v", os.DevNull, closeErr)
		}
	}()

	//nolint:noctx,gosec // detached by design; the target comes from the user's own folders
	cmd := exec.Command(bin, argv[1:]...)
	// Not the dock's own stderr, tempting as xdg-open's explanation of a
	// click that opened nothing is: xdg-open execs the handler, so the file
	// manager would inherit the dock's stderr for its whole life, the way
	// launch.SpawnDetached avoids for every application. A failed open
	// still reaches the log, as the exit status the reaper below reports.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		// The handler inherits the dock's own directory instead, which
		// matters only to one that resolves relative paths.
		log.Printf("warning: starting %s outside the home directory: %v", filepath.Base(bin), homeErr)
	} else {
		cmd.Dir = home
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Base(target), err)
	}
	// Reap it, or every file opened from a stack leaves a zombie behind. A
	// failed exit is the handler saying it could not open the thing, which
	// the click that asked for it will never otherwise hear about.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("%s %s: %v", filepath.Base(bin), target, err)
		}
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
