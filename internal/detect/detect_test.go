package detect

import (
	"os"
	"strings"
	"testing"

	"github.com/whosgotch/kolo/internal/term"
)

// The screens below are reconstructions of the three states, written out rather
// than recorded.
//
// Recordings were tried first and removed: a recording of a real session is a
// picture of somebody's screen, carrying their email, their paths and whatever
// else was on it, and that is not a thing to commit to a repository. What the
// detector actually reads is the arrangement — a dialog takes the whole screen
// and the input box is not drawn at all — and that is reproduced here without
// anybody's session in it.
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
		want   State
	}{
		{"idle at the prompt", idleScreen, Idle},
		{"tool permission dialog", permissionScreen, Dialog},
		{"workspace trust dialog", trustScreen, Dialog},
		{"running a shell command", busyScreen, Busy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Of(tt.screen); got != tt.want {
				t.Errorf("Of(%s) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}

// TestTheDialogsHideTheInputBox pins the arrangement the detector depends on.
// If a future agent drew its input box underneath a dialog, the idle marker
// would be on screen at the same time as the dialog's, and these cases would be
// the ones to revisit.
func TestTheDialogsHideTheInputBox(t *testing.T) {
	for name, screen := range map[string]string{
		"permission": permissionScreen,
		"trust":      trustScreen,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(screen, idleFooter) {
				t.Errorf("the %s dialog shows the input box footer, which the detector treats as idle", name)
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
		"partial repaint":   "╭─── Claude Code ───╮\n│ Welcome back!     │\n",
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

// TestBusyKeepsItsInputBox is why this state needed a marker of its own rather
// than being left to fall through as Unknown. It draws the input box, so the
// absence of the idle footer is the only thing standing between a message and a
// child process's stdin.
func TestBusyKeepsItsInputBox(t *testing.T) {
	if strings.Contains(busyScreen, idleFooter) {
		t.Error("the busy screen carries the idle footer, so it reads as safe to send")
	}
	// Two footers, two states. A case-insensitive match would fold them
	// together and a busy agent would read as a dialog.
	if strings.Contains(busyScreen, dialogFooter) {
		t.Error("the busy screen reads as a dialog")
	}
}

func TestOptions(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   []Option
	}{
		{"tool permission dialog", permissionScreen, []Option{
			{1, "Yes", true},
			{2, "Yes, allow all edits during this session (shift+tab)", false},
			{3, "No", false},
		}},
		{"workspace trust dialog", trustScreen, []Option{
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
			got := Options(tt.screen)
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

// TestOptionsIgnoresNumbersThatAreNotChoices is the sharp case for the parser.
// The permission dialog shows a numbered diff of the file it is asking about, so
// the screen carries numbers that mean nothing and must not be offered as
// answers.
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
			for _, o := range Options(screen) {
				if strings.Contains(o.Label, "hello") || strings.Contains(o.Label, "one thing") {
					t.Errorf("offered %q as a choice", o.Label)
				}
			}
		})
	}
}

func TestOnlyIdleCanSend(t *testing.T) {
	for _, s := range []State{Unknown, Idle, Dialog, Busy} {
		if want := s == Idle; s.CanSend() != want {
			t.Errorf("%s.CanSend() = %v, want %v", s, s.CanSend(), want)
		}
	}
}

// TestOfARecording replays a real capture, which exercises the arrangements no
// handwritten screen will think of. Recordings are not committed, because one
// is a picture of somebody's session; make your own with cmd/kolorec and point
// this at it:
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
	if got := Of(screen.Text()); got.String() != want {
		t.Errorf("Of(%s) = %s, want %s", path, got, want)
	}
}
