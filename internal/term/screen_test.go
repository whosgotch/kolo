package term

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/hinshun/vt10x"
)

// cell writes seq followed by one character and returns the glyph it produced.
func cell(t *testing.T, seq string) vt10x.Glyph {
	t.Helper()
	s := New(10, 2)
	if _, err := s.Write([]byte(seq + "X")); err != nil {
		t.Fatalf("write: %v", err)
	}
	return s.term.Cell(0, 0)
}

// TestMirroredAttributes guards the constants copied out of vt10x's state.go.
// If the dependency ever moves and the bit order shifts, one attribute will
// read as another, so each case asserts the flags it does *not* set as well.
func TestMirroredAttributes(t *testing.T) {
	tests := []struct {
		name string
		sgr  string
		want style
	}{
		{"bold", "\x1b[1m", style{bold: true}},
		{"italic", "\x1b[3m", style{italic: true}},
		{"underline", "\x1b[4m", style{underline: true}},
		{"blink", "\x1b[5m", style{blink: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := styleOf(cell(t, tt.sgr))
			got.fg, got.bg = 0, 0 // colours are not under test here
			if got != tt.want {
				t.Errorf("styleOf(%q) = %+v, want %+v", tt.sgr, got, tt.want)
			}
		})
	}
}

// TestReverseIsAlreadyApplied pins the behaviour styleOf relies on: vt10x
// resolves reverse video into the stored colours, so a repaint must not emit
// SGR 7 on top of them.
func TestReverseIsAlreadyApplied(t *testing.T) {
	got := styleOf(cell(t, "\x1b[31;7m"))
	if got.fg != vt10x.DefaultBG || got.bg != vt10x.Red {
		t.Errorf("colours = fg %v, bg %v; want them swapped to fg %v, bg %v",
			got.fg, got.bg, vt10x.DefaultBG, vt10x.Red)
	}
	if (style{fg: got.fg, bg: got.bg}) != got {
		t.Errorf("styleOf reported drawable attributes %+v, want none", got)
	}
}

// TestBoldPromotesColour documents the other colour rewrite vt10x performs: a
// bold foreground below 8 is stored as its bright counterpart. Emitting SGR 1
// again over that colour is harmless, so styleOf keeps the bit.
func TestBoldPromotesColour(t *testing.T) {
	got := styleOf(cell(t, "\x1b[31;1m"))
	if got.fg != vt10x.LightRed {
		t.Errorf("fg = %v, want %v (Red promoted)", got.fg, vt10x.LightRed)
	}
	if !got.bold {
		t.Error("bold = false, want true")
	}
}

// TestGfxIsAlreadyTranslated pins the third bit styleOf drops: with the
// line-drawing charset selected, the glyph holds the translated rune, not the
// ASCII that selected it.
func TestGfxIsAlreadyTranslated(t *testing.T) {
	if got := cell(t, "\x1b(0q").Char; got != '─' {
		t.Errorf("Char = %q, want %q", got, '─')
	}
}

// TestWriteAcrossChunkBoundaries pins the two things Write guarantees: the
// whole slice is reported consumed, and it makes no difference where the
// caller's chunks happen to fall.
//
// Reads off a PTY split anywhere, so this is the common case rather than an
// edge one — every box-drawing character in an agent's TUI is three bytes. The
// round-trip tests all wrote their input in one call, which is exactly why they
// never caught the emulator dropping split runes.
func TestWriteAcrossChunkBoundaries(t *testing.T) {
	input := []byte("┌───┐\r\n│ ⠂⠐ │\r\n└───┘\r\n\x1b[1m✳ bold\x1b[0m ✓")

	whole := New(20, 6)
	if n, err := whole.Write(input); n != len(input) || err != nil {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(input))
	}
	want := whole.Snapshot()

	for _, size := range []int{1, 2, 3, 5, 8, 13} {
		t.Run(fmt.Sprintf("chunks of %d", size), func(t *testing.T) {
			s := New(20, 6)
			for i := 0; i < len(input); i += size {
				chunk := input[i:min(i+size, len(input))]
				n, err := s.Write(chunk)
				if n != len(chunk) || err != nil {
					t.Fatalf("Write(%d bytes) = %d, %v; want %d, nil", len(chunk), n, err, len(chunk))
				}
			}
			if got := s.Snapshot(); !bytes.Equal(got, want) {
				t.Errorf("screen differs from the same input written in one call\n got: %q\nwant: %q", got, want)
			}
		})
	}
}
