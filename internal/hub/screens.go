package hub

import (
	"sync"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
)

// screens holds one live terminal per agent. A screen stays the size the host
// opened it at; browsers scale it to their own window.
type screens struct {
	mu sync.Mutex
	m  map[string]*session.Session
}

func newScreens() *screens {
	return &screens{m: map[string]*session.Session{}}
}

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

func (s *screens) close(name string, live *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m[name] == live {
		delete(s.m, name)
		live.Close()
	}
}
