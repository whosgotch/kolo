package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whosgotch/kolo/internal/term"
)

// replay renders a recording the way kolo does, so the detector is tested
// against a screen built from the agent's real byte stream rather than against
// the text dump sitting next to it.
func replay(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".raw"))
	if err != nil {
		t.Fatal(err)
	}
	screen := term.New(120, 40)
	// In chunks, because that is how the bytes arrive off a PTY.
	for i := 0; i < len(raw); i += 64 {
		screen.Write(raw[i:min(i+64, len(raw))])
	}
	return screen.Text()
}

func TestOfRecordings(t *testing.T) {
	tests := []struct {
		recording string
		want      State
	}{
		{"idle-prompt", Idle},
		{"permission-dialog", Dialog},
		{"trust-dialog", Dialog},
	}
	for _, tt := range tests {
		t.Run(tt.recording, func(t *testing.T) {
			if got := Of(replay(t, tt.recording)); got != tt.want {
				t.Errorf("Of(%s) = %s, want %s", tt.recording, got, tt.want)
			}
		})
	}
}

// TestUnrecognisedScreensHold is the property the whole package is for. A
// screen that means nothing to the detector must not read as safe.
func TestUnrecognisedScreensHold(t *testing.T) {
	screens := map[string]string{
		"empty":             "",
		"blank rows":        strings.Repeat(strings.Repeat(" ", 120)+"\n", 40),
		"another agent":     "$ some other tool\n> waiting for input\n",
		"partial repaint":   "╭─── Claude Code v2.1.227 ───╮\n│ Welcome back!              │\n",
		"agent output only": "Here is the answer to your question.\n",
	}
	for name, screen := range screens {
		t.Run(name, func(t *testing.T) {
			if got := Of(screen); got.CanSend() {
				t.Errorf("Of(%s) = %s, which allows sending; want it held", name, got)
			}
		})
	}
}

// TestDialogWinsOverIdle pins the tie-break. If both sets of markers are on
// screen at once, the answer must be the one that holds the queue.
func TestDialogWinsOverIdle(t *testing.T) {
	both := "Do you want to create note.txt?\n ❯ 1. Yes\n Esc to cancel\n ? for shortcuts\n"
	if got := Of(both); got != Dialog {
		t.Errorf("Of(both) = %s, want %s", got, Dialog)
	}
}

func TestOnlyIdleCanSend(t *testing.T) {
	for _, s := range []State{Unknown, Idle, Dialog} {
		if want := s == Idle; s.CanSend() != want {
			t.Errorf("%s.CanSend() = %v, want %v", s, s.CanSend(), want)
		}
	}
}
