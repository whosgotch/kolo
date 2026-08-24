// Package adapter holds what kolo knows about each kind of agent.
package adapter

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/whosgotch/kolo/internal/detect"
)

// See docs/reference.md, "Agents".
type Adapter struct {
	Markers detect.Markers `json:"markers"`
	// Appended to the command to resume the last conversation; empty means
	// it can't be resumed.
	Resume []string `json:"resume,omitempty"`
	// Appended on an agent's first start to bind its conversation to an id
	// kolo mints ({session}); the same id goes back in Resume at a restart,
	// so two agents can share one directory.
	Pin []string `json:"pin,omitempty"`
	// Appended when a restart has no id to resume by; only while this agent
	// is alone in its directory.
	Continue []string `json:"continue,omitempty"`
	// Locates the {session} id in Resume, read off the agent's screen.
	Session string `json:"session,omitempty"`
	// Key that stops this kind: "esc", "ctrl+c", or one character;
	// empty means Esc.
	Interrupt string `json:"interrupt,omitempty"`
}

const Esc = 0x1b

func (a Adapter) InterruptKey() []byte {
	keys, ok := parseKey(a.Interrupt)
	if !ok {
		return []byte{Esc}
	}
	return keys
}

func parseKey(name string) ([]byte, bool) {
	switch name = strings.ToLower(strings.TrimSpace(name)); name {
	case "", "esc", "escape":
		return []byte{Esc}, true
	}
	if letter, ok := strings.CutPrefix(name, "ctrl+"); ok {
		r := []rune(letter)
		if len(r) != 1 || r[0] < 'a' || r[0] > 'z' {
			return nil, false
		}
		return []byte{byte(r[0]-'a') + 1}, true
	}
	if r := []rune(name); len(r) == 1 && unicode.IsPrint(r[0]) {
		return []byte(name), true
	}
	return nil, false
}

const SessionPlaceholder = "{session}"

const maxSession = 200

func (a Adapter) ResumeArgs(session string) ([]string, bool) {
	if len(a.Resume) == 0 {
		return nil, false
	}
	if !slices.Contains(a.Resume, SessionPlaceholder) {
		// Nothing to fill: the resume asks for whatever ran here last.
		return slices.Clone(a.Resume), true
	}
	if session == "" {
		// The command line wants an id and nobody has one to give it.
		return nil, false
	}
	return withID(a.Resume, session), true
}

// PinArgs is what to append on a first start to give the conversation the id
// kolo minted for it. False for a kind that does not pin.
func (a Adapter) PinArgs(session string) ([]string, bool) {
	if len(a.Pin) == 0 || session == "" {
		return nil, false
	}
	return withID(a.Pin, session), true
}

// withID fills {session} in every argument. The placeholder is braces because
// an agent's own flags do not use them.
func withID(args []string, session string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, SessionPlaceholder, session)
	}
	return out
}

func (a Adapter) SessionFrom(screen string) string {
	if a.Session == "" {
		return ""
	}
	re := compiled(a.Session)
	if re == nil {
		return ""
	}
	found := re.FindAllStringSubmatch(screen, -1)
	if len(found) == 0 {
		return ""
	}
	id := strings.TrimSpace(found[len(found)-1][1])
	if id == "" || len(id) > maxSession || strings.ContainsFunc(id, unprintable) {
		return ""
	}
	return id
}

func unprintable(r rune) bool { return !unicode.IsPrint(r) }

// compiled caches one regexp per pattern: the screen is read every few hundred
// milliseconds per agent.
var (
	patternsMu sync.Mutex
	patterns   = map[string]*regexp.Regexp{}
)

func compiled(pattern string) *regexp.Regexp {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	if re, ok := patterns[pattern]; ok {
		return re
	}
	// Already validated by Load or the table below; Compile cannot fail here.
	re, _ := regexp.Compile(pattern)
	patterns[pattern] = re
	return re
}

