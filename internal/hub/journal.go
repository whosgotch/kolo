package hub

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// What one entry says happened. The set is small on purpose: a journal nobody
// reads to the end is one nobody trusts.
const (
	WhatCreated     = "created"
	WhatSaid        = "said"
	WhatInterrupted = "interrupted"
	WhatRestarted   = "restarted"
	WhatFresh       = "fresh"
	WhatRelabeled   = "relabeled"
	WhatStopped     = "stopped"
	WhatFailed      = "failed"
	WhatGone        = "gone"
)

const (
	// How much history is kept. Both bounds apply: an org that talks all day
	// stops at the count, one that works a week a month stops at the age.
	keepEntries = 5000
	keepFor     = 30 * 24 * time.Hour

	// A said line is longer than a label because it is somebody's sentence.
	maxSaid = 500
)

// Entry is one thing that happened to one agent, and who did it. Who is absent
// when nobody did — an agent going quiet is news with no author.
type Entry struct {
	At    time.Time `json:"at"`
	Agent string    `json:"agent"`
	What  string    `json:"what"`
	Who   Person    `json:"who,omitzero"`
	Text  string    `json:"text,omitempty"`
}

// journal is the org's record of who asked for what.
//
// It lives on the hub because that is the only place a member's identity is
// known, and because it has to outlive the machines: a host that disconnects
// takes its agents off the list, and the record of what they did is not the
// host's to take with it.
type journal struct {
	mu      sync.Mutex
	file    *os.File
	entries []Entry
	// A partly typed line per agent, waiting for the Enter that makes it a
	// sentence. See typed.
	typing map[string]line
	now    func() time.Time
}

type line struct {
	who  Person
	text string
}

// journalPath puts the journal beside the org file it belongs to. An org with no
// file — a test's — gets no journal file either.
func journalPath(org string) string {
	if org == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(org), "journal.jsonl")
}

// openJournal reads back what is on disk and appends to it. The journal it
// returns is usable whether or not the error is nil: losing the history is bad,
// and a hub that will not start because of it is worse, so a caller that cannot
// open the file says so and carries on in memory.
func openJournal(path string) (*journal, error) {
	j := &journal{typing: map[string]line{}, now: time.Now}
	if path == "" {
		return j, nil
	}

	kept, trimmed, err := readJournal(path, j.now())
	if err != nil {
		return j, err
	}
	j.entries = kept

	// Rewritten only when something was dropped, so the usual start is a read
	// and an open rather than a rewrite of the whole file.
	if trimmed {
		if err := writeJournal(path, kept); err != nil {
			return j, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return j, fmt.Errorf("hub: journal %s: %w", path, err)
	}
	j.file = f
	return j, nil
}

func (j *journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

// add records something that happened. Failing to write it down is not worth
// failing the action that was written about, so the error goes nowhere: the
// entry is in memory either way and the next read still sees it.
func (j *journal) add(e Entry) {
	e.At = j.now()
	switch e.What {
	case WhatSaid:
		e.Text = label(e.Text, maxSaid)
	default:
		e.Text = label(e.Text, maxLabel)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.entries = append(j.entries, e)
	if len(j.entries) > keepEntries {
		j.entries = j.entries[len(j.entries)-keepEntries:]
	}
	if j.file == nil {
		return
	}
	if b, err := json.Marshal(e); err == nil {
		j.file.Write(append(b, '\n'))
	}
}

// tail is the last entries, oldest first. An empty agent is every agent, which
// is what the list of agents wants and one agent's page does not.
func (j *journal) tail(agent string, limit int) []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]Entry, 0, min(limit, len(j.entries)))
	for i := len(j.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if agent == "" || j.entries[i].Agent == agent {
			out = append(out, j.entries[i])
		}
	}
	slices.Reverse(out)
	return out
}

// readJournal loads a journal file, dropping what has aged out. A line that will
// not parse is skipped rather than fatal: the tail of a file a machine died
// while writing is half a line, and the rest of the history is still good.
func readJournal(path string, now time.Time) (kept []Entry, trimmed bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("hub: journal %s: %w", path, err)
	}
	defer f.Close()

	cutoff := now.Add(-keepFor)
	read := bufio.NewScanner(f)
	read.Buffer(make([]byte, 0, 4<<10), 64<<10)
	for read.Scan() {
		var e Entry
		if json.Unmarshal(read.Bytes(), &e) != nil {
			trimmed = true
			continue
		}
		if e.At.Before(cutoff) {
			trimmed = true
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) > keepEntries {
		kept, trimmed = kept[len(kept)-keepEntries:], true
	}
	return kept, trimmed, nil
}

func writeJournal(path string, entries []Entry) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(append(b, '\n'))
	}
	if err := cmp.Or(w.Flush(), f.Sync(), f.Close()); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	return nil
}

// typed reconstructs the line a member is sending from the keystrokes the hub is
// passing on, and records it when they press Enter.
//
// A reconstruction rather than a transcript: the hub sees keys, not messages,
// and reading the message off the screen instead would need the per-agent
// knowledge the hub deliberately does not have. A paste, a completion the agent
// filled in, or a menu chosen with the arrow keys will not read back exactly.
//
// Nothing is written down until Enter, so a line abandoned half typed is never
// recorded, and the member credited is the one whose keys the hub passed on.
func (j *journal) typed(agent string, who Person, keys string) {
	j.mu.Lock()
	held := j.typing[agent]
	j.mu.Unlock()

	held.who = who
	for _, r := range strip(keys) {
		switch r {
		case '\r', '\n':
			said := strings.TrimSpace(held.text)
			held.text = ""
			if said != "" {
				j.add(Entry{Agent: agent, What: WhatSaid, Who: who, Text: said})
			}
		case 0x7f, 0x08:
			if n := len(held.text); n > 0 {
				_, size := utf8.DecodeLastRuneInString(held.text)
				held.text = held.text[:n-size]
			}
		default:
			// Bounded here as well as at the entry, so a member holding a key down
			// cannot grow the buffer without ever pressing Enter.
			if len(held.text) < maxSaid {
				held.text += string(r)
			}
		}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if held.text == "" {
		delete(j.typing, agent)
		return
	}
	j.typing[agent] = held
}

// forget drops a half typed line. What interrupts, restarts and stops have in
// common is that whatever was on the input line is not going to be sent.
func (j *journal) forget(agent string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.typing, agent)
}

// strip removes the escape sequences a browser's terminal encodes arrow keys and
// the like as, leaving the characters that make up a sentence. Anything it does
// not understand it drops, which is the right way round: a stray escape in the
// journal is somebody else's terminal doing something surprising.
func strip(keys string) string {
	var b strings.Builder
	for i := 0; i < len(keys); i++ {
		if keys[i] != 0x1b {
			b.WriteByte(keys[i])
			continue
		}
		// CSI and OSC run until a byte in their own range; anything else after an
		// escape is a two-byte sequence.
		i++
		if i < len(keys) && (keys[i] == '[' || keys[i] == 'O') {
			for i++; i < len(keys) && keys[i] >= 0x20 && keys[i] < 0x40; i++ {
			}
		}
	}
	return strings.ToValidUTF8(b.String(), "")
}
