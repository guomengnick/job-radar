// Package version names the code that is running. Go writes the commit into
// every binary built inside a git checkout (vcs stamping), so the binary can
// say which commit it is without a build flag; `go version -m bootstrap`
// reads the same record from outside.
package version

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// Commit returns the short commit the binary was built from, with "+dirty"
// when the checkout had uncommitted or untracked files, or "unknown" when
// there is no record (go run, or a build outside git).
func Commit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return fromSettings(info.Settings)
}

func fromSettings(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

// AtLeast reports whether version v (like "1.4.0") is min or newer. A
// version that does not parse is treated as too old.
func AtLeast(v, min string) bool {
	a, ok1 := parse(v)
	b, ok2 := parse(min)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
