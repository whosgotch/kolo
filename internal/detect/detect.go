// Package detect reads an agent's screen to tell what it's doing. The zero
// Markers matches nothing; see docs/reference.md, "Agents".
package detect

import (
	"strings"
	"time"
)

type State int

const (
	// First, so an unrecognised screen is the zero value.
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

// Markers are the strings a kind draws on screen per state, matched
// case-sensitively against a recording of the screen, not its source.
type Markers struct {
	Idle         []string `json:"idle,omitempty"`
	Busy         string   `json:"busy,omitempty"`
	DialogFooter string   `json:"dialogFooter,omitempty"`
	// Matched as "<sigil> 1.", so a bare sigil doesn't also hit the input
	// box's placeholder, which wears the same sigil.
	DialogSelected string `json:"dialogSelected,omitempty"`
	// How long the screen must sit unchanged to read as idle; zero means
	// silence proves nothing.
	Settle time.Duration `json:"settle,omitempty"`
}

// Blank reports markers that match nothing, as an unconfigured kind has.
func (m Markers) Blank() bool {
	return len(m.Idle) == 0 && m.Busy == "" && m.DialogFooter == "" && m.DialogSelected == ""
}

// OfSettled is Of, plus a settle-timeout fallback for a kind that says nothing
// while idle. A blank screen is never idle.
func (m Markers) OfSettled(screen string, still time.Duration) State {
	if s := m.Of(screen); s != Unknown {
		return s
	}
	if m.Settle > 0 && still >= m.Settle && strings.TrimSpace(screen) != "" {
		return Idle
	}
	return Unknown
}

// Of classifies the screen; dialog wins over idle as the safer reading.
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

func (m Markers) firstChoice() string {
	if m.DialogSelected == "" {
		return ""
	}
	return m.DialogSelected + " 1."
}

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
