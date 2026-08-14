// Package term keeps an authoritative model of the agent's screen.
//
// The browser renders with xterm.js, fed the agent's raw output byte for byte,
// so this emulator is not a renderer. Its only job is to answer "what is on
// screen right now" so that a viewer joining mid-session can be repainted
// before the live stream starts.
//
// The emulator is github.com/hinshun/vt10x. See docs/probe-findings.md #1 for
// why not github.com/charmbracelet/x/vt.
package term

import (
	"sync"
	"unicode/utf8"

	"github.com/hinshun/vt10x"
)

// Attribute bits mirrored from vt10x's state.go, because Glyph.Mode is exported
// but the bits it holds are not. Frozen since 2022, so stable — but recheck
// against state.go if the dependency ever moves.
const (
	attrReverse = 1 << iota
	attrUnderline
	attrBold
	attrGfx
	attrItalic
	attrBlink
	attrWrap
)

// Screen is a virtual terminal fed from the agent's PTY.
type Screen struct {
	term vt10x.Terminal

	mu sync.Mutex
	// Leading bytes of a rune the last write ended in the middle of. See Write.
	partial []byte
}

// New returns a Screen sized to cols x rows.
func New(cols, rows int) *Screen {
	return &Screen{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// Write feeds agent output into the emulator. It never fails, and always reports
// the whole slice consumed.
//
// Both matter. PTY reads split anywhere, so a multi-byte rune regularly straddles
// two writes, and vt10x returns a short count with a nil error and drops what it
// could not decode (vt_posix.go, "not enough bytes for a full rune"). That breaks
// io.Writer: io.MultiWriter turns it into ErrShortWrite, io.Copy stops, nothing
// drains the PTY, and the agent blocks forever. So Write splits on a rune
// boundary itself and carries the remainder into the next call.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf := p
	if len(s.partial) > 0 {
		buf = append(s.partial, p...)
		s.partial = nil
	}
	tail := incompleteTail(buf)
	if body := buf[:len(buf)-tail]; len(body) > 0 {
		s.term.Write(body)
	}
	if tail > 0 {
		s.partial = append([]byte(nil), buf[len(buf)-tail:]...)
	}
	return len(p), nil
}

// incompleteTail returns how many bytes at the end of b begin a rune that is
// not all there yet, and so must wait for the rest.
func incompleteTail(b []byte) int {
	for i := 1; i <= utf8.UTFMax-1 && i <= len(b); i++ {
		c := b[len(b)-i]
		if !utf8.RuneStart(c) {
			continue
		}
		if r, size := utf8.DecodeRune(b[len(b)-i:]); r == utf8.RuneError && size <= 1 {
			return i
		}
		return 0
	}
	return 0
}

// Resize changes the screen size, following the host's terminal.
func (s *Screen) Resize(cols, rows int) { s.term.Resize(cols, rows) }

// Size returns the current screen size.
func (s *Screen) Size() (cols, rows int) { return s.term.Size() }

// Text returns the screen as plain rows, one per line, with no styling. It is
// what a detector matches against and what a recording dumps for review.
func (s *Screen) Text() string { return s.term.String() }

// style is the drawable part of a glyph: everything a repaint must restate when
// it moves to a cell.
type style struct {
	fg, bg    vt10x.Color
	bold      bool
	italic    bool
	underline bool
	blink     bool
}

// styleOf reads the attributes a repaint has to reproduce.
//
// Three bits are ignored deliberately. attrReverse is already applied — vt10x
// swaps FG and BG in the stored glyph (state.go, setChar) and leaves the bit set,
// so re-emitting SGR 7 would swap them back. attrGfx is likewise applied: the
// glyph holds the translated rune. attrWrap draws nothing.
//
// attrBold gets the same colour treatment but is a font weight too, so it is
// kept; re-emitting SGR 1 over an already-bright colour is idempotent.
func styleOf(g vt10x.Glyph) style {
	return style{
		fg:        g.FG,
		bg:        g.BG,
		bold:      g.Mode&attrBold != 0,
		italic:    g.Mode&attrItalic != 0,
		underline: g.Mode&attrUnderline != 0,
		blink:     g.Mode&attrBlink != 0,
	}
}
