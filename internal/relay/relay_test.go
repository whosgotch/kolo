package relay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

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

// Real detector on purpose; a stubbed verdict would pass a relay reading screens wrong.
var screens = map[detect.State]string{
	detect.Idle:    "❯\n  ? for shortcuts\n",
	detect.Busy:    "✳ Levitating…\n❯\n  esc to interrupt\n",
	detect.Dialog:  " Do you want to create note.txt?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n",
	detect.Unknown: "some other tool\n",
}

func fixture(state detect.State) (*Relay, *recorder, func(detect.State)) {
	rec := &recorder{}
	current := screens[state]
	r := New(rec, func() (string, time.Duration) { return current, 0 }, adapter.For("claude"))
	if got := r.state(); got != state {
		panic("fixture screen for " + state.String() + " reads as " + got.String())
	}
	return r, rec, func(s detect.State) { current = screens[s] }
}

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
	if err := r.Type("x"); err != nil {
		t.Fatal(err)
	}
}

// A pasted prompt arrives as one message. It is the thing people do most
// here, and it used to be refused at 256 bytes with nobody told.
func TestAPastedPromptGoesThrough(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)

	paste := strings.Repeat("fix the flaky test in internal/hub. ", 400)
	if err := r.Type(paste); err != nil {
		t.Fatalf("a %d-byte paste was refused: %v", len(paste), err)
	}
	if got := strings.Join(rec.writes, ""); got != paste {
		t.Errorf("the agent got %d bytes of a %d-byte paste", len(got), len(paste))
	}
}

// Past the ceiling it is refused, and said so rather than dropped.
func TestAPasteTooBigSaysSo(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)

	err := r.Type(strings.Repeat("x", maxKeys+1))
	if !errors.Is(err, ErrTooMuch) {
		t.Errorf("refusing a paste past the ceiling gave %v, which nothing can tell apart", err)
	}
	if len(rec.writes) != 0 {
		t.Errorf("a refused paste still reached the agent: %v", rec.writes)
	}
}
