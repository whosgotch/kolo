package hub

import (
	"sync"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
)

// screens holds one live terminal per agent, so joiners get a repaint without
// asking the host.
//
// A screen is the size the host opened it at, and nothing here moves it. What
// a browser does with that grid, how large it draws it in the room it has, is
// the one thing about the screen that is nobody else's business.
//
// Kolo used to let the browsers settle the size between them, smallest window
// winning as in tmux. That made every window a vote on everybody else's: one
// phone on the invite link and the agent redrew its whole interface narrow,
// for the people sitting at the machine included. Largest wins only moves who
// pays. Nobody voting is the answer, because the agent draws one grid and the
// browsers can each draw it at their own size.
type screens struct {
	mu sync.Mutex
	m  map[string]*session.Session
}

func newScreens() *screens {
	return &screens{m: map[string]*session.Session{}}
}

// open replaces any existing screen, dropping its viewers.
func (s *screens) open(name string, cols, rows int, markers detect.Markers) *session.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	if was, ok := s.m[name]; ok {
		was.Close()
	}
	live := session.New(cols, rows, markers)
	s.m[name] = live
	return live
}

func (s *screens) get(name string) (*session.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.m[name]
	return live, ok
}

// close ends a screen unless it's already been replaced by a newer one.
func (s *screens) close(name string, live *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m[name] == live {
		delete(s.m, name)
		live.Close()
	}
}
