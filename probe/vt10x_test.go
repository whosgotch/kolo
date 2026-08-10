package main

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

// TestVT10xOSCTitle runs the spec's fallback emulator against the exact case
// that breaks charmbracelet/x/vt: an OSC 0 payload containing a non-ASCII rune.
func TestVT10xOSCTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"bel terminated", "\x1b]0;SECRET TITLE\x07"},
		{"bel with leading rune", "\x1b]0;✳ SECRET TITLE\x07"},
		{"spinner frames", "\x1b]0;⠂ SECRET TITLE\x07\x1b]0;⠐ SECRET TITLE\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := vt10x.New(vt10x.WithSize(60, 5))
			term.Write([]byte("hello\r\n"))
			term.Write([]byte(tc.in))
			term.Write([]byte("world\r\n"))

			got := term.String()
			if strings.Contains(got, "SECRET TITLE") {
				t.Errorf("OSC title payload leaked onto screen:\n%q", got)
			}
			if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
				t.Errorf("surrounding text lost:\n%q", got)
			}
		})
	}
}

// TestVT10xWideRunes checks the box-drawing and braille glyphs the agent's TUI
// leans on survive a round trip.
func TestVT10xWideRunes(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(20, 3))
	term.Write([]byte("╭──╮ ✻ ⏺ ▐▛█\r\n"))

	got := term.String()
	for _, r := range []string{"╭", "─", "╮", "✻", "⏺", "▐", "▛", "█"} {
		if !strings.Contains(got, r) {
			t.Errorf("lost rune %q in render:\n%q", r, got)
		}
	}
}
