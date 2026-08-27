package main

import (
	"runtime/debug"
	"testing"
)

// The shapes a binary arrives in, and what each should say it is.
func TestVersionFromVCS(t *testing.T) {
	const long = "78a6230255dffbc2c084964a7e6cf2ebf16b5adc"
	cases := []struct {
		name         string
		settings     []debug.BuildSetting
		wantRevision string
		wantDirty    bool
		wantOK       bool
	}{{
		name:         "a clean checkout is its commit, shortened",
		settings:     []debug.BuildSetting{{Key: "vcs.revision", Value: long}, {Key: "vcs.modified", Value: "false"}},
		wantRevision: "78a6230255df",
		wantOK:       true,
	}, {
		name:         "uncommitted changes are reported, not hidden",
		settings:     []debug.BuildSetting{{Key: "vcs.revision", Value: long}, {Key: "vcs.modified", Value: "true"}},
		wantRevision: "78a6230255df",
		wantDirty:    true,
		wantOK:       true,
	}, {
		// go install pkg@version builds from the module cache, which is not a
		// checkout: the tag in Main.Version is all there is to go on.
		name:     "a released build carries no commit",
		settings: []debug.BuildSetting{{Key: "GOARCH", Value: "amd64"}},
		wantOK:   false,
	}, {
		// A revision shorter than the twelve it is trimmed to must not panic
		// the binary on start.
		name:         "a short revision is left as it is",
		settings:     []debug.BuildSetting{{Key: "vcs.revision", Value: "78a6230"}},
		wantRevision: "78a6230",
		wantOK:       true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			revision, dirty, ok := fromVCS(&debug.BuildInfo{Settings: c.settings})
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if revision != c.wantRevision {
				t.Errorf("revision = %q, want %q", revision, c.wantRevision)
			}
			if dirty != c.wantDirty {
				t.Errorf("dirty = %v, want %v", dirty, c.wantDirty)
			}
		})
	}
}
