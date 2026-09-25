package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// readGroup reads one [group] of a desktop-entry style file -- .desktop,
// D-Bus .service, mimeapps.list -- as key=value pairs. Comments, blank
// lines and other groups are skipped; a key given twice keeps its first
// value, as the specification says.
func readGroup(path, group string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // reading the user's own configuration is this tool's job
	if err != nil {
		return nil, err
	}
	keys, err := parseGroup(data, group)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys, nil
}

func parseGroup(data []byte, group string) (map[string]string, error) {
	keys := map[string]string{}
	in := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			in = line[1:len(line)-1] == group
			continue
		case !in:
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, seen := keys[k]; !seen {
			keys[k] = strings.TrimSpace(v)
		}
	}
	if err := sc.Err(); err != nil {
		// Only a line over a megabyte gets here.
		return nil, err
	}
	return keys, nil
}
