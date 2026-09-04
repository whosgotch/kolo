package hub

import (
	"sync"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
)

// screens holds one live terminal per agent, so joiners get a repaint without
// asking the host.
type screens struct {
	mu sync.Mutex
	m  map[string]*session.Session
	// Per-browser window sizes; agreed is the size last asked of the host.
	windows map[string]map[any]size
	agreed  map[string]size
}

type size struct{ cols, rows int }

// The smallest and largest terminal kolo will ask a host for. The roof is
// there because a proposal is a number a browser sent: the hub builds a grid
// that size to draw on, and an agent asked for a screen wider than any window
// draws nothing anybody can read.
var (
	floor = size{cols: 60, rows: 15}
	roof  = size{cols: 400, rows: 150}
)

func newScreens() *screens {
	return &screens{
		m:       map[string]*session.Session{},
		windows: map[string]map[any]size{},
		agreed:  map[string]size{},
	}
}

// propose records one browser's size and returns the size all can use,
// smallest wins as in tmux. A zero size removes the browser.
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

	if len(windows) == 0 {
		return 0, 0, false
	}
	smallest := size{cols: 1 << 30, rows: 1 << 30}
	for _, w := range windows {
		smallest.cols = min(smallest.cols, w.cols)
		smallest.rows = min(smallest.rows, w.rows)
	}
	smallest.cols = min(max(smallest.cols, floor.cols), roof.cols)
	smallest.rows = min(max(smallest.rows, floor.rows), roof.rows)

	if s.agreed[name] == smallest {
		return 0, 0, false
	}
	s.agreed[name] = smallest
	if live, ok := s.m[name]; ok {
		live.Resize(smallest.cols, smallest.rows)
	}
	return smallest.cols, smallest.rows, true
}

// open replaces any existing screen, dropping its viewers.
//
// The agreed size goes with the old screen. A host opens every screen at the
// size it starts every PTY at, so what was agreed about the screen before this
// one describes a terminal that no longer exists. Kept, it matches the
// proposal each dropped viewer makes on reconnecting, reads as no change, and
// leaves the hub drawing the host's size into windows that are not that size,
// with the host never told. The close below clears it too, but only when it
// wins the race to notice: a host that dropped off the network opens the new
// screen long before the old connection is known to be dead.
func (s *screens) open(name string, cols, rows int, markers detect.Markers) *session.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	if was, ok := s.m[name]; ok {
		was.Close()
	}
	delete(s.agreed, name)
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
		delete(s.windows, name)
		delete(s.agreed, name)
		live.Close()
	}
}
