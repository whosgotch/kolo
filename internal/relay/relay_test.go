package relay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
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

// TestHeldWhenTheScreenIsUnrecognised is the case that actually protects the
// host: not a dialog we spotted, but a screen we did not understand at all.
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
//
// See TestEnterWaitsForTheAgentToCatchUp for the other half of it.
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

// TestEnterWaitsForTheAgentToCatchUp pins findings #8, which the two-write rule
// on its own did not cover: an Enter hard behind the text is still inside the
// agent's paste window and lands as a newline, leaving the line in the box.
//
// The recorder that proved the two-write rule paused between them; the relay did
// not, and the message that did not submit was the difference.
func TestEnterWaitsForTheAgentToCatchUp(t *testing.T) {
	was := enterDelay
	enterDelay = 40 * time.Millisecond
	defer func() { enterDelay = was }()

	r, rec, _ := fixture(detect.Idle)
	r.Submit("ada", "hello")

	start := time.Now()
	if _, err := r.Tick(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < enterDelay {
		t.Errorf("line and Enter went %s apart, want at least %s", took, enterDelay)
	}
	if len(rec.writes) != 2 || rec.writes[1] != "\r" {
		t.Errorf("writes = %q, want the line then the Enter", rec.writes)
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
	r, _, becomes := fixture(detect.Dialog)
	r.Submit("ada", "first")
	r.Submit("bob", "second")

	r.Tick()
	if len(r.Pending()) != 2 {
		t.Fatalf("queue lost messages while held: %v", r.Pending())
	}

	becomes(detect.Idle)
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
// case is a keystroke rather than a character.
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

func TestAnswerPressesTheNumber(t *testing.T) {
	r, rec, _ := fixture(detect.Dialog)
	if err := r.Answer(2, "No"); err != nil {
		t.Fatal(err)
	}
	if len(rec.writes) != 1 || rec.writes[0] != "2" {
		t.Errorf("writes = %q, want one write of %q", rec.writes, "2")
	}
}

// TestAnswerNeedsTheQuestionItWasGiven is what makes answering from a browser
// safe. Between reading the screen and clicking, the dialog may have been
// replaced by the next one — same numbers, different meaning.
func TestAnswerNeedsTheQuestionItWasGiven(t *testing.T) {
	tests := []struct {
		name   string
		state  detect.State
		number int
		label  string
	}{
		{"the label has changed", detect.Dialog, 1, "Yes, allow all edits during this session"},
		{"the number is not offered", detect.Dialog, 3, "No"},
		{"a number that is two keystrokes", detect.Dialog, 12, "No"},
		{"the question has gone", detect.Idle, 1, "Yes"},
		{"the agent is working", detect.Busy, 1, "Yes"},
		{"the screen is unrecognised", detect.Unknown, 1, "Yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, rec, _ := fixture(tt.state)
			if err := r.Answer(tt.number, tt.label); err == nil {
				t.Error("the answer was accepted")
			}
			if len(rec.writes) != 0 {
				t.Errorf("wrote %q to the agent", rec.writes)
			}
		})
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

// TestOptionsAreOnlyOfferedForAQuestion keeps a page from showing choices for a
// screen that has moved on.
func TestOptionsAreOnlyOfferedForAQuestion(t *testing.T) {
	if r, _, _ := fixture(detect.Dialog); len(r.Options()) != 2 {
		t.Errorf("Options() = %v, want the two on screen", r.Options())
	}
	for _, state := range []detect.State{detect.Idle, detect.Busy, detect.Unknown} {
		if r, _, _ := fixture(state); len(r.Options()) != 0 {
			t.Errorf("Options() offered choices while %s", state)
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

// TestACommandGoesUnattributed is why the exception exists. Attributed, the
// sigil leaves the first column, the agent's CLI stops reading it as a command,
// and a member who typed /clear has said "ada: /clear" to the model instead.
func TestACommandGoesUnattributed(t *testing.T) {
	for _, text := range []string{"/clear", "!ls -la", "#remember the release chore"} {
		t.Run(text, func(t *testing.T) {
			r, rec, _ := fixture(detect.Idle)
			m, err := r.Submit("ada", text)
			if err != nil {
				t.Fatal(err)
			}
			if !m.Command {
				t.Fatal("not read as a command")
			}
			if _, err := r.Tick(); err != nil {
				t.Fatal(err)
			}
			if rec.writes[0] != text {
				t.Errorf("wrote %q, want it exactly as typed", rec.writes[0])
			}
			// Still two writes: the Enter is separate for a command as well,
			// because what makes it separate is the terminal, not the content.
			if len(rec.writes) != 2 || rec.writes[1] != "\r" {
				t.Errorf("wrote %q; want the line and the Enter apart", rec.writes)
			}
		})
	}
}

// TestAKindWithNoSigilsHasNoCommands: the sigils belong to the agent's CLI, so
// a kind kolo has no adapter for has none. Everything it is sent is a message,
// attributed like any other — which is the safe way round, since an unattributed
// line to a CLI kolo cannot read is a line nobody can be held to.
func TestAKindWithNoSigilsHasNoCommands(t *testing.T) {
	r := New(&recorder{}, func() (string, time.Duration) { return screens[detect.Idle], 0 }, adapter.For("some-other-agent"))
	m, err := r.Submit("ada", "/clear")
	if err != nil {
		t.Fatal(err)
	}
	if m.Command {
		t.Error("read a command for a CLI kolo knows nothing about")
	}
	if m.Line() != "ada: /clear" {
		t.Errorf("line = %q, want it attributed", m.Line())
	}
}

// TestOnlyTheFirstCharacterMakesACommand: prose is attributed, and prose is
// most of what anyone sends. A path or a shell line quoted mid-sentence is not
// an instruction to the CLI.
func TestOnlyTheFirstCharacterMakesACommand(t *testing.T) {
	for _, text := range []string{
		"clear the /tmp directory",
		"what does !important mean in css?",
		"the tag is #release",
	} {
		t.Run(text, func(t *testing.T) {
			r, rec, _ := fixture(detect.Idle)
			m, err := r.Submit("ada", text)
			if err != nil {
				t.Fatal(err)
			}
			if m.Command {
				t.Fatal("read as a command")
			}
			if _, err := r.Tick(); err != nil {
				t.Fatal(err)
			}
			if want := "ada: " + text; rec.writes[0] != want {
				t.Errorf("wrote %q, want %q", rec.writes[0], want)
			}
		})
	}
}

// TestACommandIsStillHeldByTheGate: nothing about being a command lets a line
// past the screen. Sent while the agent is asking a question, its Enter answers
// the question — which is the whole reason the queue exists.
func TestACommandIsStillHeldByTheGate(t *testing.T) {
	r, rec, idle := fixture(detect.Dialog)
	if _, err := r.Submit("ada", "/clear"); err != nil {
		t.Fatal(err)
	}
	if m, err := r.Tick(); err != nil || m != nil {
		t.Fatalf("released at a dialog: %v, %v", m, err)
	}
	if len(rec.writes) != 0 {
		t.Fatalf("wrote %q at a dialog", rec.writes)
	}

	idle(detect.Idle)
	if m, err := r.Tick(); err != nil || m == nil {
		t.Fatalf("not released once idle: %v, %v", m, err)
	}
}

// TestPaddingDoesNotHideACommand: clean() collapses what surrounds a line, so a
// pasted command with a space in front of it is still a command rather than
// prose that happens to start with a slash.
func TestPaddingDoesNotHideACommand(t *testing.T) {
	r, _, _ := fixture(detect.Idle)
	m, err := r.Submit("ada", "  /clear  ")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Command || m.Line() != "/clear" {
		t.Errorf("line = %q, command = %v", m.Line(), m.Command)
	}
}

// TestSilenceIsIdleOnlyForAKindThatSaysSo: the queue asks its kind, and a kind
// whose idle is silence is released by the screen having stopped moving rather
// than by anything the screen says (docs/probe-findings.md #6).
func TestSilenceIsIdleOnlyForAKindThatSaysSo(t *testing.T) {
	settling := adapter.Adapter{Markers: detect.Markers{
		Busy:   "esc to interrupt",
		Settle: 2 * time.Second,
	}}
	quiet := "1. One, starting steadily.\n› \n"

	for _, tc := range []struct {
		name  string
		still time.Duration
		want  bool
	}{
		{"still moving", time.Second, false},
		{"settled", 3 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			r := New(rec, func() (string, time.Duration) { return quiet, tc.still }, settling)
			r.Submit("ada", "hello")

			sent, err := r.Tick()
			if err != nil {
				t.Fatal(err)
			}
			if (sent != nil) != tc.want {
				t.Errorf("sent = %v after %s of quiet, want %v", sent != nil, tc.still, tc.want)
			}
		})
	}
}
