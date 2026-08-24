package term

import (
	"bytes"
	"fmt"

	"github.com/hinshun/vt10x"
)

// Snapshot renders the current screen as terminal bytes for a joining viewer.
// Rows are positioned absolutely, so replaying it cannot scroll.
func (s *Screen) Snapshot() []byte {
	s.term.Lock()
	defer s.term.Unlock()

	cols, rows := s.term.Size()
	var b bytes.Buffer

	// Only enter-alt-screen is emitted, never exit: a fresh terminal starts on
	// the normal screen, and vt10x turns a no-op exit into an enter (state.go).
	if s.term.Mode()&vt10x.ModeAltScreen != 0 {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[0m\x1b[2J\x1b[3J")

	cur := defaultStyle()
	for y := range rows {
		last := -1
		for x := range cols {
			if !isBlank(s.term.Cell(x, y)) {
				last = x
			}
		}
		if last < 0 {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H", y+1)
		for x := 0; x <= last; x++ {
			g := s.term.Cell(x, y)
			if st := styleOf(g); st != cur {
				writeSGR(&b, st)
				cur = st
			}
			if g.Char == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(g.Char)
			}
		}
	}

	b.WriteString("\x1b[0m")
	c := s.term.Cursor()
	fmt.Fprintf(&b, "\x1b[%d;%dH", c.Y+1, c.X+1)
	if s.term.CursorVisible() {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	return b.Bytes()
}

func defaultStyle() style {
	return style{fg: vt10x.DefaultFG, bg: vt10x.DefaultBG}
}

func isBlank(g vt10x.Glyph) bool {
	return (g.Char == ' ' || g.Char == 0) && styleOf(g) == defaultStyle()
}

// Reverse video is reconstructed, not read off the glyph: vt10x already swapped
// fg/bg (styleOf), and a default in the wrong slot is swapped back + SGR 7.
func writeSGR(b *bytes.Buffer, st style) {
	fg, bg := st.fg, st.bg
	reverse := fg == vt10x.DefaultBG || bg == vt10x.DefaultFG
	if reverse {
		fg, bg = bg, fg
	}

	b.WriteString("\x1b[0")
	if reverse {
		b.WriteString(";7")
	}
	if st.bold {
		b.WriteString(";1")
	}
	if st.italic {
		b.WriteString(";3")
	}
	if st.underline {
		b.WriteString(";4")
	}
	if st.blink {
		b.WriteString(";5")
	}
	writeColor(b, fg, 3)
	writeColor(b, bg, 4)
	b.WriteString("m")
}

// writeColor appends one SGR colour parameter; base is 3 (fg) or 4 (bg). vt10x
// packs 24-bit colour into palette space, so COLORTERM is scrubbed (internal/agent).
func writeColor(b *bytes.Buffer, c vt10x.Color, base int) {
	switch {
	case c >= 1<<24: // DefaultFG, DefaultBG, DefaultCursor
		fmt.Fprintf(b, ";%d9", base)
	case c < 8:
		fmt.Fprintf(b, ";%d%d", base, c)
	case c < 16:
		fmt.Fprintf(b, ";%d%d", base+6, c-8)
	case c < 256:
		fmt.Fprintf(b, ";%d8;5;%d", base, c)
	default:
		fmt.Fprintf(b, ";%d8;2;%d;%d;%d", base, c>>16&0xff, c>>8&0xff, c&0xff)
	}
}
