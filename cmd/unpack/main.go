// Command unpack extracts archives where they are and removes them, for
// double-clicking in a file manager with none of an archive app's
// ceremony.
//
//	unpack archive.zip [more.tar.gz ...]
//
// One top-level item lands beside the archive as it is; several go into a
// folder named after it. Nothing is overwritten: a name already taken gets
// " 2", as in Finder. The archive is removed only once everything is in
// place. There is no window, so a failure is reported as a notification
// as well as on stderr, and the archive is left alone.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/godbus/dbus/v5"

	"github.com/mpdroog/osxflow/internal/unpack"
)

func main() {
	log.SetPrefix("unpack: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if len(os.Args) < 2 {
		log.Fatal("usage: unpack archive [archive ...]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	failed := false
	for _, path := range os.Args[1:] {
		dest, err := unpack.Unpack(ctx, path)
		if err != nil {
			failed = true
			log.Printf("%q: %v", path, err) //nolint:gosec // G706: the path is quoted, so it cannot forge a log line
			if notifyErr := notify(filepath.Base(path), err); notifyErr != nil {
				log.Printf("sending notification: %v", notifyErr)
			}
			continue
		}
		log.Printf("%q -> %q", path, dest) //nolint:gosec // G706: both paths are quoted, so they cannot forge a log line
	}
	stop()
	if failed {
		os.Exit(1)
	}
}

// notify tells the user why an archive was left as it was.
func notify(name string, err error) error {
	body := err.Error()
	switch {
	case errors.Is(err, unpack.ErrEncrypted):
		body = "It is password-protected. Right-click it and choose Open With › Archive Manager."
	case errors.Is(err, unpack.ErrUnknown):
		body = "It is not an archive unpack knows how to open."
	case errors.Is(err, unpack.ErrUnsafePath):
		body = "It tries to write outside its own folder, so it was not opened: " + body
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("connecting to the session bus: %w", err)
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	call := obj.Call("org.freedesktop.Notifications.Notify", 0,
		"Unpack", uint32(0), "package-x-generic",
		"Couldn't unpack "+name, body,
		[]string{}, map[string]dbus.Variant{}, int32(-1))
	closeErr := conn.Close()
	if call.Err != nil {
		return errors.Join(call.Err, closeErr)
	}
	return closeErr
}
