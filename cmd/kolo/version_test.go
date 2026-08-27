package main

import (
	"runtime"
	"runtime/debug"
	"strings"
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

// A release binary is built with -ldflags, and the build info underneath it
// still describes a checkout. The flag has to win, or a downloaded kolo calls
// itself by a commit nobody chose it from.
func TestVersionPrefersWhatTheBuildWasToldToSay(t *testing.T) {
	was := version
	t.Cleanup(func() { version = was })

	version = "v0.1.0"
	stamped()
	if version != "v0.1.0" {
		t.Errorf("version = %q, want the ldflags value to stand", version)
	}
}

// The line a bug report is asked for. It names the platform and the toolchain
// as well, because a version on its own is rarely enough to reproduce with.
func TestVersionLineSaysWhatIsNeededToReproduce(t *testing.T) {
	was := version
	t.Cleanup(func() { version = was })
	version = "v0.1.1"

	line := versionLine()
	for _, want := range []string{"kolo", "v0.1.1", runtime.GOOS, runtime.GOARCH, runtime.Version()} {
		if !strings.Contains(line, want) {
			t.Errorf("version line %q does not mention %q", line, want)
		}
	}
}

// doctor's report is the thing people paste, so it has to say which binary
// wrote it without being asked separately.
func TestDoctorReportOpensWithTheVersion(t *testing.T) {
	was := version
	t.Cleanup(func() { version = was })
	version = "v0.1.1"

	var out strings.Builder
	if _, err := doctor(&out, absent(t), absent(t)); err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(out.String(), "\n"); first != versionLine() {
		t.Errorf("doctor opens with %q, want the version line %q", first, versionLine())
	}
}
