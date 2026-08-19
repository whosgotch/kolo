// Package detect reads the agent's screen to decide whether a guest's line may
// be sent to it.
//
// What the screen looks like in each state is a property of the agent kind, so
// the markers are given rather than known here; internal/adapter holds them.
//
// Nothing is safe unless it is recognised as safe: a screen carrying none of the
// markers reads as Unknown, and Unknown holds the queue. An empty marker never
// matches, so the zero Markers — an agent kind kolo has no adapter for — reads
// every screen as Unknown and is never sent anything.
//
// See docs/architecture.md "Input" and docs/probe-findings.md #4 and #5.
package detect

import (
	"regexp"
	"strconv"
	"strings"
	"time"
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

func (s State) CanSend() bool { return s == Idle }

// Markers are the strings one agent kind puts on screen in each state, each
// taken from a recording of it rather than from its source. Matched
// case-sensitively: "esc to interrupt" and "Esc to cancel" are different states.
type Markers struct {
	// Idle is the hints an input box that can take a line carries, any one of
	// which means idle. Several, because the hint changes between versions and
	// permission modes while meaning the same thing (probe-findings #7).
	//
	// Read last and only as an absence: a dialog or a working line is answered
	// first.
	Idle         []string
	Busy         string
	DialogFooter string
	// DialogSelected is the sigil in front of a dialog's highlighted choice. It
	// is not a marker of the dialog on its own — the input box draws the same
	// sigil in front of its placeholder — so what is looked for is the sigil in
	// front of the first choice.
	DialogSelected string
	// Settle is how long the screen must go unchanged before it reads as idle,
	// for a kind that says nothing while it waits. Zero means silence proves
	// nothing, which is the safer arrangement and the one to prefer.
	//
	// See docs/probe-findings.md #6.
	Settle time.Duration
}

// Option is one numbered choice of the dialog on screen.
type Option struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

var optionLine = regexp.MustCompile(`^(\d{1,2})\.\s+(\S.*)$`)

// Options reads the choices out of the dialog on screen, so that a member can be
// offered the question in words. A choice is only meaningful against the screen
// it was read from, and this is the screen an answer is later checked against.
//
// Nothing is returned unless the numbering runs 1, 2, 3 down consecutive lines.
// Prose and diffs contain numbers, and finding a list that is not there is how a
// member ends up answering a question nobody asked.
func (m Markers) Options(screen string) []Option {
	if m.Of(screen) != Dialog {
		return nil
	}
	var options []Option
	prevLine := -2
	for i, line := range strings.Split(screen, "\n") {
		text := strings.TrimSpace(line)
		selected := m.DialogSelected != "" && strings.HasPrefix(text, m.DialogSelected)
		if selected {
			text = strings.TrimSpace(strings.TrimPrefix(text, m.DialogSelected))
		}
		match := optionLine.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		number, _ := strconv.Atoi(match[1])
		option := Option{Number: number, Label: strings.TrimSpace(match[2]), Selected: selected}
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

// OfSettled is Of, plus the answer a single screen cannot give: a kind that
// declares a settle period reads as idle once nothing has changed for that long.
// Only from a screen Of made nothing of, so a working line or a question still
// wins however long it has been up.
//
// still is how long the screen has been the same picture. A blank screen is never
// idle: an agent that has drawn nothing has not started, and silence means
// something only once there is something to be silent under.
func (m Markers) OfSettled(screen string, still time.Duration) State {
	if s := m.Of(screen); s != Unknown {
		return s
	}
	if m.Settle > 0 && still >= m.Settle && strings.TrimSpace(screen) != "" {
		return Idle
	}
	return Unknown
}

// Of classifies the agent's screen. Dialog is tested first: if a screen somehow
// carried two sets of markers, the answer that holds the queue is the one to give.
func (m Markers) Of(screen string) State {
	switch {
	case has(screen, m.DialogFooter), has(screen, m.firstChoice()):
		return Dialog
	case has(screen, m.Busy):
		return Busy
	case hasAny(screen, m.Idle):
		return Idle
	}
	return Unknown
}

// firstChoice is the dialog's highlighted first option, which is the arrangement
// the sigil only appears in while a question is on screen.
func (m Markers) firstChoice() string {
	if m.DialogSelected == "" {
		return ""
	}
	return m.DialogSelected + " 1."
}

// has is Contains, except that a marker a kind does not declare matches nothing.
func has(screen, marker string) bool {
	return marker != "" && strings.Contains(screen, marker)
}

// hasAny is has over the strings that mean the same state.
func hasAny(screen string, markers []string) bool {
	for _, marker := range markers {
		if has(screen, marker) {
			return true
		}
	}
	return false
}
