package main

// What "About This Linux" shows, read from where the kernel and the
// distribution keep it. Every field is optional: one that cannot be read
// is left out of the menu, and why goes to the log.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type about struct {
	model  string // "MacBookPro14,1"
	system string // "Linux Mint 22.1"
	kernel string // "7.0.0-30-generic"
	memory string // "16 GB"
	uptime string // "3 h 12 min"
}

// The files the fields come from.
const (
	productFile   = "/sys/class/dmi/id/product_name"
	osReleaseFile = "/etc/os-release"
	kernelFile    = "/proc/sys/kernel/osrelease"
	meminfoFile   = "/proc/meminfo"
	uptimeFile    = "/proc/uptime"
)

// readAbout reads what it can. The error joins everything that could not
// be read; the fields that could are filled in regardless.
func readAbout() (about, error) {
	var a about
	var errs []error
	// keep takes a read's result, so every os.ReadFile below names its
	// file directly.
	keep := func(b []byte, err error) []byte {
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		return b
	}

	if b := keep(os.ReadFile(productFile)); b != nil {
		a.model = strings.TrimSpace(string(b))
	}
	if b := keep(os.ReadFile(osReleaseFile)); b != nil {
		fields := parseOSRelease(b)
		a.system = fields["PRETTY_NAME"]
		if a.system == "" {
			a.system = strings.TrimSpace(fields["NAME"] + " " + fields["VERSION"])
		}
	}
	if b := keep(os.ReadFile(kernelFile)); b != nil {
		a.kernel = strings.TrimSpace(string(b))
	}
	if b := keep(os.ReadFile(meminfoFile)); b != nil {
		kB, err := parseMemTotal(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", meminfoFile, err))
		} else {
			a.memory = formatMemory(kB)
		}
	}
	if b := keep(os.ReadFile(uptimeFile)); b != nil {
		d, err := parseUptime(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", uptimeFile, err))
		} else {
			a.uptime = formatUptime(d)
		}
	}
	return a, errors.Join(errs...)
}

// parseOSRelease reads os-release's KEY=value lines. Values may be quoted
// with double or single quotes; comments and malformed lines are skipped,
// as the specification says a reader should.
func parseOSRelease(b []byte) map[string]string {
	fields := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		fields[key] = value
	}
	// A line too long for the scanner ends the scan; what came before it is
	// still good, and none of the fields read here are that long.
	return fields
}

var errNoMemTotal = errors.New("no MemTotal line")

// parseMemTotal finds "MemTotal:   16318048 kB".
func parseMemTotal(b []byte) (uint64, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kB, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("MemTotal %q: %w", fields[1], err)
		}
		return kB, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errNoMemTotal
}

// parseUptime reads the first field of /proc/uptime: seconds since boot,
// with a fraction.
func parseUptime(b []byte) (time.Duration, error) {
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errors.New("empty")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("uptime %q: %w", fields[0], err)
	}
	// Past a century of uptime the number is not an uptime; refusing it
	// also keeps the conversion below from overflowing.
	const maxSecs = 100 * 365 * 24 * 3600
	if secs < 0 || secs > maxSecs || secs != secs {
		return 0, fmt.Errorf("uptime %q out of range", fields[0])
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// formatMemory rounds to what the machine is sold as: 16318048 kB of
// usable memory is a 16 GB machine.
func formatMemory(kB uint64) string {
	const mib = 1024
	const gib = 1024 * 1024
	if kB < gib {
		return fmt.Sprintf("%d MB", (kB+mib/2)/mib)
	}
	return fmt.Sprintf("%d GB", (kB+gib/2)/gib)
}

// formatUptime is "12 min", "3 h 12 min" or "2 days 4 h": precise enough
// to answer "when did I last restart", no more.
func formatUptime(d time.Duration) string {
	minutes := int(d / time.Minute)
	hours, minutes := minutes/60, minutes%60
	days, hours := hours/24, hours%24
	switch {
	case days == 1:
		return fmt.Sprintf("1 day %d h", hours)
	case days > 1:
		return fmt.Sprintf("%d days %d h", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d min", hours, minutes)
	}
	return fmt.Sprintf("%d min", minutes)
}
