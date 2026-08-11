// Package server streams the agent's terminal to a viewer in the browser.
package server

import (
	"encoding/json"
	"sync"

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

// Hub owns the virtual terminal and fans the agent's output out to the viewer.
//
// Milestone 1 serves a single viewer. A second connection takes over from the
// first rather than being refused, so that a viewer whose browser tab died
// without closing the socket cannot lock everyone else out.
type Hub struct {
	mu     sync.Mutex
	screen *term.Screen
	sub    *subscriber
}

type subscriber struct {
	ch     chan Message
	closed bool
}

// NewHub returns a Hub whose terminal is cols x rows.
func NewHub(cols, rows int) *Hub {
	return &Hub{screen: term.New(cols, rows)}
}

// Write feeds agent output into the virtual terminal and on to the viewer. It
// never fails and never blocks on the viewer: this sits in the path between the
// agent and the host's own terminal, so stalling here would stall the host.
func (h *Hub) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.screen.Write(p)
	h.send(Message{Data: append([]byte(nil), p...)})
	return len(p), nil
}

// Resize follows the host's terminal and tells the viewer to match.
func (h *Hub) Resize(cols, rows int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.screen.Resize(cols, rows)
	h.send(sizeMessage(cols, rows))
}

// Subscribe replaces any current viewer and returns everything the new one
// needs: the size, a repaint of the screen as it is right now, and the stream of
// what happens next.
//
// The snapshot is taken and the subscription created in one critical section
// with Write. Any gap between them would show up as terminal output the viewer
// either never receives or receives twice, and neither is recoverable — a
// half-applied escape sequence corrupts the screen from then on.
func (h *Hub) Subscribe() (backlog []Message, stream <-chan Message, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.sub != nil {
		h.drop(h.sub)
	}
	s := &subscriber{ch: make(chan Message, viewerBuffer)}
	h.sub = s

	cols, rows := h.screen.Size()
	backlog = []Message{sizeMessage(cols, rows), {Data: h.screen.Snapshot()}}

	return backlog, s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.sub == s {
			h.drop(s)
		}
	}
}

// send queues a message for the viewer, dropping the viewer if it has fallen
// too far behind. Skipping bytes instead is not an option: the viewer is
// rendering an escape-sequence stream, so a gap corrupts it permanently. Being
// disconnected costs the viewer a reconnect and a fresh snapshot, which is
// correct by construction.
//
// Callers must hold h.mu.
func (h *Hub) send(m Message) {
	if h.sub == nil {
		return
	}
	select {
	case h.sub.ch <- m:
	default:
		h.drop(h.sub)
	}
}

// drop disconnects a subscriber. Callers must hold h.mu.
func (h *Hub) drop(s *subscriber) {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	if h.sub == s {
		h.sub = nil
	}
}

func sizeMessage(cols, rows int) Message {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{"size", cols, rows})
	return Message{Control: true, Data: b}
}