var kinds = map[string]Adapter{
	"claude": {
		Markers: detect.Markers{
			Idle:           []string{"(shift+tab to cycle)", "? for shortcuts"},
			Busy:           "esc to interrupt",
			DialogFooter:   "Esc to cancel",
			DialogSelected: "❯",
		},
		Resume:    []string{"--resume", SessionPlaceholder},
		Pin:       []string{"--session-id", SessionPlaceholder},
		Continue:  []string{"--continue"},
		Interrupt: "esc",
	},
	"opencode": {
		Markers: detect.Markers{
			// Both states live in the status bar under the input box. Idle,
			// the right half reads "tab agents ctrl+p commands"; working, the
			// left half becomes "esc interrupt" and the rest stays — which is
			// why Of reads busy before idle. After a turn the tab hint gives
			// way to the directory and its context, so the command hint is
			// the one that is always there to be read.
			Idle: []string{"ctrl+p commands"},
			Busy: "esc interrupt",
			// A question draws its own box over the bar, and the last line of
			// it carries these hints. Which choice is selected is drawn in
			// colour alone and never reaches the text kolo reads, so there is
			// no sigil here to mistake for one.
			DialogFooter: "enter confirm",
		},
		Resume: []string{"--continue"},
	},
}

// Argv splits a command line into program and arguments, on whitespace only.
func Argv(command string) []string { return strings.Fields(command) }

// For looks up a kind by the binary name at the front of the command line.
func For(command string) Adapter {
	argv := Argv(command)
	if len(argv) == 0 {
		return Adapter{}
	}
	return kinds[filepath.Base(argv[0])]
}

// Kinds is every agent kind kolo knows, sorted.
func Kinds() []string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// catalog is discovery data, not knowledge: a name here carries no markers
// unless it is also in kinds or kinds.json.
var catalog = []string{
	"aider",
	"amp",
	"claude",
	"codex",
	"crush",
	"cursor-agent",
	"droid",
	"gemini",
	"kilo",
	"opencode",
	"qwen",
}

// Discovered is every name kolo has heard of that this machine can run,
// sorted: shipped kinds, kinds.json, and the catalog. What kolo up lends
// when nobody names anything.
func Discovered() []string {
	heard := make(map[string]bool, len(kinds)+len(catalog))
	for _, kind := range Kinds() {
		heard[kind] = true
	}
	for _, name := range catalog {
		if !heard[name] {
			heard[name] = true
		}
	}
	var found []string
	for name := range heard {
		if _, err := exec.LookPath(name); err == nil {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

// ResumesByName says whether a restart comes back as itself even beside
// another agent in the same directory: Session or Pin qualify, "last
// conversation here" does not.
func (a Adapter) ResumesByName() bool {
	if !slices.Contains(a.Resume, SessionPlaceholder) {
		return false
	}
	return a.Session != "" || len(a.Pin) > 0
}

func (a Adapter) validate() error {
	wants := slices.ContainsFunc(a.Resume, func(arg string) bool {
		return strings.Contains(arg, SessionPlaceholder)
	})
	pinned := len(a.Pin) > 0
	if _, ok := parseKey(a.Interrupt); !ok {
		return fmt.Errorf("interrupt: %q is not a key — try esc, ctrl+c, or a single character", a.Interrupt)
	}
	if pinned && !slices.Contains(a.Pin, SessionPlaceholder) {
		return fmt.Errorf("pin must carry %s — it stands for the id kolo minted for this conversation", SessionPlaceholder)
	}
	switch {
	case a.Session == "" && !pinned && wants:
		return fmt.Errorf("resume asks for %s, but nothing says where the id comes from — describe session, or pin one at birth", SessionPlaceholder)
	case a.Session != "" && !wants:
		return fmt.Errorf("session says how to read an id that resume never uses; put %s in it", SessionPlaceholder)
	case a.Session == "":
		return nil
	}
	re, err := regexp.Compile(a.Session)
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if re.NumSubexp() != 1 {
		return fmt.Errorf("session must capture exactly one thing — the id — and captures %d", re.NumSubexp())
	}
	return nil
}

// Load merges agent kinds from a JSON file (docs/reference.md) into the shipped
// ones, replacing any kind it names. Must run before any agent starts.
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
		if a.Markers.Blank() && len(a.Resume) == 0 {
			return nil, fmt.Errorf("adapter: %s: %s says nothing about how to read or resume that agent", path, name)
		}
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("adapter: %s: %s: %w", path, name, err)
		}
		merged[name] = a
		added = append(added, name)
	}
	kinds = merged
	sort.Strings(added)
	return added, nil
}
