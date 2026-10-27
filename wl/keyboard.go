package wl

// A virtual keyboard: zwp_virtual_keyboard_v1.
//
// It exists for one reason. labwc has no IPC, and wlr-foreign-toplevel can
// focus, close and minimise another client's window but never move or size
// it -- so the only way left to place a window is to focus it and press a
// key the compositor has bound to placing it. This sends that key.
//
// The keyboard brings its own keymap, which is what makes the keys safe to
// use: a chord such as Super+Ctrl+Alt+Shift+F33 does not exist on any real
// keyboard, so a keybind on it fires only when this sends it.

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Requests, zwp_virtual_keyboard_manager_v1 and zwp_virtual_keyboard_v1.
const (
	vkbdMgrCreate   = 0 // create_virtual_keyboard(seat, new_id)
	vkbdKeymap      = 0 // keymap(format, fd, size)
	vkbdKey         = 1 // key(time, key, state)
	vkbdModifiers   = 2 // modifiers(depressed, latched, locked, group)
	vkbdDestroy     = 3
	vkbdMgrVersion  = 1
	keymapFormatXKB = 1 // wl_keyboard.keymap_format.xkb_v1
)

// Real modifier bits, as xkb serialises them. A keybind's W-, C-, A- and S-
// are Mod4, Control, Mod1 and Shift.
const (
	ModShift = 1 << 0
	ModCtrl  = 1 << 2
	ModAlt   = 1 << 3
	ModSuper = 1 << 6
)

// Keyboard is a virtual keyboard on the seat.
type Keyboard struct {
	c  *Conn
	id uint32
}

// VirtualKeyboard creates a keyboard with the given xkb keymap.
func (c *Conn) VirtualKeyboard(keymap string) (*Keyboard, error) {
	c.mu.Lock()
	mgr, seat := c.vkbdMgr, c.seat
	c.mu.Unlock()
	if mgr == 0 {
		return nil, errors.New("the compositor does not offer zwp_virtual_keyboard_manager_v1")
	}
	id := c.alloc()
	if err := c.send(mgr, vkbdMgrCreate, argUint(seat), argUint(id)); err != nil {
		return nil, err
	}

	fd, err := unix.MemfdCreate("osxflow-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("creating the keymap file: %w", err)
	}
	defer func() {
		if closeErr := unix.Close(fd); closeErr != nil {
			c.fail(fmt.Errorf("closing the keymap file: %w", closeErr))
		}
	}()
	// The size the compositor is told includes the NUL, which xkbcommon
	// expects to find at the end of the mapping.
	data := append([]byte(keymap), 0)
	if _, err := unix.Write(fd, data); err != nil {
		return nil, fmt.Errorf("writing the keymap: %w", err)
	}
	//nolint:gosec // a keymap is a few kilobytes
	if err := c.sendFD(id, vkbdKeymap, fd, argUint(keymapFormatXKB), argUint(uint32(len(data)))); err != nil {
		return nil, err
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	return &Keyboard{c: c, id: id}, nil
}

// Chord holds mods, taps the key with evdev code key, and lets go, then
// waits until the compositor has acted on it.
func (k *Keyboard) Chord(mods, key uint32) error {
	const released, pressed = 0, 1
	for _, step := range []struct {
		op   uint16
		args []arg
	}{
		{vkbdModifiers, []arg{argUint(mods), argUint(0), argUint(0), argUint(0)}},
		{vkbdKey, []arg{argUint(0), argUint(key), argUint(pressed)}},
		{vkbdKey, []arg{argUint(0), argUint(key), argUint(released)}},
		{vkbdModifiers, []arg{argUint(0), argUint(0), argUint(0), argUint(0)}},
	} {
		if err := k.c.send(k.id, step.op, step.args...); err != nil {
			return err
		}
	}
	return k.c.roundtrip()
}

// Close destroys the keyboard.
func (k *Keyboard) Close() error {
	return k.c.send(k.id, vkbdDestroy)
}
