// Command osxflow-doctor checks that the desktop is still set up the way
// osxflow left it, and says what to do about anything that is not.
//
// Upgrades are what it is for. osxflow replaced parts of XFCE and Mint by
// overriding them from the user's own configuration -- a masked unit, a
// hidden autostart entry, a D-Bus service file, a trimmed session -- and
// an upgrade that renames what was overridden gets around the override
// without a word. Run it after a big upgrade, or when the desktop looks
// wrong:
//
//	osxflow-doctor        problems only, and a summary
//	osxflow-doctor -v     every check
//
// It prints text and opens no window, so it works over SSH too. It exits 1
// when a check failed, 0 otherwise; warnings are worth reading but do not
// change the exit status.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run())
}

// run is main with an exit status, so that deferred cleanup happens before
// the process exits.
func run() int {
	verbose := flag.Bool("v", false, "show every check, not only the problems")
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "osxflow-doctor: %v\n", err)
		return 2
	}
	sys := &liveSystem{proc: "/proc"}
	bus, busErr := connectBus()
	if busErr == nil {
		sys.bus = bus
		defer func() {
			if err := bus.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "osxflow-doctor: closing the session bus: %v\n", err)
			}
		}()
	}

	d := &doctor{sys: sys, home: home, root: "/"}
	d.run()
	if busErr != nil {
		// Said once here rather than on every skipped check.
		fmt.Printf("no session bus (%v); checks that need one are skipped\n\n", busErr)
	}
	if report(os.Stdout, d.results, *verbose) {
		return 1
	}
	return 0
}

// report prints the results -- all of them, or only what is not ok -- and
// a summary, and says whether anything failed.
func report(w io.Writer, results []result, verbose bool) (failed bool) {
	counts := map[status]int{}
	var b strings.Builder
	for _, r := range results {
		counts[r.status]++
		if r.status == statusOK && !verbose {
			continue
		}
		if r.status == statusSkip && !verbose {
			continue
		}
		fmt.Fprintf(&b, "%-4s  %-12s  %s\n", r.status, r.area, r.what)
		if r.fix != "" {
			fmt.Fprintf(&b, "%-4s  %-12s  %s %s\n", "", "", fixLabel(r.status), r.fix)
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d checks: %d ok, %d warnings, %d failed, %d skipped\n",
		len(results), counts[statusOK], counts[statusWarn], counts[statusFail], counts[statusSkip])
	if counts[statusWarn]+counts[statusFail] == 0 {
		b.WriteString("everything is as osxflow left it\n")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		fmt.Fprintf(os.Stderr, "osxflow-doctor: writing the report: %v\n", err)
	}
	return counts[statusFail] > 0
}

func fixLabel(s status) string {
	if s == statusSkip {
		return "why:"
	}
	return "fix:"
}
