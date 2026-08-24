// Package adapter holds what kolo knows about each kind of agent. A kind with no
// adapter gets the zero Adapter, which is inert: no marker matches and it cannot
// be resumed.
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

// See docs/architecture.md "What kolo knows about each agent kind".
type Adapter struct {
	Markers detect.Markers `json:"markers"`
	// Resume is appended to the command to bring back the last conversation.
	// Empty means the kind cannot be resumed, so every restart of one is fresh.
	//
	// An agent that resumes by naming a particular conversation carries
	// {session} in one of these, and the id comes from Session or Pin below.
	Resume []string `json:"resume,omitempty"`
	// Pin is appended on an agent's first start to bind its conversation to
	// an id kolo mints itself, so the two belong to each other from birth
	// regardless of what appears on screen. It carries {session}, and the
	// same id goes back in Resume at a restart.
	//
	// A kind with Pin needs no session pattern: the host named the
	// conversation before the agent could say anything. An agent that honours
	// it — claude's --session-id — is resumed by identity rather than by
	// asking for whatever ran in that directory last, which is what lets two
	// of them share one directory safely.
	Pin []string `json:"pin,omitempty"`
	// Continue is what to append when a restart would like the last
	// conversation but no id names it: an agent started before kolo recorded
	// identities, or whose state went missing. Used only while this agent is
	// alone in its directory, where "the last conversation here" can only be
	// its own; beside another agent the answer is a fresh start that says so,
	// because coming back as somebody else is not continuity.
	Continue []string `json:"continue,omitempty"`
	// Session is a pattern whose first capture is the id this kind's resume
	// command needs, matched against the agent's own screen — the same screen
	// everything else about the agent is read off, because the id is something
	// the agent said rather than something kolo arranged.
	//
	// Empty for a kind that resumes without naming anything.
	Session string `json:"session,omitempty"`
	// Interrupt is the key that stops this kind working, named rather than
	// spelt as a byte, because a control character is not a thing to put in a
	// configuration file by hand: "esc", "ctrl+c", or a single character.
	//
	// Empty means Esc, which is what most of these agents use and what kolo did
	// for every kind before this was a choice.
	Interrupt string `json:"interrupt,omitempty"`
}

// Esc is what an unnamed interrupt key is.
const Esc = 0x1b

// InterruptKey is what to send to stop this kind working — the bytes a person
// pressing that key at the agent's own terminal would send, because that is the
// only thing the agent is listening for.
//
// Ctrl-C included: an agent in raw mode reads it as a byte rather than being
// signalled, which is exactly what pressing it in front of the agent does.
func (a Adapter) InterruptKey() []byte {
	keys, ok := parseKey(a.Interrupt)
	if !ok {
		// Refused by validate at startup, so this is a kind kolo ships with and
		// a test would have caught it.
		return []byte{Esc}
	}
	return keys
}

// parseKey turns a key's name into what pressing it sends.
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
	// A plain character, for an agent that stops on a letter of its own. One
	// character: a word here is somebody expecting kolo to type at their agent,
	// which is what taking the keyboard is for.
	if r := []rune(name); len(r) == 1 && unicode.IsPrint(r[0]) {
		return []byte(name), true
	}
	return nil, false
}

// SessionPlaceholder stands in the resume command for the id read off the
// screen. Braces because an agent's own flags do not use them.
const SessionPlaceholder = "{session}"

// Longer than any id worth having, and a bound on what a greedy pattern can
// drag onto a command line.
const maxSession = 200

// ResumeArgs is what to append to the command line to bring back this agent's
// last conversation, and whether that can be done at all.
//
// session is the id last read off its screen. A kind that needs one and has
// never been given one cannot resume — which is a fresh start that says so,
// the same as a resume the agent itself refuses.
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

// SessionFrom reads this agent's session id off its screen, or returns empty
// when the screen is not carrying one — which is most of the time, an id being
// something an agent says once.
//
// The last match wins rather than the first: an agent told to start a new
// conversation says so on the same screen, and resuming the one before it would
// bring back what somebody just cleared.
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
	// A pattern that drags in half the screen is a pattern to fix, not an id to
	// put on a command line.
	if id == "" || len(id) > maxSession || strings.ContainsFunc(id, unprintable) {
		return ""
	}
	return id
}

func unprintable(r rune) bool { return !unicode.IsPrint(r) }

// compiled keeps one regexp per pattern: the screen is read every few hundred
// milliseconds per agent, and the set of patterns is the set of agent kinds.
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
	// Errors are impossible here for a pattern that came through Load or the
	// table below, both of which compile it first.
	re, _ := regexp.Compile(pattern)
	patterns[pattern] = re
	return re
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

// catalog is every CLI agent kolo has heard a name for. It is discovery data,
// not knowledge: none of these carries markers unless it is also in kinds or
// kinds.json, and an agent found here runs exactly as one kolo has never heard
// of — watched and typed at, its screen unread. A name joins this list when
// there is evidence it is real and people run it, not on sight of a headline.
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

// Discovered is every name kolo has heard of that this machine can actually
// run, sorted: the kinds it ships with and knows from kinds.json, plus the
// catalog above. This is what `kolo up` lends when nobody names anything, so
// an agent installed but unknown to kolo still appears without being asked
// for — and reads as unknown until somebody describes it.
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

// ResumesByName says whether a restart of this kind comes back as itself even
// beside another agent in the same directory. Two shapes qualify: the kind
// names its conversation on screen and kolo reads it (Session), or the host
// named the conversation at birth and the agent honoured it (Pin). What never
// qualifies is a kind that asks for "the last conversation in this directory"
// — beside a neighbour, a restart would come back wearing somebody else's
// context and calling it its own.
func (a Adapter) ResumesByName() bool {
	if !slices.Contains(a.Resume, SessionPlaceholder) {
		return false
	}
	return a.Session != "" || len(a.Pin) > 0
}

// validate refuses a kind that would fail later, at a restart somebody was
// counting on, with nothing on screen to point at.
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
