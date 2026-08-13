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
//
// Answering a question and interrupting the agent come through here as well,
// because this is the only thing that writes to the agent and two writers to one
// terminal interleave. They are not queued, though: both mean the screen that is
// up at the moment somebody asks for them, and a screen that has moved on is not
// one anybody may answer or interrupt.
package relay

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/whosgotch/kolo/internal/detect"
)

// esc is the interrupt. It is the key the agent's own footer tells the person at
// the keyboard to press.
const esc = 0x1b

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
	// screen is the agent's screen as text, read fresh each time. The relay takes
	// the picture rather than a verdict about it, because releasing a line needs
	// only the state while answering a question needs the choices in it — and
	// both must be read from the same screen everybody is watching.
	screen func() string

	mu      sync.Mutex
	queue   []Message
	nextID  int
	sending bool
}

// New returns a Relay that writes to agent and reads screen to decide whether it
// may.
func New(agent Sender, screen func() string) *Relay {
	return &Relay{agent: agent, screen: screen}
}

func (r *Relay) state() detect.State { return detect.Of(r.screen()) }

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

// Options are the choices the agent is offering, for showing a member what they
// would be answering. Empty whenever there is no question on screen.
func (r *Relay) Options() []detect.Option { return detect.Options(r.screen()) }

// Answer chooses one of the options on screen, by pressing its number.
//
// The number is checked against the label the member was shown, and both against
// the screen as it stands the instant before the key is written. A dialog that
// has been answered by somebody else, or replaced by the next question, is a
// different question with the same numbers on it — and answering the wrong
// question is the failure this whole package exists to prevent.
//
// Pressing the number rather than walking the highlight down with arrows is the
// safer of the two guesses. Neither was probed, but a key this dialog does not
// understand is discarded (docs/probe-findings.md #5), so a wrong guess here
// costs a member an answer that does not land — where a wrong guess about arrows
// leaves a different option highlighted and Enter still meaning yes.
func (r *Relay) Answer(number int, label string) error {
	// Two digits would be two keystrokes, and the first of them is an answer.
	if number < 1 || number > 9 {
		return fmt.Errorf("relay: %d is not a choice", number)
	}
	return r.exclusive(func() error {
		screen := r.screen()
		if detect.Of(screen) != detect.Dialog {
			return fmt.Errorf("relay: there is no question on screen")
		}
		for _, o := range detect.Options(screen) {
			if o.Number == number && o.Label == label {
				_, err := r.agent.Write([]byte(strconv.Itoa(number)))
				return err
			}
		}
		return fmt.Errorf("relay: the screen has moved on from that question")
	})
}

// Interrupt stops the agent working, and only then: Esc at an input box clears
// what is in it, and Esc at a dialog answers by cancelling. Both are somebody
// else's business, taken by a member who meant to stop something.
func (r *Relay) Interrupt() error {
	return r.exclusive(func() error {
		if r.state() != detect.Busy {
			return fmt.Errorf("relay: the agent is not working")
		}
		_, err := r.agent.Write([]byte{esc})
		return err
	})
}

// exclusive runs one write to the agent with nothing else writing at the same
// time. The lock is not held across the write itself, so a queue that is stuck
// against a full PTY buffer is still one people can add to.
func (r *Relay) exclusive(write func() error) error {
	r.mu.Lock()
	if r.sending {
		r.mu.Unlock()
		return fmt.Errorf("relay: something else is being sent")
	}
	r.sending = true
	r.mu.Unlock()

	err := write()

	r.mu.Lock()
	r.sending = false
	r.mu.Unlock()
	return err
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
