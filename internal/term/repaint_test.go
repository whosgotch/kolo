package term

import (
	"fmt"
	"os"
	"testing"

	"github.com/hinshun/vt10x"
)

// roundTrip feeds input into one screen, repaints a second screen from the
// first one's snapshot, and reports every difference between them.
//
// The assertion is a round trip rather than a golden file on purpose: a golden
// file pins whatever bytes the encoder happens to emit today, including wrong
// ones, while this pins the only property that matters — a viewer joining now
// sees what the host sees.
func roundTrip(t *testing.T, cols, rows int, input []byte) {
	t.Helper()

	live := New(cols, rows)
	if _, err := live.Write(input); err != nil {
		t.Fatalf("write: %v", err)
	}
	joined := New(cols, rows)
	if _, err := joined.Write(live.Snapshot()); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	for y := range rows {
		for x := range cols {
			want, got := live.term.Cell(x, y), joined.term.Cell(x, y)
			if normalize(want) == normalize(got) {
				continue
			}
			t.Errorf("cell (%d,%d) = %s, want %s", x, y, describe(normalize(got)), describe(normalize(want)))
		}
	}
	if want, got := live.term.Cursor(), joined.term.Cursor(); want.X != got.X || want.Y != got.Y {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", got.X, got.Y, want.X, want.Y)
	}
	if want, got := live.term.CursorVisible(), joined.term.CursorVisible(); want != got {
		t.Errorf("cursor visible = %v, want %v", got, want)
	}
	if want, got := live.term.Mode()&vt10x.ModeAltScreen, joined.term.Mode()&vt10x.ModeAltScreen; want != got {
		t.Errorf("alternate screen = %v, want %v", got != 0, want != 0)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"plain text", "hello\r\nworld\r\n"},
		{"attributes", "\x1b[1mbold\x1b[0m \x1b[3mital\x1b[0m \x1b[4munder\x1b[0m \x1b[5mblink\x1b[0m"},
		{"ansi colours", "\x1b[31;42mred on green\x1b[0m normal"},
		{"bright colours", "\x1b[91;104mbright\x1b[0m"},
		{"256 colours", "\x1b[38;5;208;48;5;17mextended\x1b[0m"},
		{"reverse video", "\x1b[7mreversed\x1b[0m tail"},
		{"reverse with one colour", "\x1b[31;7mred reversed\x1b[0m"},
		{"reverse with both colours", "\x1b[31;44;7mboth\x1b[0m"},
		// Trailing blanks are skipped by the encoder, but only when they carry
		// no colour of their own.
		{"coloured trailing blanks", "\x1b[41m" + spaces(20) + "\x1b[0m"},
		// The agent's TUI draws in these; see docs/probe-findings.md #1.
		{"box drawing and braille", "┌────┐\r\n│ ⠂⠐ │\r\n└────┘\r\n"},
		{"line drawing charset", "\x1b(0qqqwqqq\x1b(B done"},
		{"alternate screen", "before\x1b[?1049hinside the TUI\r\nsecond line"},
		{"hidden cursor", "text\x1b[?25l"},
		{"cursor parked mid screen", "\x1b[10;20Hx\x1b[5;3H"},
		{"full last row", "\x1b[40;1H" + repeat("z", 120)},
		// The window title Claude Code sets on every frame. It must stay off
		// screen; the emulator this replaced leaked it (findings #1).
		{"osc title", "visible\x1b]0;✳ hidden title\x07 tail"},
		{"scrolled past the bottom", repeat("filler line\r\n", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTrip(t, 120, 40, []byte(tt.input))
		})
	}
}

// TestSnapshotRoundTripCapture replays a real recording, which exercises
// sequence combinations no handwritten case will think of. Recordings are not
// committed — they contain live session content — so point KOLO_RAW at one the
// Milestone 0 probe produced:
//
//	KOLO_RAW=/tmp/out/raw.log go test ./internal/term -run Capture -v
func TestSnapshotRoundTripCapture(t *testing.T) {
	path := os.Getenv("KOLO_RAW")
	if path == "" {
		t.Skip("set KOLO_RAW to a raw PTY capture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, 120, 40, raw)
}

func spaces(n int) string { return repeat(" ", n) }

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

// cellView is a glyph reduced to what a viewer can actually see, so that cells
// differing only in bits the encoder deliberately drops compare equal.
type cellView struct {
	ch rune
	st style
}

func normalize(g vt10x.Glyph) cellView {
	ch := g.Char
	if ch == 0 {
		ch = ' '
	}
	return cellView{ch: ch, st: styleOf(g)}
}

func describe(c cellView) string {
	return fmt.Sprintf("%q fg=%d bg=%d %+v", c.ch, c.st.fg, c.st.bg, struct {
		B, I, U, K bool
	}{c.st.bold, c.st.italic, c.st.underline, c.st.blink})
}
