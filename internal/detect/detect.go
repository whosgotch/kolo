// Package detect reads the agent's screen to tell what it is doing.
//
// What the screen looks like in each state is a property of the agent kind, so
// the markers are given rather than known here; internal/adapter holds them.
//
// An empty marker matches nothing, so the zero Markers — a kind with no adapter
// — reads every screen as Unknown.
//
// See docs/architecture.md "Input" and docs/probe-findings.md #4 and #5.
package detect

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// State is what the agent's screen says it is doing.
type State int

const (
	// Unknown is first, so an unrecognised screen is the zero value.
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

// Markers are the strings one agent kind puts on screen in each state, each
// taken from a recording of it rather than from its source. Matched
// case-sensitively: "esc to interrupt" and "Esc to cancel" are different states.
type Markers struct {
	// Idle is the hints an input box that can take a line carries, any one of
	// which means idle: the hint changes between versions and permission modes
	// while meaning the same thing (probe-findings #7). Read last and only as an
	// absence.
	Idle         []string `json:"idle,omitempty"`
	Busy         string   `json:"busy,omitempty"`
	DialogFooter string   `json:"dialogFooter,omitempty"`
	// DialogSelected is the sigil in front of a dialog's highlighted choice. It
	// is not a marker of the dialog on its own — the input box draws the same
	// sigil in front of its placeholder — so what is looked for is the sigil in
	// front of the first choice.
	DialogSelected string `json:"dialogSelected,omitempty"`
	// Settle is how long the screen must go unchanged before it reads as idle,
	// for a kind that says nothing while it waits. Zero means silence proves
	// nothing, which is the arrangement to prefer (probe-findings #6).
	Settle time.Duration `json:"settle,omitempty"`
}

// Blank reports whether these markers say nothing at all, and so read every
// screen as Unknown. It is what a kind kolo has no adapter for gets, and what a
// configured one must not be.
func (m Markers) Blank() bool {
	return len(m.Idle) == 0 && m.Busy == "" && m.DialogFooter == "" && m.DialogSelected == ""
}

// Option is one numbered choice of the dialog on screen.
type Option struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

var optionLine = regexp.MustCompile(`^(\d{1,2})\.\s+(\S.*)$`)

// Options reads the choices out of the dialog on screen, so a member can be
// offered the question in words.
//
// Nothing is returned unless the numbering runs 1, 2, 3 down consecutive lines:
// prose and diffs contain numbers, and a list that is not there is how somebody
// answers a question nobody asked.
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
// declares a settle period reads as idle once nothing has changed for that long,
// and only from a screen Of made nothing of.
//
// still is how long the screen has been the same picture. A blank screen is never
// idle: silence means something only once there is something to be silent under.
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
// carried two sets of markers, the safest reading is the one to give.
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

func hasAny(screen string, markers []string) bool {
	for _, marker := range markers {
		if has(screen, marker) {
			return true
		}
	}
	return false
}
