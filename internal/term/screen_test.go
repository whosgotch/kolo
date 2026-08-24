package term

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/hinshun/vt10x"
)

func cell(t *testing.T, seq string) vt10x.Glyph {
	t.Helper()
	s := New(10, 2)
	if _, err := s.Write([]byte(seq + "X")); err != nil {
		t.Fatalf("write: %v", err)
	}
	return s.term.Cell(0, 0)
}

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
			got.fg, got.bg = 0, 0
			if got != tt.want {
				t.Errorf("styleOf(%q) = %+v, want %+v", tt.sgr, got, tt.want)
			}
		})
	}
}

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

func TestBoldPromotesColour(t *testing.T) {
	got := styleOf(cell(t, "\x1b[31;1m"))
	if got.fg != vt10x.LightRed {
		t.Errorf("fg = %v, want %v (Red promoted)", got.fg, vt10x.LightRed)
	}
	if !got.bold {
		t.Error("bold = false, want true")
	}
}

func TestGfxIsAlreadyTranslated(t *testing.T) {
	if got := cell(t, "\x1b(0q").Char; got != '─' {
		t.Errorf("Char = %q, want %q", got, '─')
	}
}

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
