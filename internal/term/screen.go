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

import "github.com/hinshun/vt10x"

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
}

// New returns a Screen sized to cols x rows.
func New(cols, rows int) *Screen {
	return &Screen{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// Write feeds agent output into the emulator. It never fails.
func (s *Screen) Write(p []byte) (int, error) { return s.term.Write(p) }

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
