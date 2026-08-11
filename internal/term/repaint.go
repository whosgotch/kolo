package term

import (
	"bytes"
	"fmt"

	"github.com/hinshun/vt10x"
)

// Snapshot returns a byte sequence that reproduces the current screen on a
// terminal that has not seen any of the session so far.
//
// It is what a joining viewer receives before the live stream starts: xterm.js
// in the browser renders the agent's raw output, so the catch-up has to be
// expressed in the same language rather than as a separate grid format.
//
// Rows are placed with absolute cursor positioning, so nothing here can scroll
// the screen, and trailing blank cells are left unwritten.
func (s *Screen) Snapshot() []byte {
	s.term.Lock()
	defer s.term.Unlock()

	cols, rows := s.term.Size()
	var b bytes.Buffer

	// The agent's TUI runs on the alternate screen once it is up, but not at
	// the startup prompts (docs/probe-findings.md, incidental #3).
	//
	// Only the enter is emitted. A snapshot is written to a terminal that has
	// just connected and is therefore on the normal screen already, so the
	// reset would be a no-op on a real terminal — and vt10x, which the
	// round-trip test replays into, mishandles it by swapping *into* the
	// alternate screen (state.go, the !set || !alt condition).
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

// isBlank reports whether a cell would look identical to one the erase left
// behind, and so can be skipped. A space with a non-default background is not
// blank: the colour is visible.
func isBlank(g vt10x.Glyph) bool {
	return (g.Char == ' ' || g.Char == 0) && styleOf(g) == defaultStyle()
}

// writeSGR emits a full attribute set, leading with a reset, rather than the
// difference from the previous one. A snapshot is written once per viewer, so
// the redundancy costs nothing worth saving and removes a class of bug.
//
// Reverse video is reconstructed here rather than read from the glyph, because
// vt10x resolves it into the stored colours (see styleOf). Usually that is
// lossless — a swapped pair of concrete colours is just a pair of colours — but
// the two defaults have no direct spelling in the slot they were swapped into:
// there is no "default background, as a foreground". Wherever a default lands
// in the wrong slot, the pair is swapped back and SGR 7 restores it.
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

// writeColor appends one SGR colour parameter. base is 3 for a foreground and 4
// for a background, which also yields the bright (90/100) and extended (38/48)
// forms by arithmetic.
//
// vt10x packs a 24-bit colour into the same uint32 as a palette index, so 200
// and rgb(0,0,200) are indistinguishable and are both read as palette 200. The
// runner avoids the ambiguity at the source by scrubbing COLORTERM, which keeps
// the agent on 256 colours.
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
