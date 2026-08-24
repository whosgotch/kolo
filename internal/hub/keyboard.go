package hub

import "sync"

// typists remembers who typed at an agent last, so watchers joining mid-word
// know whose keys are moving the screen.
type typists struct {
	mu sync.Mutex
	m  map[string]Person
}

func newTypists() *typists { return &typists{m: map[string]Person{}} }

// set records who typed last and reports who had it before, if that changed.
func (t *typists) set(agent string, who Person) (was Person, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	previous, ok := t.m[agent]
	if ok && previous == who {
		return Person{}, false
	}
	t.m[agent] = who
	return previous, true
}

func (t *typists) get(agent string) (Person, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	who, ok := t.m[agent]
	return who, ok
}
