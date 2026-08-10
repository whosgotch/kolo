package main

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/hinshun/vt10x"
)

// TestReplay replays a recorded raw PTY capture through both candidate
// emulators. Recordings are not committed (they contain live session content);
// point KOLO_RAW at one produced by the probe:
//
//	KOLO_RAW=out-q1c/raw.log go test ./probe -run TestReplay -v
func TestReplay(t *testing.T) {
	path := os.Getenv("KOLO_RAW")
	if path == "" {
		t.Skip("set KOLO_RAW to a raw PTY capture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The agent's own window title, which is what leaks. Take it from the
	// recording so the assertion is about this capture, not a guess.
	title := extractTitle(raw)
	t.Logf("last window title in capture: %q", title)

	charm := vt.NewSafeEmulator(120, 40)
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := charm.Read(buf); err != nil {
				return
			}
		}
	}()
	charm.Write(raw)

	hinshun := vt10x.New(vt10x.WithSize(120, 40))
	hinshun.Write(raw)

	if title != "" {
		t.Run("charmbracelet", func(t *testing.T) {
			if strings.Contains(charm.Render(), title) {
				t.Errorf("window title %q leaked onto the screen", title)
			}
		})
		t.Run("hinshun", func(t *testing.T) {
			if strings.Contains(hinshun.String(), title) {
				t.Errorf("window title %q leaked onto the screen", title)
			}
		})
	}
}

// extractTitle pulls the payload of the last BEL-terminated OSC 0 in the capture.
func extractTitle(raw []byte) string {
	const marker = "\x1b]0;"
	s := string(raw)
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	end := strings.IndexByte(rest, '\x07')
	if end < 0 {
		return ""
	}
	// Drop the leading spinner glyph; the ASCII tail is what leaks.
	return strings.TrimSpace(strings.TrimLeftFunc(rest[:end], func(r rune) bool { return r > 127 || r == ' ' }))
}
