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

// Attribute bits mirrored from vt10x's state.go.
//
// Glyph.Mode is exported but the bits it holds are not, and a repaint has no
// other way to recover bold/underline/... The library has been frozen since
// 2022 so these are stable, but they must be rechecked against state.go if the
// dependency ever moves.
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
	// partial holds the leading bytes of a rune that the last write ended in
	// the middle of. See Write.
	partial []byte
}

// New returns a Screen sized to cols x rows.
func New(cols, rows int) *Screen {
	return &Screen{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// Write feeds agent output into the emulator. It never fails, and it always
// reports the whole slice consumed.
//
// Both of those matter. Reads off a PTY split wherever they like, so a
// multi-byte rune — every box-drawing and block character an agent's TUI is
// made of — regularly straddles two writes. vt10x handles that badly: it
// returns a short count with a nil error and drops the bytes it could not
// decode (vt_posix.go, the "not enough bytes for a full rune" branch).
//
// A short count with no error breaks the io.Writer contract, and the damage is
// not local: io.MultiWriter turns it into ErrShortWrite, io.Copy stops, nothing
// drains the PTY, and the agent blocks forever part-way through a frame. So
// Write splits on a rune boundary itself and carries the remainder into the
// next call.
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
// It deliberately ignores three bits. attrReverse is already applied: vt10x
// swaps FG and BG in the stored glyph as the cell is written (state.go,
// setChar) and leaves the bit set, so re-emitting SGR 7 would swap them back.
// attrGfx is also already applied — the glyph holds the translated line-drawing
// rune, not the ASCII that selected it. attrWrap is line-wrap bookkeeping and
// draws nothing.
//
// attrBold gets the same colour treatment (FG is promoted to its bright
// counterpart below 8) but bold is a font weight too, so it is kept; re-emitting
// SGR 1 over an already-bright colour is idempotent.
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
