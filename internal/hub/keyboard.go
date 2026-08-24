package hub

import (
	"sync"
	"time"
)

type keyboards struct {
	mu sync.Mutex
	m  map[string]keyboard
}

// keyboard is one agent's typist; at identifies the connection, not the member.
type keyboard struct {
	who   Person
	at    any
	since time.Time
}

func newKeyboards() *keyboards { return &keyboards{m: map[string]keyboard{}} }

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

func (k *keyboards) release(agent string, at any) bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	if held, ok := k.m[agent]; !ok || held.at != at {
		return false
	}
	delete(k.m, agent)
	return true
}

func (k *keyboards) holds(agent string, at any) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	held, ok := k.m[agent]
	return ok && held.at == at
}

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
