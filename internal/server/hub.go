// Package server streams the agent's terminal to a viewer in the browser.
package server

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

// Hub owns the virtual terminal and fans the agent's output out to the viewers.
//
// Every viewer gets its own subscription. An earlier version allowed one at a
// time and let a new connection take over from the old, which was wrong twice
// over: guests are meant to watch together, and a page that reconnects on its
// own turns a takeover into two viewers knocking each other offline in a loop.
type Hub struct {
	mu     sync.Mutex
	screen *term.Screen
	subs   map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan Message
	closed bool
}

// NewHub returns a Hub whose terminal is cols x rows.
func NewHub(cols, rows int) *Hub {
	return &Hub{screen: term.New(cols, rows), subs: map[*subscriber]struct{}{}}
}

// Viewers is how many are currently watching.
func (h *Hub) Viewers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Write feeds agent output into the virtual terminal and on to the viewers. It
// never fails and never blocks on a viewer: this sits in the path between the
// agent and the host's own terminal, so stalling here would stall the host.
func (h *Hub) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.screen.Write(p)
	h.send(Message{Data: append([]byte(nil), p...)})
	return len(p), nil
}

// State reads the agent's screen and reports whether it could take a line now.
// It is what the relay asks before releasing anything, so it reads the same
// screen the viewer is looking at rather than a second, separately maintained
// one.
func (h *Hub) State() detect.State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return detect.Of(h.screen.Text())
}

// Announce sends the viewer a control frame describing something that happened
// to a guest's message.
func (h *Hub) Announce(event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.send(Message{Control: true, Data: b})
}

// Resize follows the host's terminal and tells the viewer to match.
func (h *Hub) Resize(cols, rows int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.screen.Resize(cols, rows)
	h.send(sizeMessage(cols, rows))
}

// Subscribe adds a viewer and returns everything it needs to start: the size, a
// repaint of the screen as it is right now, and the stream of what happens next.
// Viewers already watching are undisturbed.
//
// The snapshot is taken and the subscription created in one critical section
// with Write. Any gap between them would show up as terminal output the viewer
// either never receives or receives twice, and neither is recoverable — a
// half-applied escape sequence corrupts the screen from then on.
func (h *Hub) Subscribe() (backlog []Message, stream <-chan Message, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := &subscriber{ch: make(chan Message, viewerBuffer)}
	h.subs[s] = struct{}{}

	cols, rows := h.screen.Size()
	backlog = []Message{sizeMessage(cols, rows), {Data: h.screen.Snapshot()}}

	return backlog, s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.drop(s)
	}
}

// send queues a message for every viewer, dropping any that has fallen too far
// behind. Skipping bytes instead is not an option: a viewer is rendering an
// escape-sequence stream, so a gap corrupts it permanently. Being disconnected
// costs that viewer a reconnect and a fresh snapshot, and costs the others
// nothing.
//
// Callers must hold h.mu.
func (h *Hub) send(m Message) {
	for s := range h.subs {
		select {
		case s.ch <- m:
		default:
			h.drop(s)
		}
	}
}

// drop disconnects one viewer. Callers must hold h.mu.
func (h *Hub) drop(s *subscriber) {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	delete(h.subs, s)
}

func sizeMessage(cols, rows int) Message {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{"size", cols, rows})
	return Message{Control: true, Data: b}
}
