// Package adapter holds what kolo knows about each kind of agent, in one place
// rather than three.
//
// A kind kolo has no adapter for gets the zero Adapter, and the zero Adapter is
// harmless by construction: no marker matches, so its screen never reads as idle
// and nothing is ever sent to it; it cannot be resumed, so it restarts fresh;
// and no line of its is taken for a command. Such an agent is watchable, not
// drivable.
package adapter

import (
	"path/filepath"

	"github.com/whosgotch/kolo/internal/detect"
)

// Adapter is the three things kolo knows about an agent kind. See
// docs/architecture.md "What kolo knows about each agent kind".
type Adapter struct {
	// Markers are how this kind's screen looks when it is idle, working, or
	// asking a question.
	Markers detect.Markers
	// Resume is appended to the command to bring back the last conversation.
	// Empty means the kind cannot be resumed, so every restart of one is fresh.
	Resume []string
	// Sigils begin a line that belongs to the agent's own CLI rather than to the
	// model behind it: a slash command, a shell line, a note for its memory.
	Sigils string
}

var kinds = map[string]Adapter{
	"claude": {
		Markers: detect.Markers{
			Idle:           "? for shortcuts",
			Busy:           "esc to interrupt",
			DialogFooter:   "Esc to cancel",
			DialogSelected: "❯",
		},
		Resume: []string{"--continue"},
		Sigils: "/!#",
	},
}

// For returns the adapter for the kind of agent command runs, keyed by the name
// of the binary rather than the path it was found at.
func For(command string) Adapter { return kinds[filepath.Base(command)] }
