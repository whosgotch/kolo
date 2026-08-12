package hub

import (
	"sync"

	"github.com/whosgotch/kolo/internal/session"
)

// screens holds one live terminal per agent.
//
// The hub keeps its own copy of each screen rather than passing bytes through.
// It costs a terminal emulator per agent and buys the thing a pipe cannot do:
// somebody opening an agent is repainted from the screen as it stands, without
// asking the host for it and without the redraw landing in front of everyone
// already watching.
type screens struct {
	mu sync.Mutex
	m  map[string]*session.Session
}

func newScreens() *screens { return &screens{m: map[string]*session.Session{}} }

// open starts a screen for an agent, replacing whatever was there.
//
// A replacement is what a restarted agent looks like from here: a new process
// with a new screen. Viewers of the old one are dropped and reconnect, which is
// the honest outcome — the alternative is repainting them with a picture of a
// process that no longer exists.
func (s *screens) open(name string, cols, rows int) *session.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	if was, ok := s.m[name]; ok {
		was.Close()
	}
	live := session.New(cols, rows)
	s.m[name] = live
	return live
}

func (s *screens) get(name string) (*session.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.m[name]
	return live, ok
}

// close ends a screen, unless it has already been replaced by a newer one. The
// check matters when an agent reconnects before its old connection has finished
// tidying up: closing then would take down the screen that just arrived.
func (s *screens) close(name string, live *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m[name] == live {
		delete(s.m, name)
		live.Close()
	}
}
