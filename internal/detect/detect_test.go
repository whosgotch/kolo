package detect_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/term"
)

var claude = adapter.For("claude").Markers

var opencode = adapter.For("opencode").Markers

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

	busyScreen = `
❯ run in bash: for i in 1 2 3; do echo $i; sleep 2; done

⏺ Bash(for i in 1 2 3; do echo $i; sleep 2; done)

✳ Levitating… (8s · ↓ 117 tokens)
────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────
  ⏸ manual mode on · esc to interrupt · ← for agents
`

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

	idleScreenTyped = `
────────────────────────────────────────────────────────────────
❯ Dana: count slowly from one to sixty
────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)
`

	autoModeScreen = `
  9. Nine, one shy of double digits.

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

	// OpenCode's three states. Idle and busy share the status bar, and the
	// busy bar carries the idle hints too.
	ocIdleScreen = `
  ┃
  ┃  Ask anything... "What is the tech stack of this project?"
  ┃
  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
   tab agents  ctrl+p commands
`

	// After a turn the tab hint gives way to the directory and its context.
	ocIdleAfterTurn = `
  ┃  Build · model · max
  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
   ~/work/api                                              8.4K (1%)  ctrl+p commands
`

	ocBusyScreen = `
  ┃  count slowly from one to ten, one per line
  ┃
     1. The loneliest number.
     2. Even the first prime.

  ┃  Build · model · max
  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
   ⬝⬝⬝⬝■■■■  esc interrupt    tab agents  ctrl+p commands
`

	// A question draws its own box over the status bar. The selected choice is
	// colour alone, which is why no sigil is looked for here.
	ocDialogScreen = `
  ┃  △ Permission required
  ┃    → Edit note.txt
  ┃
  ┃   Allow once   Allow always   Reject          ctrl+f fullscreen  ⇆ select  enter confirm
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

func TestAQuestionOverTheInputBoxIsStillAQuestion(t *testing.T) {
	if !hasAnyOf(autoModeScreen, claude.Idle) {
		t.Fatal("fixture no longer carries the idle footer; it is the point of this test")
	}
	if got := claude.Of(autoModeScreen); got != detect.Dialog {
		t.Errorf("Of(a question over the input box) = %s, want %s", got, detect.Dialog)
	}
}

func hasAnyOf(screen string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(screen, m) {
			return true
		}
	}
	return false
}

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
			if got := claude.Of(screen); got != detect.Unknown {
				t.Errorf("Of(%s) = %s, want it unrecognised", name, got)
			}
		})
	}
}

func TestDialogWinsOverIdle(t *testing.T) {
	both := "Do you want to create note.txt?\n ❯ 1. Yes\n Esc to cancel\n ? for shortcuts\n"
	if got := claude.Of(both); got != detect.Dialog {
		t.Errorf("Of(both) = %s, want %s", got, detect.Dialog)
	}
}

func TestOpencodeStates(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   detect.State
	}{
		{"fresh at the input box", ocIdleScreen, detect.Idle},
		{"idle after a turn", ocIdleAfterTurn, detect.Idle},
		{"running a turn", ocBusyScreen, detect.Busy},
		{"permission dialog", ocDialogScreen, detect.Dialog},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opencode.Of(tt.screen); got != tt.want {
				t.Errorf("Of(%s) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}

func TestOpencodeReadsBusyWhileTheIdleHintsAreUp(t *testing.T) {
	if !hasAnyOf(ocBusyScreen, opencode.Idle) {
		t.Fatal("fixture no longer carries the idle hints; it is the point of this test")
	}
	if got := opencode.Of(ocBusyScreen); got != detect.Busy {
		t.Errorf("Of(a working bar) = %s, want %s", got, detect.Busy)
	}
}

func TestOpencodeUnrecognisedScreensHold(t *testing.T) {
	screens := map[string]string{
		"another agent":  "$ some other tool\n> waiting for input\n",
		"output only":    "Here is the answer to your question.\n",
		"bar half drawn": "   tab agents\n",
	}
	for name, screen := range screens {
		t.Run(name, func(t *testing.T) {
			if got := opencode.Of(screen); got != detect.Unknown {
				t.Errorf("Of(%s) = %s, want it unrecognised", name, got)
			}
		})
	}
}

func TestBusyKeepsItsInputBox(t *testing.T) {
	if got := claude.Of(busyScreenNow); got != detect.Busy {
		t.Errorf("the current version's busy screen reads as %s", got)
	}
	if strings.Contains(busyScreen, "? for shortcuts") {
		t.Error("the busy screen carries the idle footer, so it reads as safe to send")
	}
	if strings.Contains(busyScreen, claude.DialogFooter) {
		t.Error("the busy screen reads as a dialog")
	}
}

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
		})
	}
}

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
		{"blank, however long", "   \n\n", time.Hour, detect.Unknown},
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

