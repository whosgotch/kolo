// Package relay holds guests' messages and gives them to the agent only when
// the agent is ready for one.
//
// Two rules come from Milestone 0 and neither is negotiable:
//
// A line is only ever released while the screen says the agent is idle at its
// input box (internal/detect). Released at the wrong moment it is either
// swallowed without trace or, worse, its Enter answers a question the agent was
// asking the host — see docs/probe-findings.md #4 and #5.
//
// The text and the Enter go as two separate writes. Sent as one, the agent's
// terminal reads them as a paste, the Enter becomes a literal newline, and the
// line is never submitted at all (findings #3).
package relay

import (
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/whosgotch/kolo/internal/detect"
)

const (
	// maxText is a generous limit on one message. It exists so a guest cannot
	// paste a novel into the host's agent, not to shape what people say.
	maxText = 2000
	// maxNickname keeps the prefix from crowding out the message.
	maxNickname = 32
)

// Sender is the agent's input. It is an interface so the queue can be tested
// without an agent attached to it.
type Sender interface {
	Write(p []byte) (int, error)
}

// Message is one guest's line.
type Message struct {
	ID       int
	Nickname string
	Text     string
}

// Line is what actually reaches the agent: the guest's words, attributed.
func (m Message) Line() string { return m.Nickname + ": " + m.Text }

// Relay is the queue between the guests and the agent.
type Relay struct {
	agent Sender
	state func() detect.State

	mu      sync.Mutex
	queue   []Message
	nextID  int
	sending bool
}

// New returns a Relay that writes to agent and asks state whether it may.
func New(agent Sender, state func() detect.State) *Relay {
	return &Relay{agent: agent, state: state}
}

// Submit queues a guest's line. It is never written to the agent here, however
// idle the agent happens to be: everything goes through the queue, so there is
// one path to the agent and one place that decides when it opens.
func (r *Relay) Submit(nickname, text string) (Message, error) {
	nickname, text = clean(nickname, maxNickname), clean(text, maxText)
	if nickname == "" {
		return Message{}, fmt.Errorf("relay: message needs a nickname")
	}
	if text == "" {
		return Message{}, fmt.Errorf("relay: message is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	m := Message{ID: r.nextID, Nickname: nickname, Text: text}
	r.queue = append(r.queue, m)
	return m, nil
}

// Pending returns the messages still waiting, oldest first.
func (r *Relay) Pending() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.queue...)
}

// Tick releases at most one message, and only if the agent is idle. It reports
// what it sent, or nothing if the queue is empty or the agent is not ready.
//
// One message per tick is deliberate. Submitting a line stops the agent being
// idle, so the second line of a queue must wait for the screen to say so again
// rather than be written on the strength of a reading taken before the first
// was sent.
func (r *Relay) Tick() (*Message, error) {
	r.mu.Lock()
	if r.sending || len(r.queue) == 0 || !r.state().CanSend() {
		r.mu.Unlock()
		return nil, nil
	}
	m := r.queue[0]
	r.sending = true
	r.mu.Unlock()

	err := r.send(m)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sending = false
	if err != nil {
		// The message stays at the head of the queue for the next tick. If the
		// text landed but the Enter did not, it is sitting in the agent's input
		// box, and resending would double it — but a failed write to the PTY
		// means the agent is going away, so there is no next tick to worry
		// about in practice.
		return nil, err
	}
	r.queue = r.queue[1:]
	return &m, nil
}

// send writes the line and then the Enter, as two separate writes. See the
// package comment: bundled, they are read as a paste and nothing is submitted.
func (r *Relay) send(m Message) error {
	if _, err := r.agent.Write([]byte(m.Line())); err != nil {
		return fmt.Errorf("relay: write line: %w", err)
	}
	if _, err := r.agent.Write([]byte("\r")); err != nil {
		return fmt.Errorf("relay: write enter: %w", err)
	}
	return nil
}

// clean reduces a guest's input to text, which is the whole of what a guest is
// allowed to send.
//
// Every control character goes. A guest is typing into a terminal attached to
// the host's machine, so an escape sequence is not text — it is a keystroke:
// arrows that move through a dialog, or the Enter that answers one. Newlines
// and tabs become spaces instead of being dropped, so pasted text stays
// readable rather than running together.
//
// Format characters go too. They are invisible by definition, and among them
// are the bidirectional overrides, which can make a line render as something
// other than what it says.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	for len(out) > max {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}
