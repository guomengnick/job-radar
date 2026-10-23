package version

import (
	"runtime/debug"
	"testing"
)

func TestFromSettings(t *testing.T) {
	for want, s := range map[string][]debug.BuildSetting{
		"3a9a26c":       {{Key: "vcs.revision", Value: "3a9a26cb4160fba24ee329879f1b19a39fe8473f"}, {Key: "vcs.modified", Value: "false"}},
		"3a9a26c+dirty": {{Key: "vcs.revision", Value: "3a9a26cb4160fba24ee329879f1b19a39fe8473f"}, {Key: "vcs.modified", Value: "true"}},
		"unknown":       {{Key: "GOARCH", Value: "arm64"}},
	} {
		if got := fromSettings(s); got != want {
			t.Errorf("%v → %q, want %q", s, got, want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	for _, c := range []struct {
		v, min string
		want   bool
	}{
		{"1.4.0", "1.4.0", true}, {"1.10.0", "1.4.0", true}, {"2.0", "1.4.0", true}, {"1.3.9", "1.4.0", false},
		{"1.4", "1.4.0", true}, {"", "1.4.0", false}, {"1.x", "1.4.0", false}, {"1.4.0.1", "1.4.0", false},
	} {
		if got := AtLeast(c.v, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v", c.v, c.min, got)
		}
	}
}
