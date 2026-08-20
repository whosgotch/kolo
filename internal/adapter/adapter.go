// Package adapter holds what kolo knows about each kind of agent. A kind with no
// adapter gets the zero Adapter, which is inert: no marker matches and it cannot
// be resumed.
package adapter

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whosgotch/kolo/internal/detect"
)

// See docs/architecture.md "What kolo knows about each agent kind".
type Adapter struct {
	Markers detect.Markers `json:"markers"`
	// Resume is appended to the command to bring back the last conversation.
	// Empty means the kind cannot be resumed, so every restart of one is fresh.
	Resume []string `json:"resume,omitempty"`
}

// kinds is replaced by Load at startup, so a machine can lend an agent kolo does
// not ship an adapter for.
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

// Argv splits a command line into the program and its arguments.
//
// On whitespace and nothing else: an argument with a space inside it is not
// expressible, and a host that needs one puts it in a script and lends that.
// Quoting rules here would be a shell nobody asked for, and one that differs
// from the shell the flag was typed into.
func Argv(command string) []string { return strings.Fields(command) }

// For is keyed by the name of the binary rather than the path it was found at,
// and reads that name off the front of the command line: what "claude" and
// "/opt/bin/claude --model x" have in common is the kind kolo knows.
//
// Arguments are not looked at. A kind is how an agent wears its states and how
// it is resumed, and no flag changes either.
func For(command string) Adapter {
	argv := Argv(command)
	if len(argv) == 0 {
		return Adapter{}
	}
	return kinds[filepath.Base(argv[0])]
}

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

// Load reads agent kinds from a JSON file and adds them to the ones kolo ships
// with, replacing any of the same name. A file that is not there is not an
// error: most machines run the kinds kolo knows.
//
//	{
//	  "codex": {
//	    "markers": {"busy": "esc to interrupt", "idle": ["? for shortcuts"],
//	                "dialogFooter": "Esc to cancel", "dialogSelected": "❯"},
//	    "resume": ["--continue"]
//	  }
//	}
//
// This is how an org runs an agent kolo has never heard of without waiting for
// a release: the markers are strings off that agent's own screen, and the way to
// get them right is to record one (see cmd/kolorec) rather than guess.
//
// Called once at startup, before any agent runs, because it replaces the table
// every other package reads through For.
func Load(path string) (added []string, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("adapter: read %s: %w", path, err)
	}
	var configured map[string]Adapter
	if err := json.Unmarshal(b, &configured); err != nil {
		return nil, fmt.Errorf("adapter: parse %s: %w", path, err)
	}

	merged := make(map[string]Adapter, len(kinds)+len(configured))
	maps.Copy(merged, kinds)
	for name, a := range configured {
		if name == "" || name != filepath.Base(name) {
			return nil, fmt.Errorf("adapter: %s: %q is not the name of a command", path, name)
		}
		// An entry that says nothing is a kind kolo would treat exactly as one it
		// has never heard of, so it is a typo rather than a choice — an empty
		// object where the markers were meant to go, or a name spelt twice.
		if a.Markers.Blank() && len(a.Resume) == 0 {
			return nil, fmt.Errorf("adapter: %s: %s says nothing about how to read or resume that agent", path, name)
		}
		merged[name] = a
		added = append(added, name)
	}
	kinds = merged
	sort.Strings(added)
	return added, nil
}
