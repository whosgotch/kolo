// Package session holds one agent's screen and fans it out to viewers.
package session

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/term"
)

const viewerBuffer = 256

// Message is one frame on the viewer websocket.
type Message struct {
	Control bool
	Data    []byte
}

// Session owns the virtual terminal and fans its output out to every viewer.
type Session struct {
	markers detect.Markers

	mu     sync.Mutex
	screen *term.Screen
	subs   map[*subscriber]struct{}
	// Only used for markers whose idle is silence.
	settled   string
	settledAt time.Time
}

type subscriber struct {
	ch     chan Message
	closed bool
}

func New(cols, rows int, markers detect.Markers) *Session {
	return &Session{
		markers:   markers,
		screen:    term.New(cols, rows),
		subs:      map[*subscriber]struct{}{},
		settledAt: time.Now(),
	}
}

func (s *Session) Markers() detect.Markers { return s.markers }

func (s *Session) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Write never blocks: it sits inline between the agent and the host, so
// stalling on a slow viewer would stall the agent.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.screen.Write(p)
	s.noteChange()
	s.send(Message{Data: append([]byte(nil), p...)})
	return len(p), nil
}

func (s *Session) State() detect.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markers.OfSettled(s.screen.Text(), time.Since(s.settledAt))
}

func (s *Session) Screen() (text string, still time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Text(), time.Since(s.settledAt)
}

// Callers must hold s.mu.
func (s *Session) noteChange() {
	if s.markers.Settle == 0 {
		return
	}
	if text := s.screen.Text(); text != s.settled {
		s.settled, s.settledAt = text, time.Now()
	}
}

// Size is the grid the screen is being drawn on.
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Size()
}

func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Text()
}

func (s *Session) Announce(event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(Message{Control: true, Data: b})
}

func (s *Session) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.screen.Resize(cols, rows)
	s.send(sizeMessage(cols, rows))
}

// Subscribe takes the backlog and registers the stream in the same critical
// section as Write, so a joining viewer misses or duplicates no bytes.
func (s *Session) Subscribe() (backlog []Message, stream <-chan Message, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub := &subscriber{ch: make(chan Message, viewerBuffer)}
	s.subs[sub] = struct{}{}

	cols, rows := s.screen.Size()
	backlog = []Message{sizeMessage(cols, rows), {Data: s.screen.Snapshot()}}

	return backlog, sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.drop(sub)
	}
}

// Close disconnects every viewer.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		s.drop(sub)
	}
}

// send drops any viewer that's fallen behind: skipping bytes would corrupt
// its escape-sequence stream. Callers must hold s.mu.
func (s *Session) send(m Message) {
	for sub := range s.subs {
		select {
		case sub.ch <- m:
		default:
			s.drop(sub)
		}
	}
}

// Callers must hold s.mu.
func (s *Session) drop(sub *subscriber) {
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
	delete(s.subs, sub)
}

func sizeMessage(cols, rows int) Message {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{"size", cols, rows})
	return Message{Control: true, Data: b}
}
