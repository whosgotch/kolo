// Package adapter holds what kolo knows about each kind of agent.
//
// A kind kolo has no adapter for gets the zero Adapter, and the zero Adapter is
// harmless by construction: no marker matches, so its screen never reads as idle
// and nothing is ever answered on it; and it cannot be resumed, so it restarts
// fresh. Such an agent is watchable and typeable, but kolo can say nothing about
// what is on its screen.
package adapter

import (
	"path/filepath"

	"github.com/whosgotch/kolo/internal/detect"
)

// Adapter is the two things kolo knows about an agent kind. It was three until
// members could type for themselves: which lines address the agent's own CLI
// mattered while kolo was typing them, and a member typing /clear needs nobody's
// help to put the slash in the first column.
//
// See docs/architecture.md "What kolo knows about each agent kind".
type Adapter struct {
	// Markers are how this kind's screen looks when it is idle, working, or
	// asking a question.
	Markers detect.Markers
	// Resume is appended to the command to bring back the last conversation.
	// Empty means the kind cannot be resumed, so every restart of one is fresh.
	Resume []string
}

var kinds = map[string]Adapter{
	"claude": {
		Markers: detect.Markers{
			// Two versions' worth of the same fact — the input box is drawn
			// and nothing is running in front of it. v2.1.226 hung "? for
			// shortcuts" under the box; v2.1.234 hangs the permission mode
			// there, and drops every other segment of the footer as soon as
			// the box has anything in it (probe-findings #7).
			Idle:           []string{"(shift+tab to cycle)", "? for shortcuts"},
			Busy:           "esc to interrupt",
			DialogFooter:   "Esc to cancel",
			DialogSelected: "❯",
		},
		Resume: []string{"--continue"},
	},
}

// For returns the adapter for the kind of agent command runs, keyed by the name
// of the binary rather than the path it was found at.
func For(command string) Adapter { return kinds[filepath.Base(command)] }
