// Package session holds one agent's screen and the viewers watching it.
//
// It is used twice over: by the viewer an agent serves on its own machine, and
// by the hub, which holds a session for each agent connected to the org. Both
// need the same thing — a screen kept current, a way to catch somebody up to it,
// and a fan-out of what happens next — so both use this.
package session

import (
	"encoding/json"
	"sync"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/term"
)

// viewerBuffer is how far the viewer may fall behind, in messages, before it is
// dropped rather than slowed down.
const viewerBuffer = 256

// Message is one thing to send to the viewer: either a control frame or a chunk
// of the agent's raw terminal output.
type Message struct {
	Control bool
	Data    []byte
}

// Session owns the virtual terminal and fans the agent's output out to the viewers.
//
// Every viewer gets its own subscription. An earlier version allowed one at a
// time and let a new connection take over from the old, which was wrong twice
// over: guests are meant to watch together, and a page that reconnects on its
// own turns a takeover into two viewers knocking each other offline in a loop.
type Session struct {
	mu     sync.Mutex
	screen *term.Screen
	subs   map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan Message
	closed bool
}

// NewHub returns a Hub whose terminal is cols x rows.
func New(cols, rows int) *Session {
	return &Session{screen: term.New(cols, rows), subs: map[*subscriber]struct{}{}}
}

// Viewers is how many are currently watching.
func (s *Session) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Write feeds agent output into the virtual terminal and on to the viewers. It
// never fails and never blocks on a viewer: this sits in the path between the
// agent and the host's own terminal, so stalling here would stall the host.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.screen.Write(p)
	s.send(Message{Data: append([]byte(nil), p...)})
	return len(p), nil
}

// State reads the agent's screen and reports whether it could take a line now.
// It is what the relay asks before releasing anything, so it reads the same
// screen the viewer is looking at rather than a second, separately maintained
// one.
func (s *Session) State() detect.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return detect.Of(s.screen.Text())
}

// Announce sends the viewer a control frame describing something that happened
// to a guest's message.
func (s *Session) Announce(event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(Message{Control: true, Data: b})
}

// Resize follows the host's terminal and tells the viewer to match.
func (s *Session) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.screen.Resize(cols, rows)
	s.send(sizeMessage(cols, rows))
}

// Subscribe adds a viewer and returns everything it needs to start: the size, a
// repaint of the screen as it is right now, and the stream of what happens next.
// Viewers already watching are undisturbed.
//
// The snapshot is taken and the subscription created in one critical section
// with Write. Any gap between them would show up as terminal output the viewer
// either never receives or receives twice, and neither is recoverable — a
// half-applied escape sequence corrupts the screen from then on.
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

// send queues a message for every viewer, dropping any that has fallen too far
// behind. Skipping bytes instead is not an option: a viewer is rendering an
// escape-sequence stream, so a gap corrupts it permanently. Being disconnected
// costs that viewer a reconnect and a fresh snapshot, and costs the others
// nothing.
//
// Callers must hold s.mu.
func (s *Session) send(m Message) {
	for sub := range s.subs {
		select {
		case sub.ch <- m:
		default:
			s.drop(sub)
		}
	}
}

// drop disconnects one viewer. Callers must hold s.mu.
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
