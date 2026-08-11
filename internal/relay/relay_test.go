package relay

import (
	"errors"
	"strings"
	"testing"

	"github.com/whosgotch/kolo/internal/detect"
)

// recorder stands in for the agent's input and remembers each write separately,
// which is the only way to tell a bundled write from two.
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

// fixture builds a relay whose agent state the test controls.
func fixture(state detect.State) (*Relay, *recorder, *detect.State) {
	rec := &recorder{}
	current := state
	return New(rec, func() detect.State { return current }), rec, &current
}

func TestHeldWhileADialogIsUp(t *testing.T) {
	r, rec, _ := fixture(detect.Dialog)
	if _, err := r.Submit("ada", "hi everyone, what are we working on?"); err != nil {
		t.Fatal(err)
	}

	sent, err := r.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if sent != nil {
		t.Errorf("sent %q while a dialog was up", sent.Line())
	}
	if len(rec.writes) != 0 {
		t.Errorf("wrote %q to the agent while a dialog was up", rec.writes)
	}
	if len(r.Pending()) != 1 {
		t.Error("message was dropped rather than held")
	}
}

// TestHeldWhenTheScreenIsUnrecognised is the same guarantee for the case that
// actually protects the host: not a dialog we spotted, but a screen we did not
// understand at all.
func TestHeldWhenTheScreenIsUnrecognised(t *testing.T) {
	r, rec, _ := fixture(detect.Unknown)
	r.Submit("ada", "hello")

	if sent, _ := r.Tick(); sent != nil || len(rec.writes) != 0 {
		t.Errorf("sent %v / wrote %q on an unrecognised screen", sent, rec.writes)
	}
}

func TestReleasedWhenIdle(t *testing.T) {
	r, _, _ := fixture(detect.Idle)
	r.Submit("ada", "what does this function do?")

	sent, err := r.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if sent == nil {
		t.Fatal("nothing sent while idle")
	}
	if len(r.Pending()) != 0 {
		t.Error("message still queued after being sent")
	}
	if want := "ada: what does this function do?"; sent.Line() != want {
		t.Errorf("line = %q, want %q", sent.Line(), want)
	}
}

// TestEnterIsASeparateWrite pins findings #3. Bundled with the text, the Enter
// is read as part of a paste and the line is never submitted.
func TestEnterIsASeparateWrite(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)
	r.Submit("ada", "hello")
	if _, err := r.Tick(); err != nil {
		t.Fatal(err)
	}

	if len(rec.writes) != 2 {
		t.Fatalf("wrote %d times: %q; want the text and the Enter separately", len(rec.writes), rec.writes)
	}
	if rec.writes[0] != "ada: hello" {
		t.Errorf("first write = %q, want the line alone", rec.writes[0])
	}
	if rec.writes[1] != "\r" {
		t.Errorf("second write = %q, want the Enter alone", rec.writes[1])
	}
}

// TestOneMessagePerTick keeps the queue from emptying itself on a single
// reading of the screen: sending the first line stops the agent being idle.
func TestOneMessagePerTick(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)
	r.Submit("ada", "first")
	r.Submit("bob", "second")

	if _, err := r.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(r.Pending()) != 1 {
		t.Errorf("queue has %d left, want the second message still waiting", len(r.Pending()))
	}
	for _, w := range rec.writes {
		if strings.Contains(w, "second") {
			t.Error("second message was sent in the same tick as the first")
		}
	}
}

func TestQueueSurvivesUntilTheAgentIsReady(t *testing.T) {
	r, _, state := fixture(detect.Dialog)
	r.Submit("ada", "first")
	r.Submit("bob", "second")

	r.Tick()
	if len(r.Pending()) != 2 {
		t.Fatalf("queue lost messages while held: %v", r.Pending())
	}

	*state = detect.Idle
	first, _ := r.Tick()
	second, _ := r.Tick()
	if first == nil || second == nil {
		t.Fatal("messages were not released once the agent was idle")
	}
	if first.Nickname != "ada" || second.Nickname != "bob" {
		t.Errorf("released out of order: %s then %s", first.Nickname, second.Nickname)
	}
}

func TestFailedWriteKeepsTheMessage(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)
	r.Submit("ada", "hello")
	rec.err = errors.New("agent is gone")

	if _, err := r.Tick(); err == nil {
		t.Fatal("Tick reported success on a failed write")
	}
	if len(r.Pending()) != 1 {
		t.Error("message was dropped after a failed write")
	}
}

// TestCleanStripsEverythingButText is the boundary a guest cannot cross. Each
// case is a keystroke rather than a character: text is all a guest may send.
func TestCleanStripsEverythingButText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "what does this do?", "what does this do?"},
		{"carriage return", "yes\rand more", "yes and more"},
		{"newline", "one\ntwo", "one two"},
		{"escape", "hi\x1bthere", "hithere"},
		{"arrow key", "\x1b[Bpick the second option", "[Bpick the second option"},
		{"bell", "wake up\x07", "wake up"},
		{"backspace", "oops\x08", "oops"},
		{"delete", "gone\x7f", "gone"},
		{"c1 control", "hithere", "hithere"},
		{"bidi override", "safe‮gnorw", "safegnorw"},
		{"zero width", "a​b", "ab"},
		{"collapses spacing", "  too   much\t space  ", "too much space"},
		{"keeps unicode", "héllo ✳ 世界", "héllo ✳ 世界"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clean(tt.input, maxText); got != tt.want {
				t.Errorf("clean(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestEscapeSequencesCannotReachTheAgent checks the same thing where it counts:
// nothing a guest submits can arrive as a control character.
func TestEscapeSequencesCannotReachTheAgent(t *testing.T) {
	r, rec, _ := fixture(detect.Idle)
	r.Submit("ada\x1b[A", "approve it\r\x1b[B\r")
	if _, err := r.Tick(); err != nil {
		t.Fatal(err)
	}

	if len(rec.writes) != 2 {
		t.Fatalf("writes = %q", rec.writes)
	}
	for _, r := range rec.writes[0] {
		if r < 0x20 || r == 0x7f {
			t.Errorf("control character %q reached the agent in %q", r, rec.writes[0])
		}
	}
}

func TestSubmitRejectsEmptyMessages(t *testing.T) {
	r, _, _ := fixture(detect.Idle)
	for _, tt := range []struct{ nick, text string }{
		{"", "hello"},
		{"ada", ""},
		{"ada", "\x1b\x07"},
		{"\x1b", "hello"},
	} {
		if _, err := r.Submit(tt.nick, tt.text); err == nil {
			t.Errorf("Submit(%q, %q) was accepted", tt.nick, tt.text)
		}
	}
	if len(r.Pending()) != 0 {
		t.Error("a rejected message was queued anyway")
	}
}

func TestLongMessagesAreCut(t *testing.T) {
	r, _, _ := fixture(detect.Idle)
	m, err := r.Submit("ada", strings.Repeat("é", maxText))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Text) > maxText {
		t.Errorf("text is %d bytes, want at most %d", len(m.Text), maxText)
	}
	if !strings.HasPrefix(m.Text, "é") || strings.ContainsRune(m.Text, '�') {
		t.Error("text was cut in the middle of a rune")
	}
}
