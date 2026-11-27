package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionPrefersTheReleaseStamp(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.1.2-0.20261127-abc"}}
	for _, tc := range []struct {
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"v1.2.0", info, "v1.2.0"},
		{"", info, "v1.1.2-0.20261127-abc"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"", nil, "dev"},
	} {
		if got := resolveVersion(tc.stamped, tc.info); got != tc.want {
			t.Errorf("resolveVersion(%q, …) = %q, want %q", tc.stamped, got, tc.want)
		}
	}
}
