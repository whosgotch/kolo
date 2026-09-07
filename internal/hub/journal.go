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

// What one entry says happened.
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
	keepEntries = 5000
	keepFor     = 30 * 24 * time.Hour
	maxSaid     = 500
)

// Entry is one thing that happened to one agent. Who is absent when nobody
// did it.
type Entry struct {
	At    time.Time `json:"at"`
	Agent string    `json:"agent"`
	What  string    `json:"what"`
	Who   Person    `json:"who,omitzero"`
	Text  string    `json:"text,omitempty"`
}

type journal struct {
	mu      sync.Mutex
	file    *os.File
	entries []Entry
	typing  map[string]line
	now     func() time.Time
}

type line struct {
	who  Person
	text string
}

func journalPath(org string) string {
	if org == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(org), "journal.jsonl")
}

// The journal returned is usable whether or not the error is nil, so a hub
// that cannot open the file carries on in memory rather than refusing to start.
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

func (j *journal) add(e Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.addLocked(e)
}

// Callers must hold j.mu.
func (j *journal) addLocked(e Entry) {
	e.At = j.now()
	switch e.What {
	case WhatSaid:
		e.Text = label(e.Text, maxSaid)
	default:
		e.Text = label(e.Text, maxLabel)
	}

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

// The temporary carries a name nothing else will pick, as in Org.replace: a
// shared one lets two writers rename each other's half-finished work into place.
func writeJournal(path string, entries []Entry) error {
	dir, base := filepath.Split(path)
	f, err := os.CreateTemp(dir, base+".*")
	if err != nil {
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	w := bufio.NewWriter(f)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(append(b, '\n'))
	}
	if err := cmp.Or(w.Flush(), f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hub: journal %s: %w", path, err)
	}
	return nil
}

// The lock is held throughout: two members typing at one agent otherwise read
// the same half-line, and the second to finish writes the first one's
// keystrokes back out.
func (j *journal) typed(agent string, who Person, keys string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	held := j.typing[agent]
	held.who = who
	for _, r := range strip(keys) {
		switch r {
		case '\r', '\n':
			said := strings.TrimSpace(held.text)
			held.text = ""
			if said != "" {
				j.addLocked(Entry{Agent: agent, What: WhatSaid, Who: who, Text: said})
			}
		case 0x7f, 0x08:
			if n := len(held.text); n > 0 {
				_, size := utf8.DecodeLastRuneInString(held.text)
				held.text = held.text[:n-size]
			}
		default:
			if len(held.text) < maxSaid {
				held.text += string(r)
			}
		}
	}

	if held.text == "" {
		delete(j.typing, agent)
		return
	}
	j.typing[agent] = held
}

func (j *journal) forget(agent string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.typing, agent)
}

func strip(keys string) string {
	var b strings.Builder
	for i := 0; i < len(keys); i++ {
		if keys[i] != 0x1b {
			b.WriteByte(keys[i])
			continue
		}
		i++
		if i < len(keys) && (keys[i] == '[' || keys[i] == 'O') {
			for i++; i < len(keys) && keys[i] >= 0x20 && keys[i] < 0x40; i++ {
			}
		}
	}
	return strings.ToValidUTF8(b.String(), "")
}
