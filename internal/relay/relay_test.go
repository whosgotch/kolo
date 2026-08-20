package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

// recorder stands in for the agent's input and remembers each write separately,
// which is the only way to tell one write from two.
type recorder struct {
	writes []string
	err    error
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.writes = append(r.writes, string(p))
	return len(p), nil
}

// The relay is given a screen rather than a verdict, so these go through the real
// detector: stubbing the verdict would pass with a relay that reads screens wrong.
var screens = map[detect.State]string{
	detect.Idle:    "❯\n  ? for shortcuts\n",
	detect.Busy:    "✳ Levitating…\n❯\n  esc to interrupt\n",
	detect.Dialog:  " Do you want to create note.txt?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n",
	detect.Unknown: "some other tool\n",
}

// fixture builds a relay whose screen the test controls, and returns the way to
// change it.
func fixture(state detect.State) (*Relay, *recorder, func(detect.State)) {
	rec := &recorder{}
	current := screens[state]
	// Claude Code's idle is a thing its screen says, so the stillness the relay is
	// given never decides anything here; a kind whose idle is silence is exercised
	// in TestSilenceIsIdleOnlyForAKindThatSaysSo.
	r := New(rec, func() (string, time.Duration) { return current, 0 }, adapter.For("claude"))
	if got := r.state(); got != state {
		panic("fixture screen for " + state.String() + " reads as " + got.String())
	}
	return r, rec, func(s detect.State) { current = screens[s] }
}

// TestKeystrokesGoThroughUntouched is the change of shape. What a member presses
// is what the agent gets: no line assembled for them, no moment picked for them,
// and nothing stripped out — an escape sequence is a keypress, not an attack.
func TestKeystrokesGoThroughUntouched(t *testing.T) {
	for _, keys := range []string{"a", "\x1b", "\x1b[B", "\r", "/clear"} {
		t.Run(strings.ToValidUTF8(keys, "?"), func(t *testing.T) {
			r, rec, _ := fixture(detect.Idle)
			if err := r.Type(keys); err != nil {
				t.Fatal(err)
			}
			if len(rec.writes) != 1 || rec.writes[0] != keys {
				t.Errorf("writes = %q, want exactly %q", rec.writes, keys)
			}
		})
	}
}

// TestKeystrokesNeedNoParticularScreen: the member is looking at the screen, so
// there is nothing for kolo to protect them from. Every state takes keys.
func TestKeystrokesNeedNoParticularScreen(t *testing.T) {
	for _, state := range []detect.State{detect.Idle, detect.Busy, detect.Dialog, detect.Unknown} {
		t.Run(state.String(), func(t *testing.T) {
			r, rec, _ := fixture(state)
			if err := r.Type("x"); err != nil {
				t.Fatalf("refused a keystroke while %s: %v", state, err)
			}
			if len(rec.writes) != 1 {
				t.Errorf("writes = %q", rec.writes)
			}
		})
	}
}

// TestAFloodIsNotAKeystroke keeps out the one thing a keystroke channel should
// not carry: somebody piping a file into an agent one frame at a time.
func TestAFloodIsNotAKeystroke(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)
	if err := r.Type(strings.Repeat("x", maxKeys+1)); err == nil {
		t.Error("a flood was accepted as a keystroke")
	}
	if len(rec.writes) != 0 {
		t.Errorf("wrote %q", rec.writes)
	}
}

func TestInterruptOnlyWhileWorking(t *testing.T) {
	for _, state := range []detect.State{detect.Idle, detect.Dialog, detect.Unknown} {
		t.Run("refused when "+state.String(), func(t *testing.T) {
			r, rec, _ := fixture(state)
			if err := r.Interrupt(); err == nil {
				t.Error("the interrupt was accepted")
			}
			if len(rec.writes) != 0 {
				t.Errorf("wrote %q to the agent", rec.writes)
			}
		})
	}

	r, rec, _ := fixture(detect.Busy)
	if err := r.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if len(rec.writes) != 1 || rec.writes[0] != "\x1b" {
		t.Errorf("writes = %q, want one Esc", rec.writes)
	}
}

// TestTheInterruptKeyIsTheKindsOwn: Esc was sent to every agent there was, and
// it is the key that clears the input box of one that stops on Ctrl-C. What is
// pressed is now the kind's, and it is pressed under the same rule — only while
// the agent is working.
func TestTheInterruptKeyIsTheKindsOwn(t *testing.T) {
	rec := &recorder{}
	stops := adapter.Adapter{
		Markers:   adapter.For("claude").Markers,
		Interrupt: "ctrl+c",
	}
	r := New(rec, func() (string, time.Duration) { return screens[detect.Busy], 0 }, stops)

	if err := r.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if len(rec.writes) != 1 || rec.writes[0] != "\x03" {
		t.Errorf("writes = %q, want one Ctrl-C", rec.writes)
	}
}

// TestSilenceIsIdleOnlyForAKindThatSaysSo: a kind whose idle is silence reads as
// idle once the screen has stopped moving (docs/probe-findings.md #6). That
// reading is now what the room is shown rather than permission to type, since
// kolo types nothing on its own.
func TestSilenceIsIdleOnlyForAKindThatSaysSo(t *testing.T) {
	settling := adapter.Adapter{Markers: detect.Markers{
		Busy:   "esc to interrupt",
		Settle: 2 * time.Second,
	}}
	quiet := "1. One, starting steadily.\n› \n"

	for _, tc := range []struct {
		name  string
		still time.Duration
		want  detect.State
	}{
		{"still moving", time.Second, detect.Unknown},
		{"settled", 3 * time.Second, detect.Idle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(&recorder{}, func() (string, time.Duration) { return quiet, tc.still }, settling)
			if got := r.state(); got != tc.want {
				t.Errorf("state after %s of quiet = %s, want %s", tc.still, got, tc.want)
			}
		})
	}
}

// TestOneWriteAtATime is the rule that outlived the queue: two writers to one
// terminal interleave, and a keystroke landing inside an answer is a keypress
// nobody made.
func TestOneWriteAtATime(t *testing.T) {
	r, _, _ := fixture(detect.Busy)
	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- r.exclusive(func() error {
			close(started)
			time.Sleep(50 * time.Millisecond)
			return nil
		})
	}()
	<-started

	if err := r.Type("x"); err == nil {
		t.Error("a keystroke landed while something else was being written")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// And once it is over, the keyboard works again.
	if err := r.Type("x"); err != nil {
		t.Fatal(err)
	}
}
