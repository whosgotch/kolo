// Package detect reads the agent's screen to decide whether a guest's line may
// be sent to it.
//
// This exists because sending at the wrong moment is not a cosmetic mistake.
// kolo has to press Enter to submit a guest's line, and Enter means something
// else entirely when the agent has a question on screen: it approves the
// highlighted option. An ordinary guest message can therefore approve a file
// write, or — if the highlighted option happens to be "allow all edits during
// this session" — switch off prompting for the rest of the session, while the
// guest's actual words are discarded. See docs/probe-findings.md #5.
//
// The agent running a shell command is the sharp case. Its input box is still
// drawn, so the screen looks idle, and a line sent then goes to the child
// process's stdin and is lost without a trace (findings #4). What tells them
// apart is the hint under the box: "? for shortcuts" when it is yours to type
// in, "esc to interrupt" when it is not.
//
// So the rule here is that nothing is safe unless it is recognised as safe.
// Every screen this package does not understand comes back Unknown, and Unknown
// holds the queue. A missed detection costs a guest a delay; the opposite
// mistake costs the host a command they never approved.
//
// Reading the dialog's choices is here too, and for the same reason. A member
// answering from a browser must answer the question that is on screen now, and
// the screen is the only account of what that question is.
//
// The markers are Claude Code's. A detector that works for one agent's TUI is
// worth more than one that half-works for every agent, and the fallback for an
// unrecognised agent is the safe one: its screens never match, so nothing is
// ever sent.
package detect

import (
	"regexp"
	"strconv"
	"strings"
)

// State is what the agent's screen says about sending it a line.
type State int

const (
	// Unknown means the screen was not recognised. It is the zero value on
	// purpose: anything unaccounted for must hold the queue rather than
	// release it.
	Unknown State = iota

	// Idle is the agent sitting at its input box. This is the state a queued
	// line may be flushed in.
	Idle

	// Dialog is a question on screen, waiting to be answered. Enter would
	// answer it, so nothing may be sent.
	Dialog

	// Busy is the agent working — thinking, or running a shell command. Nothing
	// may be sent, and it is worth telling apart from Unknown: this one clears
	// on its own, and a queue held here is waiting rather than stuck.
	Busy
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Dialog:
		return "dialog"
	case Busy:
		return "busy"
	default:
		return "unknown"
	}
}

// CanSend reports whether a guest's line may be written to the agent now.
func (s State) CanSend() bool { return s == Idle }

// Markers, each taken from a recording under internal/detect/testdata rather
// than from reading the agent's source or guessing at its wording.
const (
	// dialogFooter appears under every question the agent asks — both the
	// tool-permission dialog and the workspace-trust prompt.
	dialogFooter = "Esc to cancel"

	// dialogOption is the highlighted first choice of a numbered list. It is
	// matched as well as the footer so that a dialog is still recognised if the
	// footer wording ever changes.
	dialogOption = "❯ 1."

	// idleFooter is the shortcut hint under the input box. It is on screen
	// whenever the input box is, and gone while a dialog is up: a dialog takes
	// the whole screen and the input box is not drawn at all.
	idleFooter = "? for shortcuts"

	// busyFooter replaces idleFooter while the agent is working. The input box
	// is still drawn, which is what made this state dangerous — it looks idle —
	// but the hint under it changes, and that is the tell.
	//
	// The case matters. This is not dialogFooter with different words: "esc to
	// interrupt" and "Esc to cancel" are separate states and a case-insensitive
	// match would confuse them.
	busyFooter = "esc to interrupt"
)

// Option is one numbered choice of the dialog on screen.
type Option struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

// optionLine matches a choice in a numbered list, with the marker that the agent
// draws against the highlighted one.
var optionLine = regexp.MustCompile(`^\s*(❯)?\s*(\d{1,2})\.\s+(\S.*?)\s*$`)

// Options reads the choices out of the dialog on screen, so that a member can be
// offered the question in words rather than told to steer through it blind.
//
// It is the same screen the detector classifies and the same one everybody is
// watching. That matters for what happens with the answer: a choice is only
// meaningful against the arrangement it was read from, so what is returned here
// is what the answer is later checked against.
//
// Nothing is returned for a screen that is not a dialog, or for one whose
// numbering does not run 1, 2, 3 down consecutive lines. Prose and diffs contain
// numbers; a list does not have to be found, and finding one that is not there is
// how a member ends up answering a question nobody asked.
func Options(screen string) []Option {
	if Of(screen) != Dialog {
		return nil
	}
	var out []Option
	last := -2
	for i, line := range strings.Split(screen, "\n") {
		m := optionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		opt := Option{Number: n, Label: m[3], Selected: m[1] != ""}
		switch {
		case n == len(out)+1 && i == last+1:
			out = append(out, opt)
		case n == 1:
			// A list further down the screen replaces one found above it. The
			// dialog is drawn at the bottom, under whatever it is asking about.
			out = []Option{opt}
		default:
			continue
		}
		last = i
	}
	// One line reading "1." is a sentence. A question offers something to choose
	// between.
	if len(out) < 2 {
		return nil
	}
	return out
}

// Of classifies the agent's screen.
func Of(screen string) State {
	// Dialog is tested first. If a screen somehow carried two sets of markers,
	// the answer that holds the queue is the one to give.
	if strings.Contains(screen, dialogFooter) || strings.Contains(screen, dialogOption) {
		return Dialog
	}
	if strings.Contains(screen, busyFooter) {
		return Busy
	}
	if strings.Contains(screen, idleFooter) {
		return Idle
	}
	return Unknown
}
