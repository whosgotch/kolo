// Package adapter holds what kolo knows about each kind of agent. A kind with no
// adapter gets the zero Adapter, which is inert: no marker matches and it cannot
// be resumed.
package adapter

import (
	"path/filepath"
	"sort"

	"github.com/whosgotch/kolo/internal/detect"
)

// See docs/architecture.md "What kolo knows about each agent kind".
type Adapter struct {
	Markers detect.Markers
	// Resume is appended to the command to bring back the last conversation.
	// Empty means the kind cannot be resumed, so every restart of one is fresh.
	Resume []string
}

var kinds = map[string]Adapter{
	"claude": {
		Markers: detect.Markers{
			// Both say the input box is drawn with nothing running in
			// front of it. v2.1.226 hung "? for shortcuts" under the box;
			// v2.1.234 hangs the permission mode there and drops the rest
			// of the footer once the box has anything in it (#7).
			Idle:           []string{"(shift+tab to cycle)", "? for shortcuts"},
			Busy:           "esc to interrupt",
			DialogFooter:   "Esc to cancel",
			DialogSelected: "❯",
		},
		Resume: []string{"--continue"},
	},
}

// For is keyed by the name of the binary rather than the path it was found at.
func For(command string) Adapter { return kinds[filepath.Base(command)] }

// Kinds is every agent command kolo knows how to run, sorted. What a host may
// be told to start is still only what it was started with; this is for asking
// which of them are installed.
func Kinds() []string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
