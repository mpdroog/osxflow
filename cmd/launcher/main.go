// Command launcher is a keyboard launcher: type a few letters, get
// the app, and if it is already running get the window you already have
// rather than a second copy of it.
//
// It is one of the tools in this repository; the shared desktop plumbing
// it uses (X11 windows, .desktop parsing) lives in internal/ at the root
// so the next tool can use it too.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mpdroog/osxflow/internal/calc"
	"github.com/mpdroog/osxflow/internal/desktop"
	"github.com/mpdroog/osxflow/internal/frecency"
	"github.com/mpdroog/osxflow/internal/launch"
	"github.com/mpdroog/osxflow/internal/search"
	"github.com/mpdroog/osxflow/internal/ui"
	"github.com/mpdroog/osxflow/internal/xwin"
)

func main() {
	var (
		list    = flag.Bool("list", false, "list the applications that were found and exit")
		windows = flag.Bool("windows", false, "list the open windows as the matcher sees them and exit")
		match   = flag.Bool("match", false, "show which application each open window was matched to and exit")
		open    = flag.String("open", "", "open the named application (focus it if running) and exit")
		eval    = flag.String("eval", "", "evaluate an expression and exit")
		query   = flag.String("search", "", "show how a query ranks against the installed applications and exit")
		scale   = flag.Float64("scale", 0, "display scale factor; 0 detects it from the desktop")
		noLearn = flag.Bool("no-learn", false, "do not record which applications get chosen")
		forget  = flag.String("forget", "", "remove an application from the usage memory, by desktop id")
	)
	flag.Parse()

	if err := run(*list, *windows, *match, *open, *eval, *query, *scale, *noLearn, *forget); err != nil {
		fmt.Fprintln(os.Stderr, "launcher:", err)
		os.Exit(1)
	}
}

func run(list, windows, match bool, open, eval, query string, scale float64, noLearn bool, forget string) error {
	if eval != "" {
		v, err := calc.Eval(eval)
		if err != nil {
			return err
		}
		fmt.Println(calc.Format(v))
		return nil
	}

	// Usage memory. A failure to read it is reported and then ignored: a
	// launcher that will not open because it could not remember your
	// habits is worse than one that has forgotten them.
	store, storeErr := loadStore()
	if storeErr != nil {
		fmt.Fprintln(os.Stderr, "launcher:", storeErr)
	}
	now := time.Now()
	ranker := store.At(now)

	if forget != "" {
		store.Forget(forget)
		if err := store.Save(now); err != nil {
			return err
		}
		fmt.Printf("forgot %s\n", forget)
		return nil
	}

	apps, problems := desktop.Scan()
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "launcher: skipped an entry:", p)
	}
	if len(apps) == 0 {
		return fmt.Errorf("no applications found in %v", desktop.DataDirs())
	}

	if list {
		printApps(apps)
		return nil
	}
	if query != "" {
		printSearch(apps, query, ranker)
		return nil
	}

	server, err := xwin.NewX11()
	if err != nil {
		return err
	}
	defer server.Close() //nolint:errcheck // exiting anyway

	switch {
	case windows:
		return printWindows(server)
	case match:
		return printMatches(apps, server)
	case open != "":
		return openApp(apps, server, open)
	}

	// No flags: show the launcher. Everything above is a way to inspect
	// what it would do without it appearing on screen.
	l := launch.New(server)
	return ui.Run(apps, func(app *desktop.App, chosenFor string) error {
		res, err := l.Open(app)
		if err != nil {
			return err
		}
		if res.Focused {
			log.Printf("focused 0x%x of %s (matched by %s)", res.Window.ID, app.Name, res.Confidence)
		} else {
			log.Printf("started %s", app.Name)
		}
		// Recorded only after a successful launch: an application that
		// failed to start is not one to rank higher next time. A failure
		// to save costs the lesson, not the launch, so it is reported
		// rather than returned.
		if !noLearn {
			store.Record(chosenFor, app.ID, time.Now())
			if err := store.Save(time.Now()); err != nil {
				log.Printf("could not save usage: %v", err)
			}
		}
		return nil
	}, scale, ranker)
}