func TestSilenceMeansNothingToAKindThatSpeaks(t *testing.T) {
	for _, still := range []time.Duration{0, time.Second, time.Hour} {
		if got := claude.OfSettled("some other tool\n> waiting for input\n", still); got != detect.Unknown {
			t.Errorf("still for %s read as %s", still, got)
		}
	}
}

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
	for i := 0; i < len(raw); i += 64 {
		screen.Write(raw[i:min(i+64, len(raw))])
	}
	if got := claude.Of(screen.Text()); got.String() != want {
		t.Errorf("Of(%s) = %s, want %s", path, got, want)
	}
}

// A settle is written in seconds, because that is what docs/reference.md asks
// for and what anybody describing an agent would type. Read as a bare
// time.Duration it would have been a count of nanoseconds, and "settle": 3
// would have made every screen idle three nanoseconds after it was asked.
func TestSettleIsReadAsSeconds(t *testing.T) {
	tests := []struct {
		name string
		json string
		want time.Duration
	}{
		{"whole seconds", `{"settle": 3}`, 3 * time.Second},
		{"a fraction of one", `{"settle": 1.5}`, 1500 * time.Millisecond},
		{"a duration, for anyone who would rather say so", `{"settle": "1500ms"}`, 1500 * time.Millisecond},
		{"nothing at all", `{"busy": "working"}`, 0},
		{"null", `{"settle": null}`, 0},
		{"zero", `{"settle": 0}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m detect.Markers
			if err := json.Unmarshal([]byte(tt.json), &m); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.json, err)
			}
			if m.Settle != tt.want {
				t.Errorf("Unmarshal(%s) settle = %s, want %s", tt.json, m.Settle, tt.want)
			}
		})
	}
}

func TestSettleRefusesWhatIsNotALengthOfTime(t *testing.T) {
	for _, bad := range []string{`{"settle": -1}`, `{"settle": "-2s"}`, `{"settle": "soon"}`, `{"settle": true}`} {
		var m detect.Markers
		if err := json.Unmarshal([]byte(bad), &m); err == nil {
			t.Errorf("Unmarshal(%s) was accepted as %s", bad, m.Settle)
		}
	}
}

// The host marshals its markers to the hub, which unmarshals them and reads
// screens by them. A writer and a reader that disagreed on the unit would turn
// two seconds into sixty-three years without either one erroring.
func TestSettleSurvivesTheTripToTheHub(t *testing.T) {
	sent := detect.Markers{
		Idle:           []string{"? for shortcuts"},
		Busy:           "esc to interrupt",
		DialogFooter:   "Esc to cancel",
		DialogSelected: "❯",
		Settle:         2500 * time.Millisecond,
	}
	b, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got detect.Markers
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(%s): %v", b, err)
	}
	if !reflect.DeepEqual(sent, got) {
		t.Errorf("markers arrived as %+v, want %+v (over %s)", got, sent, b)
	}
}

// Marshal and Unmarshal each mirror Markers through an embedded type so that
// adding a marker needs no edit there. This is what fails if that stops
// holding.
func TestEveryMarkerMakesTheTripNotJustSettle(t *testing.T) {
	const wire = `{"idle":["ready"],"busy":"thinking","dialogFooter":"enter confirm",` +
		`"dialogSelected":"›","settle":4}`
	var m detect.Markers
	if err := json.Unmarshal([]byte(wire), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := detect.Markers{
		Idle:           []string{"ready"},
		Busy:           "thinking",
		DialogFooter:   "enter confirm",
		DialogSelected: "›",
		Settle:         4 * time.Second,
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("read %+v, want %+v", m, want)
	}
}

// A kind whose whole account of idleness is "it went quiet" is configured,
// not unconfigured: Blank decides whether kolo claims anything about a screen.
func TestASettleOnItsOwnIsNotBlank(t *testing.T) {
	if (detect.Markers{Settle: time.Second}).Blank() {
		t.Error("markers that read silence as idle reported themselves blank")
	}
	if !(detect.Markers{}).Blank() {
		t.Error("the zero Markers did not report itself blank")
	}
}
