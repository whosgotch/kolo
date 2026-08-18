package hub

import (
	"sync"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
)

// screens holds one live terminal per agent.
//
// The hub keeps its own copy rather than passing bytes through. It costs an
// emulator per agent and buys what a pipe cannot: somebody opening an agent is
// repainted from the screen as it stands, without asking the host and without the
// redraw landing in front of everyone already watching.
type screens struct {
	mu sync.Mutex
	m  map[string]*session.Session
	// What each browser watching each agent says it can draw, and the size last
	// agreed from them.
	windows map[string]map[any]size
	agreed  map[string]size
}

type size struct{ cols, rows int }

// The smallest terminal kolo will ask a host for. Below this the agent's own
// interface stops fitting on its own screen, and a phone in somebody's pocket
// would take the room's agent down to a size nobody can work at.
var floor = size{cols: 60, rows: 15}

func newScreens() *screens {
	return &screens{
		m:       map[string]*session.Session{},
		windows: map[string]map[any]size{},
		agreed:  map[string]size{},
	}
}

// propose records what one browser can draw and returns the size every browser
// can, when that has changed.
//
// The smallest wins, as it does in tmux and for the same reason: one grid is
// being shown in several windows at once, and anything larger than the smallest
// window is drawn by an agent that cannot see where it is being cut off. A zero
// size is a browser leaving.
func (s *screens) propose(name string, at any, cols, rows int) (int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	windows, ok := s.windows[name]
	if !ok {
		windows = map[any]size{}
		s.windows[name] = windows
	}
	if cols <= 0 || rows <= 0 {
		delete(windows, at)
	} else {
		windows[at] = size{cols, rows}
	}

	// Nobody watching leaves the agent at whatever it has: resizing a terminal
	// with no one in front of it only costs the agent a redraw.
	if len(windows) == 0 {
		return 0, 0, false
	}
	smallest := size{cols: 1 << 30, rows: 1 << 30}
	for _, w := range windows {
		smallest.cols = min(smallest.cols, w.cols)
		smallest.rows = min(smallest.rows, w.rows)
	}
	smallest.cols = max(smallest.cols, floor.cols)
	smallest.rows = max(smallest.rows, floor.rows)

	if s.agreed[name] == smallest {
		return 0, 0, false
	}
	s.agreed[name] = smallest
	// The hub's own screen follows, or somebody joining later is repainted from a
	// grid the size the agent has stopped using.
	if live, ok := s.m[name]; ok {
		live.Resize(smallest.cols, smallest.rows)
	}
	return smallest.cols, smallest.rows, true
}

// open starts a screen for an agent, replacing whatever was there — which is what
// a restarted agent looks like from here. Viewers of the old one are dropped and
// reconnect, rather than being repainted with a process that no longer exists.
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

// close ends a screen, unless it has already been replaced by a newer one. The
// check matters when an agent reconnects before its old connection has finished
// tidying up: closing then would take down the screen that just arrived.
func (s *screens) close(name string, live *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m[name] == live {
		delete(s.m, name)
		delete(s.windows, name)
		delete(s.agreed, name)
		live.Close()
	}
}