// loadStore opens the usage memory, always returning a usable store even
// when the file could not be read.
func loadStore() (*frecency.Store, error) {
	path, err := frecency.DefaultPath()
	if err != nil {
		return frecency.New(""), err
	}
	return frecency.Load(path)
}

func printApps(apps []desktop.App) {
	for i := range apps {
		a := &apps[i]
		binary := a.Binary
		if binary == "" {
			binary = "(unresolved: " + a.Argv[0] + ")"
		}
		fmt.Printf("%-30s %-34s %-40s %s\n", a.ID, a.Name, binary, a.StartupWMClass)
	}
	fmt.Printf("\n%d applications\n", len(apps))
}

func printSearch(apps []desktop.App, query string, ranker search.Ranker) {
	results := search.Filter(apps, query, ranker)
	for i, r := range results {
		if i >= 10 {
			fmt.Printf("... and %d more\n", len(results)-i)
			break
		}
		mark := " "
		if r.Pinned {
			mark = "*"
		}
		fmt.Printf("%2d.%s %-36s %-14s used=%-6.2f %s\n",
			i+1, mark, r.App.Name, r.Tier, r.Usage, r.App.Binary)
	}
	if len(results) == 0 {
		fmt.Println("(nothing matched)")
	}
}

func printWindows(server xwin.Server) error {
	wins, err := server.Windows()
	if err != nil {
		return err
	}
	for _, w := range wins {
		exe, err := launch.ProcExe(w.PID)
		if err != nil {
			exe = "(" + err.Error() + ")"
		}
		fmt.Printf("0x%-9x pid=%-7d %-34s %s\n", w.ID, w.PID, w.Instance+"."+w.Class, exe)
	}
	fmt.Printf("\n%d windows (bottom to top)\n", len(wins))
	return nil
}

// printMatches is the check that matters: every window that belongs to a
// known application should find it, and say which rule got it there.
func printMatches(apps []desktop.App, server xwin.Server) error {
	wins, err := server.Windows()
	if err != nil {
		return err
	}

	matched := 0
	for _, w := range wins {
		name, conf := "-", launch.None
		for i := range apps {
			if _, c := launch.Match(&apps[i], []xwin.Window{w}, launch.ProcExe); c > conf {
				name, conf = apps[i].Name, c
			}
		}
		if conf > launch.None {
			matched++
		}
		fmt.Printf("%-36s -> %-24s %s\n", w.Instance+"."+w.Class, name, conf)
	}
	fmt.Printf("\n%d of %d windows matched an application\n", matched, len(wins))
	return nil
}

func openApp(apps []desktop.App, server xwin.Server, query string) error {
	app, ok := findApp(apps, query)
	if !ok {
		return fmt.Errorf("no application matching %q", query)
	}
	res, err := launch.New(server).Open(app)
	if err != nil {
		return err
	}
	if res.Focused {
		fmt.Printf("focused existing window 0x%x of %s (matched by %s)\n", res.Window.ID, app.Name, res.Confidence)
	} else {
		fmt.Printf("started %s (%s)\n", app.Name, strings.Join(app.Argv, " "))
	}
	return nil
}

// findApp takes an exact name first, then a case-insensitive prefix, then
// a substring, so "-open ghost" finds Ghostty without "-open t" finding
// something arbitrary before the exact match had a chance.
func findApp(apps []desktop.App, query string) (*desktop.App, bool) {
	q := strings.ToLower(query)
	for i := range apps {
		if strings.EqualFold(apps[i].Name, query) {
			return &apps[i], true
		}
	}
	for i := range apps {
		if strings.HasPrefix(strings.ToLower(apps[i].Name), q) {
			return &apps[i], true
		}
	}
	for i := range apps {
		if strings.Contains(strings.ToLower(apps[i].Name), q) {
			return &apps[i], true
		}
	}
	return nil, false
}
