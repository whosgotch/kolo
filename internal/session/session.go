// Package session holds one agent's screen and the viewers watching it.
//
// Used twice over: by the viewer an agent serves on its own machine, and by the
// hub, which holds one per agent. Both need a screen kept current, a way to catch
// somebody up to it, and a fan-out of what happens next.
package session

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/term"
)

// How far a viewer may fall behind, in messages, before it is dropped rather
// than slowed down.
const viewerBuffer = 256

// Message is one thing to send to the viewer: either a control frame or a chunk
// of the agent's raw terminal output.
type Message struct {
	Control bool
	Data    []byte
}

// Session owns the virtual terminal and fans the agent's output out to viewers.
// Every viewer gets its own subscription: guests watch together, and a page that
// reconnects on its own would otherwise take the screen from itself.
type Session struct {
	// How the agent's kind wears its state, so that what the session says about
	// the screen is read with the markers of the agent drawing it.
	markers detect.Markers

	mu     sync.Mutex
	screen *term.Screen
	subs   map[*subscriber]struct{}
	// The screen as it last looked, and when it started looking that way. Kept
	// only for a kind whose idle is silence, because keeping it costs a copy of
	// the screen per write and the kinds that announce themselves do not need it.
	settled   string
	settledAt time.Time
}

type subscriber struct {
	ch     chan Message
	closed bool
}

func New(cols, rows int, markers detect.Markers) *Session {
	return &Session{
		markers: markers,
		screen:  term.New(cols, rows),
		subs:    map[*subscriber]struct{}{},
		// From now rather than the zero time, so an agent that has not drawn
		// anything yet has not been still since the beginning of time.
		settledAt: time.Now(),
	}
}

// Markers are what this session reads its screen with. The host sends them on
// with the screen itself, so the hub reads that screen the same way rather than
// keeping a table of its own to look the agent up in.
func (s *Session) Markers() detect.Markers { return s.markers }

func (s *Session) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Write feeds agent output into the virtual terminal and on to the viewers. It
// never fails and never blocks on a viewer: this sits between the agent and the
// host's own terminal, so stalling here would stall the host.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.screen.Write(p)
	s.noteChange()
	s.send(Message{Data: append([]byte(nil), p...)})
	return len(p), nil
}

// State reads the agent's screen and reports whether it could take a line now —
// the same screen the viewer sees, not a second one kept alongside.
func (s *Session) State() detect.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markers.OfSettled(s.screen.Text(), time.Since(s.settledAt))
}

// Screen is the agent's screen and how long it has been that same picture, read
// together because a kind whose idle is silence needs both to be true at once.
func (s *Session) Screen() (text string, still time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Text(), time.Since(s.settledAt)
}

// noteChange records when the screen last became what it is now. Bytes arriving
// are not a change: a repaint that redraws the same picture, or output that
// scrolls off, leaves the screen where it was. Callers must hold s.mu.
func (s *Session) noteChange() {
	if s.markers.Settle == 0 {
		return
	}
	if text := s.screen.Text(); text != s.settled {
		s.settled, s.settledAt = text, time.Now()
	}
}

// Text is the agent's screen as it stands. It is what the relay reads before
// writing anything: the state a key may be sent in.
func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Text()
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

// Subscribe adds a viewer and returns what it needs to start: the size, a repaint
// of the screen as it stands, and the stream of what happens next.
//
// The snapshot and the subscription are made in one critical section with Write.
// A gap between them means output received twice or not at all, and neither is
// recoverable — a half-applied escape sequence corrupts the screen from then on.
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

// Close ends the session and disconnects everyone watching: the agent behind the
// screen has gone or been replaced, so there is nothing to catch anyone up to.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		s.drop(sub)
	}
}

// send queues a message for every viewer, dropping any that has fallen too far
// behind. Skipping bytes is not an option: a viewer renders an escape-sequence
// stream, so a gap corrupts it permanently, where a drop costs one reconnect.
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
