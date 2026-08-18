package detect_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/term"
)

// The real adapter's markers rather than a copy of them, so a kind that changes
// one of its strings is a failure here rather than a detector quietly reading
// screens nobody draws any more.
var claude = adapter.For("claude").Markers

// Reconstructions of the three states, written out rather than recorded: a real
// recording carries somebody's email and paths, which is not a thing to commit.
// What the detector reads is the arrangement, and that is reproduced here.
//
// A real recording can still be replayed locally; see TestOfARecording.
const (
	idleScreen = `
╭─── Claude Code ──────────────────────────────────────────────╮
│                                                              │
│   Welcome back!                                              │
│                                                              │
╰──────────────────────────────────────────────────────────────╯

────────────────────────────────────────────────────────────────
❯ Try "explain this function"
────────────────────────────────────────────────────────────────
  ⏸ manual mode on · ? for shortcuts · ← for agents
`

	permissionScreen = `
❯ create a file called note.txt

⏺ Write(note.txt)

────────────────────────────────────────────────────────────────
 Create file
 note.txt
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
  1 hello
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to create note.txt?
 ❯ 1. Yes
   2. Yes, allow all edits during this session (shift+tab)
   3. No

 Esc to cancel · Tab to amend
`

	// The one that made the gate necessary. The input box is drawn, exactly as
	// when idle, and the only difference is the hint under it.
	busyScreen = `
❯ run in bash: for i in 1 2 3; do echo $i; sleep 2; done

⏺ Bash(for i in 1 2 3; do echo $i; sleep 2; done)

✳ Levitating… (8s · ↓ 117 tokens)
────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────
  ⏸ manual mode on · esc to interrupt · ← for agents
`

	// v2.1.234, where the hint the detector was written against is gone: the
	// footer carries the permission mode and the agents hint, and idle differs
	// from working only by what is missing (probe-findings #7).
	idleScreenNow = `
                                                                     ● high · /effort
────────────────────────────────────────────────────────────────
❯ Try "fix typecheck errors"
────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents
`

	busyScreenNow = `
⏺ Bash(for i in 1 2 3; do echo $i; sleep 2; done)

────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents
`

	// The same version again, with something in the input box: every segment of
	// the footer but the permission mode is gone. A line waiting in the box is
	// what a failed submit leaves behind, and it must not read as unknown — the
	// agent is idle, and it is the state a member is looking at when they wonder
	// why nothing happened.
	idleScreenTyped = `
────────────────────────────────────────────────────────────────
❯ Dana: count slowly from one to sixty
────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)
`

	// The same version's auto-mode question, which is drawn above the input box
	// rather than in place of it — so the idle footer is on screen underneath a
	// question nobody has answered.
	autoModeScreen = `
  9. Nine — one shy of double digits.

────────────────────────────────────────────────────────────────
  Set up auto mode for your environment?

  Auto mode lets Claude act without asking first.

  ❯ 1. Set it up
    2. Not now
    3. Don't show again

  Enter to confirm · Esc to cancel
────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents
`

	trustScreen = `
────────────────────────────────────────────────────────────────
 Accessing workspace:

 /tmp/scratch

 Quick safety check: Is this a project you created or one you
 trust? Claude Code'll be able to read, edit, and execute files
 here.

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`
)

