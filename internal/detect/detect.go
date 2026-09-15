// Package detect reads an agent's screen to tell what it's doing. The zero
// Markers matches nothing; see docs/reference.md, "Agents".
package detect

import (
	"encoding/json"
	"fmt"
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
	// silence proves nothing. Seconds in JSON: see MarshalJSON.
	Settle time.Duration `json:"settle,omitempty"`
}

// plain is Markers with no methods, so the two below can embed every marker
// without this file listing them again and drifting when one is added.
type plain Markers

// MarshalJSON writes settle as seconds, which is what kinds.json documents and
// what somebody describing an agent would write.
//
// Both halves of the pair are needed, and needed together. encoding/json
// reads a bare time.Duration as its nanosecond count, so the documented
// "settle": 3 used to mean three nanoseconds, and every screen read as idle
// the moment it was asked. A host also marshals its markers to the hub, so
// a reader expecting seconds beside a writer emitting nanoseconds would turn
// two seconds into sixty-three years in transit.
func (m Markers) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		plain
		Settle float64 `json:"settle,omitempty"`
	}{plain(m), m.Settle.Seconds()})
}

func (m *Markers) UnmarshalJSON(b []byte) error {
	var v struct {
		plain
		// Shallower than the embedded field, so this is the one json fills.
		Settle json.RawMessage `json:"settle,omitempty"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	settle, err := parseSettle(v.Settle)
	if err != nil {
		return err
	}
	*m = Markers(v.plain)
	m.Settle = settle
	return nil
}

// parseSettle takes the seconds a person writes, or a duration string for
// anyone who would rather be explicit than count zeroes.
func parseSettle(raw json.RawMessage) (time.Duration, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		d, err := time.ParseDuration(text)
		if err != nil {
			return 0, fmt.Errorf("settle: %q is not a length of time; write 3 for three seconds, or \"1500ms\"", text)
		}
		return nonNegative(d)
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return 0, fmt.Errorf("settle: %s is neither a number of seconds nor a duration like \"1500ms\"", raw)
	}
	return nonNegative(time.Duration(seconds * float64(time.Second)))
}

func nonNegative(d time.Duration) (time.Duration, error) {
	if d < 0 {
		return 0, fmt.Errorf("settle: %s is a negative length of time, and a screen cannot sit still for less than no time", d)
	}
	return d, nil
}

// Blank reports markers that match nothing, as an unconfigured kind has. A
// settle on its own still reads silence as idle, so it is not nothing.
func (m Markers) Blank() bool {
	return len(m.Idle) == 0 && m.Busy == "" && m.DialogFooter == "" &&
		m.DialogSelected == "" && m.Settle == 0
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
