// Package detect reads the agent's screen to decide whether a guest's line may
// be sent to it.
//
// Nothing is safe unless it is recognised as safe: an unrecognised screen reads
// as Unknown, and Unknown holds the queue. The markers are Claude Code's, so an
// agent kind kolo does not know never reads as idle and is never sent anything.
//
// See docs/architecture.md "Input" and docs/probe-findings.md #4 and #5.
package detect

import (
	"regexp"
	"strconv"
	"strings"
)

// State is what the agent's screen says about sending it a line.
type State int

const (
	// Unknown is first so that the zero value holds the queue.
	Unknown State = iota
	Idle
	Dialog
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

// Markers, each taken from a recording under testdata rather than from the
// agent's source. Matched case-sensitively: "esc to interrupt" and "Esc to
// cancel" are different states.
const (
	dialogFooter = "Esc to cancel"
	dialogOption = "❯ 1."
	idleFooter   = "? for shortcuts"
	busyFooter   = "esc to interrupt"
)

// Option is one numbered choice of the dialog on screen.
type Option struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

var optionLine = regexp.MustCompile(`^\s*(❯)?\s*(\d{1,2})\.\s+(\S.*?)\s*$`)

// Options reads the choices out of the dialog on screen, so that a member can be
// offered the question in words. A choice is only meaningful against the screen
// it was read from, and this is the screen an answer is later checked against.
//
// Nothing is returned unless the numbering runs 1, 2, 3 down consecutive lines.
// Prose and diffs contain numbers, and finding a list that is not there is how a
// member ends up answering a question nobody asked.
func Options(screen string) []Option {
	if Of(screen) != Dialog {
		return nil
	}
	var options []Option
	prevLine := -2
	for i, line := range strings.Split(screen, "\n") {
		m := optionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		number, _ := strconv.Atoi(m[2])
		option := Option{Number: number, Label: m[3], Selected: m[1] != ""}
		switch {
		case number == len(options)+1 && i == prevLine+1:
			options = append(options, option)
		case number == 1:
			// The dialog is drawn at the bottom, so a list further down the
			// screen replaces one found above it.
			options = []Option{option}
		default:
			continue
		}
		prevLine = i
	}
	// A lone "1." is prose. A question offers something to choose between.
	if len(options) < 2 {
		return nil
	}
	return options
}

// Of classifies the agent's screen. Dialog is tested first: if a screen somehow
// carried two sets of markers, the answer that holds the queue is the one to give.
func Of(screen string) State {
	switch {
	case strings.Contains(screen, dialogFooter), strings.Contains(screen, dialogOption):
		return Dialog
	case strings.Contains(screen, busyFooter):
		return Busy
	case strings.Contains(screen, idleFooter):
		return Idle
	}
	return Unknown
}