func TestOf(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   detect.State
	}{
		{"idle at the prompt", idleScreen, detect.Idle},
		{"tool permission dialog", permissionScreen, detect.Dialog},
		{"workspace trust dialog", trustScreen, detect.Dialog},
		{"running a shell command", busyScreen, detect.Busy},
		{"idle, current version", idleScreenNow, detect.Idle},
		{"running a shell command, current version", busyScreenNow, detect.Busy},
		{"a question drawn above the input box", autoModeScreen, detect.Dialog},
		{"idle with a line waiting in the box", idleScreenTyped, detect.Idle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claude.Of(tt.screen); got != tt.want {
				t.Errorf("Of(%s) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}

// TestAQuestionOverTheInputBoxIsStillAQuestion is the arrangement that used to
// be impossible. Dialogs once replaced the input box; the auto-mode question
// draws above it and leaves the idle footer on screen, so a screen now carries
// both sets of markers at once and the order they are tested in is the only
// thing keeping a queued line off a question.
func TestAQuestionOverTheInputBoxIsStillAQuestion(t *testing.T) {
	if !hasAnyOf(autoModeScreen, claude.Idle) {
		t.Fatal("fixture no longer carries the idle footer; it is the point of this test")
	}
	if got := claude.Of(autoModeScreen); got != detect.Dialog {
		t.Errorf("Of(a question over the input box) = %s, want %s", got, detect.Dialog)
	}
	if got := claude.Options(autoModeScreen); len(got) != 3 {
		t.Errorf("read %v off the question, want its three choices", got)
	}
}

// hasAnyOf is the any-of match the detector does, for tests that assert about a
// screen rather than about a verdict.
func hasAnyOf(screen string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(screen, m) {
			return true
		}
	}
	return false
}

// TestUnrecognisedScreensHold is the property the whole package is for. A
// screen that means nothing to the detector must not read as safe.
func TestUnrecognisedScreensHold(t *testing.T) {
	screens := map[string]string{
		"empty":             "",
		"blank rows":        strings.Repeat(strings.Repeat(" ", 120)+"\n", 40),
		"another agent":     "$ some other tool\n> waiting for input\n",
		"partial repaint":   "╭─── Claude Code ───╮\n│ Welcome back!     │\n",
		"agent output only": "Here is the answer to your question.\n",
	}
	for name, screen := range screens {
		t.Run(name, func(t *testing.T) {
			if got := claude.Of(screen); got.CanSend() {
				t.Errorf("Of(%s) = %s, which allows sending; want it held", name, got)
			}
		})
	}
}

// TestDialogWinsOverIdle pins the tie-break. If both sets of markers are on
// screen at once, the answer must be the one that holds the queue.
func TestDialogWinsOverIdle(t *testing.T) {
	both := "Do you want to create note.txt?\n ❯ 1. Yes\n Esc to cancel\n ? for shortcuts\n"
	if got := claude.Of(both); got != detect.Dialog {
		t.Errorf("Of(both) = %s, want %s", got, detect.Dialog)
	}
}

// TestBusyKeepsItsInputBox is why this state needed a marker of its own. It draws
// the input box, so the absence of the idle footer is the only thing between a
// message and a child process's stdin.
func TestBusyKeepsItsInputBox(t *testing.T) {
	// The current version's footer carries the idle hint while it works, so this
	// says what it can: the busy screen must not read as idle, whatever it shows.
	if got := claude.Of(busyScreenNow); got != detect.Busy {
		t.Errorf("the current version's busy screen reads as %s", got)
	}
	if strings.Contains(busyScreen, "? for shortcuts") {
		t.Error("the busy screen carries the idle footer, so it reads as safe to send")
	}
	// Two footers, two states. A case-insensitive match would fold them
	// together and a busy agent would read as a dialog.
	if strings.Contains(busyScreen, claude.DialogFooter) {
		t.Error("the busy screen reads as a dialog")
	}
}

func TestOptions(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   []detect.Option
	}{
		{"tool permission dialog", permissionScreen, []detect.Option{
			{1, "Yes", true},
			{2, "Yes, allow all edits during this session (shift+tab)", false},
			{3, "No", false},
		}},
		{"workspace trust dialog", trustScreen, []detect.Option{
			{1, "Yes, I trust this folder", true},
			{2, "No, exit", false},
		}},
		// Nothing to answer means nothing to offer. An idle or busy screen must
		// not produce choices, or a page would show a question that has gone.
		{"idle at the prompt", idleScreen, nil},
		{"running a shell command", busyScreen, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claude.Options(tt.screen)
			if len(got) != len(tt.want) {
				t.Fatalf("Options(%s) = %v, want %v", tt.name, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("option %d = %+v, want %+v", i+1, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestOptionsIgnoresNumbersThatAreNotChoices is the sharp case: the permission
// dialog shows a numbered diff, so the screen carries numbers that mean nothing.
func TestOptionsIgnoresNumbersThatAreNotChoices(t *testing.T) {
	for name, screen := range map[string]string{
		"a numbered diff above the question": permissionScreen,
		"prose that enumerates": `
 Do you want to proceed?
 It does 1. one thing, then 2. another.

 ❯ 1. Yes
   2. No

 Esc to cancel
`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, o := range claude.Options(screen) {
				if strings.Contains(o.Label, "hello") || strings.Contains(o.Label, "one thing") {
					t.Errorf("offered %q as a choice", o.Label)
				}
			}
		})
	}
}

// TestAKindWithNoMarkersRecognisesNothing is what makes an agent kolo has no
// adapter for watchable rather than drivable. An empty marker must match no
// screen, not every screen.
func TestAKindWithNoMarkersRecognisesNothing(t *testing.T) {
	var unknownKind detect.Markers
	for name, screen := range map[string]string{
		"idle":       idleScreen,
		"permission": permissionScreen,
		"trust":      trustScreen,
		"busy":       busyScreen,
	} {
		t.Run(name, func(t *testing.T) {
			if got := unknownKind.Of(screen); got != detect.Unknown {
				t.Errorf("read another kind's %s screen as %s", name, got)
			}
			if got := unknownKind.Options(screen); got != nil {
				t.Errorf("offered %v to answer on a screen it cannot read", got)
			}
		})
	}
}

// A kind that says nothing while it waits, so the only thing telling working
// from waiting is that one of them is still changing (probe-findings #6). Its
// working line carries Claude Code's string exactly, as the second kind probed
// does.
var settling = detect.Markers{
	Busy:           "esc to interrupt",
	DialogFooter:   "Press enter to continue",
	DialogSelected: "›",
	Settle:         2 * time.Second,
}

func TestSilenceIsIdleOnceItHasLasted(t *testing.T) {
	reply := "1. One, starting steadily.\n2. Two, and so on.\n› \n  gpt-5 · /tmp/scratch\n"
	tests := []struct {
		name   string
		screen string
		still  time.Duration
		want   detect.State
	}{
		{"still streaming", reply, time.Second, detect.Unknown},
		{"nothing has moved for long enough", reply, 3 * time.Second, detect.Idle},
		// The screen an agent has before it has drawn anything. Waiting on that is
		// not the same as waiting for a person.
		{"blank, however long", "   \n\n", time.Hour, detect.Unknown},
		// Stillness is only ever consulted about a screen that said nothing. A
		// working line that has been up for a minute is a minute of working.
		{"working and quiet", "◦ Working (60s • esc to interrupt)\n", time.Minute, detect.Busy},
		{"a question nobody has answered", " › 1. Yes\n   2. No\n Press enter to continue\n", time.Hour, detect.Dialog},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := settling.OfSettled(tt.screen, tt.still); got != tt.want {
				t.Errorf("OfSettled(%s, %s) = %s, want %s", tt.name, tt.still, got, tt.want)
			}
		})
	}
}

// TestSilenceMeansNothingToAKindThatSpeaks: settling is opt-in, and a kind whose
// screen says when it is idle must never be read by how long it has been quiet.
// Claude Code may sit on an unrecognised screen for as long as it likes.
func TestSilenceMeansNothingToAKindThatSpeaks(t *testing.T) {
	for _, still := range []time.Duration{0, time.Second, time.Hour} {
		if got := claude.OfSettled("some other tool\n> waiting for input\n", still); got != detect.Unknown {
			t.Errorf("still for %s read as %s", still, got)
		}
	}
}

func TestOnlyIdleCanSend(t *testing.T) {
	for _, s := range []detect.State{detect.Unknown, detect.Idle, detect.Dialog, detect.Busy} {
		if want := s == detect.Idle; s.CanSend() != want {
			t.Errorf("%s.CanSend() = %v, want %v", s, s.CanSend(), want)
		}
	}
}

// TestOfARecording replays a real capture, exercising arrangements no handwritten
// screen will think of. Recordings are not committed; make your own with
// cmd/kolorec and point this at it:
//
//	KOLO_RECORDING=/tmp/rec/idle.raw KOLO_STATE=idle go test ./internal/detect
func TestOfARecording(t *testing.T) {
	path, want := os.Getenv("KOLO_RECORDING"), os.Getenv("KOLO_STATE")
	if path == "" {
		t.Skip("set KOLO_RECORDING and KOLO_STATE to replay a capture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	screen := term.New(120, 40)
	// In chunks, because that is how the bytes arrive off a PTY.
	for i := 0; i < len(raw); i += 64 {
		screen.Write(raw[i:min(i+64, len(raw))])
	}
	if got := claude.Of(screen.Text()); got.String() != want {
		t.Errorf("Of(%s) = %s, want %s", path, got, want)
	}
}
