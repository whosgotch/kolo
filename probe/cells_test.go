package main

import (
	"testing"

	"github.com/hinshun/vt10x"
)

// vt10x keeps its glyph attribute bits unexported (state.go), so a consumer has
// to mirror them. They are stable: the library has not changed since 2022.
const (
	attrReverse int16 = 1 << iota
	attrUnderline
	attrBold
	attrGfx
	attrItalic
	attrBlink
	attrWrap
)

// TestVT10xCellAttributes checks colour and style survive per cell even though
// String() is plain text. Kolo broadcasts a grid built from cells, so this is
// the access path that actually matters.
func TestVT10xCellAttributes(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(20, 2))
	term.Write([]byte("\x1b[31mR\x1b[0m\x1b[1;38;5;220mY\x1b[0m"))

	red := term.Cell(0, 0)
	if red.Char != 'R' {
		t.Fatalf("cell 0 char = %q, want 'R'", red.Char)
	}
	if red.FG == vt10x.DefaultFG {
		t.Errorf("cell 0 lost its foreground colour: %+v", red)
	}

	yellow := term.Cell(1, 0)
	if yellow.Char != 'Y' {
		t.Fatalf("cell 1 char = %q, want 'Y'", yellow.Char)
	}
	if yellow.FG == vt10x.DefaultFG {
		t.Errorf("cell 1 lost its 256-colour foreground: %+v", yellow)
	}
	if yellow.Mode&attrBold == 0 {
		t.Errorf("cell 1 lost bold: %+v", yellow)
	}

	t.Logf("red=%+v yellow=%+v", red, yellow)
}
