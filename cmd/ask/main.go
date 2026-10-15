// ask shows one dialog and prints what the user chose.
//
// It exists because the tools that would otherwise be shelled out to for
// this -- zenity, yad, kdialog -- are not installed on this machine and
// pull a desktop's worth of dependencies when they are. Everything a save
// dialog needs is already in osxflow: a window, a font, a list and a text
// field.
//
// The exit status is the answer to "did the user decide anything", and
// callers depend on telling its three cases apart:
//
//	0  accepted; the answer is on stdout
//	1  dismissed -- Escape, the close button, or an empty entry
//	2  the dialog could not be shown at all
//
// 1 and 2 must not be conflated. A caller that treats "no dialog appeared"
// as "the user pressed Cancel" throws away whatever the dialog was about
// to record, silently, which is the bug this program was written for.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	exitCancelled = 1
	exitNoDialog  = 2
)

func main() {
	var (
		mode     = flag.String("mode", "entry", "entry, list or message")
		title    = flag.String("title", "", "bold first line")
		prompt   = flag.String("text", "", "the question, wrapped over as many lines as it needs")
		def      = flag.String("default", "", "text the entry starts with")
		editable = flag.Bool("editable", false, "a list may also accept something typed that is not on it")
		allowNil = flag.Bool("allow-empty", false, "accept an entry left blank instead of treating it as a dismissal")
		copyBtn  = flag.Bool("copy", false, "a message gets a Copy button that puts its text on the clipboard")
		scale    = flag.Float64("scale", 0, "display scale; 0 detects it")
	)
	flag.Parse()

	m, err := parseMode(*mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ask:", err)
		os.Exit(exitNoDialog)
	}

	// The list and the message body come in on stdin rather than argv:
	// both can be long, and a project named `--editable` should not be
	// able to turn into a flag.
	var items []string
	body := *prompt
	switch m {
	case ModeList:
		if items, err = readLines(os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "ask: reading the list:", err)
			os.Exit(exitNoDialog)
		}
	case ModeMessage:
		text, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "ask: reading the message:", rerr)
			os.Exit(exitNoDialog)
		}
		if s := strings.TrimSpace(string(text)); s != "" {
			body = s
		}
	}

	answer, err := show(NewModel(m, items, *def, *editable, *allowNil), *title, body, *copyBtn && m == ModeMessage, *scale)
	switch {
	case errors.Is(err, errCancelled):
		os.Exit(exitCancelled)
	case err != nil:
		fmt.Fprintln(os.Stderr, "ask:", err)
		os.Exit(exitNoDialog)
	}
	fmt.Println(answer)
}

func parseMode(s string) (Mode, error) {
	switch s {
	case "entry":
		return ModeEntry, nil
	case "list":
		return ModeList, nil
	case "message":
		return ModeMessage, nil
	}
	return 0, fmt.Errorf("unknown -mode %q: want entry, list or message", s)
}

// readLines is the list, one item per line, with blanks dropped so that a
// trailing newline does not become an empty row.
func readLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}
