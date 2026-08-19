package hub

import (
	"sync"
	"time"
)

// keyboards is who is typing at each agent.
//
// One at a time, because two people typing into one terminal interleave inside a
// word. Not a permission: anybody may take it from anybody, and everybody
// watching is told who has it — what stops two people typing at once is the same
// thing that stops it at a real keyboard, seeing that somebody else is.
type keyboards struct {
	mu sync.Mutex
	m  map[string]keyboard
}

// keyboard is one agent's typist. at identifies the connection rather than the
// member: the same person watching from two tabs is two hands, and only the tab
// they took it in should be able to type.
type keyboard struct {
	who   Person
	at    any
	since time.Time
}

func newKeyboards() *keyboards { return &keyboards{m: map[string]keyboard{}} }

// take hands the keyboard to a member, taking it from whoever had it. It reports
// who lost it, so the page can say so rather than have it change hands silently.
func (k *keyboards) take(agent string, who Person, at any) (was Person, taken bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	previous := k.m[agent]
	if previous.at == at {
		return Person{}, false
	}
	k.m[agent] = keyboard{who: who, at: at, since: time.Now()}
	return previous.who, true
}

// release gives up the keyboard, and does nothing if it has already moved on —
// which is what a browser closing after somebody else took it looks like.
func (k *keyboards) release(agent string, at any) bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	if held, ok := k.m[agent]; !ok || held.at != at {
		return false
	}
	delete(k.m, agent)
	return true
}

// holds reports whether this connection may type at this agent right now. Every
// keystroke is checked, because the keyboard can change hands between two of them.
func (k *keyboards) holds(agent string, at any) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	held, ok := k.m[agent]
	return ok && held.at == at
}

// holder is who has the keyboard, for catching up somebody who has just arrived.
func (k *keyboards) holder(agent string) (Person, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	held, ok := k.m[agent]
	return held.who, ok
}

func (k *keyboards) forget(agent string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, agent)
}
