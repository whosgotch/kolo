package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// TestOSCTitleLeaks isolates the emulator from the agent entirely: does a
// BEL-terminated OSC 0 (set window title) leak its payload onto the screen?
//
// Claude Code sets the title to a summary of the current prompt, and that exact
// text keeps appearing inside the agent's input box in the probe dumps.
func TestOSCTitleLeaks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"bel terminated", "\x1b]0;SECRET TITLE\x07"},
		{"st terminated", "\x1b]0;SECRET TITLE\x1b\\"},
		{"bel with leading rune", "\x1b]0;✳ SECRET TITLE\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := vt.NewSafeEmulator(60, 5)
			term.Write([]byte("hello\r\n"))
			term.Write([]byte(tc.in))
			term.Write([]byte("world\r\n"))

			got := term.Render()
			if strings.Contains(got, "SECRET TITLE") {
				t.Errorf("OSC title payload leaked onto screen:\n%q", got)
			}
		})
	}
}

// TestOSCTitleNoReply checks the title sequence does not generate a reply that
// we would forward straight into the child's stdin.
func TestOSCTitleNoReply(t *testing.T) {
	term := vt.NewSafeEmulator(60, 5)
	term.Write([]byte("\x1b]0;SECRET TITLE\x07"))

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := term.Read(buf)
		done <- buf[:n]
	}()

	select {
	case got := <-done:
		t.Errorf("title generated a reply that would be typed into the agent: %q", got)
	default:
	}
}
