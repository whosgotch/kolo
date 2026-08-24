// Package term keeps an authoritative model of the agent's screen.
package term

import (
	"sync"
	"unicode/utf8"

	"github.com/hinshun/vt10x"
)

// Attribute bits mirrored from vt10x's state.go; Glyph.Mode exports the field
// but not these bits. Recheck against state.go if the dependency moves.
const (
	attrReverse = 1 << iota
	attrUnderline
	attrBold
	attrGfx
	attrItalic
	attrBlink
	attrWrap
)

type Screen struct {
	term vt10x.Terminal

	mu sync.Mutex
	// Leading bytes of a rune the last write ended mid-way. See Write.
	partial []byte
}

func New(cols, rows int) *Screen {
	return &Screen{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// Write never fails and always reports the whole slice consumed: vt10x drops an
// incomplete rune at a PTY read boundary (vt_posix.go), breaking io.Copy.
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

// incompleteTail returns how many bytes at the end of b begin an incomplete rune.
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

func (s *Screen) Resize(cols, rows int) { s.term.Resize(cols, rows) }

func (s *Screen) Size() (cols, rows int) { return s.term.Size() }

// Text is the screen as plain rows, one per line.
func (s *Screen) Text() string { return s.term.String() }

type style struct {
	fg, bg    vt10x.Color
	bold      bool
	italic    bool
	underline bool
	blink     bool
}

// styleOf skips attrReverse and attrGfx: vt10x already applies both to the
// stored glyph, so re-emitting them would double-apply. attrWrap draws nothing.
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
