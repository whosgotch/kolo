// Package relay holds guests' messages and gives them to the agent only when
// the agent is ready for one.
//
// Two rules, neither negotiable: a line is released only while internal/detect
// says the agent is idle, and the text and the Enter go as two separate writes.
// See docs/architecture.md "Input" and docs/probe-findings.md #3, #4 and #5.
//
// Answering and interrupting come through here too, because this is the only
// thing that writes to the agent and two writers to one terminal interleave.
// They are not queued: both mean the screen that is up at the moment somebody
// asks for them.
package relay

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

// The key the agent's own footer tells the person at the keyboard to press.
const esc = 0x1b

// How long to wait between the line and the Enter that submits it.
//
// Two separate writes are not enough on their own (probe-findings #3, #8): an
// Enter that arrives hard behind the text is still inside the agent's
// paste-detection window, where it is read as a newline in the pasted content
// and the line sits in the box unsent. The recorder that proved the two-write
// rule had this pause in it all along; the product did not, and a message going
// nowhere was the intermittent result.
//
// A variable so the tests do not have to wait it out.
var enterDelay = 150 * time.Millisecond

const (
	maxText     = 2000
	maxNickname = 32
	// A keystroke, or a burst of them from one press. Big enough for an escape
	// sequence and a fast typist's backlog, small enough that nobody streams a
	// file into the agent one frame at a time.
	maxKeys = 256
)

// Sender is the agent's input, an interface so the queue can be tested without
// an agent attached to it.
type Sender interface {
	Write(p []byte) (int, error)
}

// Message is one guest's line.
type Message struct {
	ID       int
	Nickname string
	Text     string
	// Command marks a line the agent's own CLI reads as an instruction to
	// itself. It reaches the agent unattributed, so whoever sent it is carried
	// by the event watchers are told about instead.
	Command bool
}

// isCommand reports whether text is for the CLI rather than the conversation.
//
// Only the first character decides. A line mentioning /tmp halfway through is
// prose, and prose is attributed. A kind that declares no sigils has no CLI kolo
// can reach, so every line of its is a message.
func (r *Relay) isCommand(text string) bool {
	sigils := r.kind.Sigils
	return text != "" && sigils != "" && strings.IndexByte(sigils, text[0]) >= 0
}

// Line is what actually reaches the agent: the member's words, and nothing kolo
// added to them.
//
// They used to arrive as "Dana: do the thing". It read as kolo talking through a
// member rather than a member talking, it put a convention the agent knows
// nothing about into its context, and it never held anyway — a command had to be
// exempt, because a name in front of the sigil puts it out of the first column
// and the CLI reads the whole line as prose.
//
// Who sent what is kolo's to remember, not the agent's transcript's: it goes to
// every watcher as an event, where it can be shown without being fed to a model.
func (m Message) Line() string { return m.Text }

// Relay is the queue between the guests and the agent.
type Relay struct {
	agent Sender
	// What kolo knows about this agent's kind: how to read its screen, and which
	// lines are for its CLI.
	kind adapter.Adapter
	// The screen as text, and how long it has been that same picture, read fresh
	// each time — the picture rather than a verdict about it, because releasing a
	// line needs only the state while answering needs the choices, and both must
	// come from the same screen. The stillness comes with it because for some
	// kinds it is half the state.
	screen func() (string, time.Duration)

	mu      sync.Mutex
	queue   []Message
	nextID  int
	sending bool
}

// New returns a Relay that writes to agent and reads screen, by the markers of
// kind, to decide whether it may.
func New(agent Sender, screen func() (string, time.Duration), kind adapter.Adapter) *Relay {
	return &Relay{agent: agent, kind: kind, screen: screen}
}

func (r *Relay) state() detect.State { return r.kind.Markers.OfSettled(r.screen()) }

// Submit queues a guest's line. It is never written to the agent here, however
// idle the agent happens to be: everything goes through the queue.
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
	m := Message{ID: r.nextID, Nickname: nickname, Text: text, Command: r.isCommand(text)}
	r.queue = append(r.queue, m)
	return m, nil
}

// Pending returns the messages still waiting, oldest first.
func (r *Relay) Pending() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.queue...)
}

// Tick releases at most one message, and only if the agent is idle.
//
// One per tick is deliberate: submitting a line stops the agent being idle, so
// the next must wait for the screen to say so again rather than go out on a
// reading taken before the first was sent.
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
		// Stays at the head of the queue. If the text landed but the Enter did
		// not, resending would double it — but a failed PTY write means the agent
		// is going away, so in practice there is no next tick.
		return nil, err
	}
	r.queue = r.queue[1:]
	return &m, nil
}

// Options are the choices the agent is offering, for showing a member what they
// would be answering. Empty whenever there is no question on screen.
func (r *Relay) Options() []detect.Option {
	screen, _ := r.screen()
	return r.kind.Markers.Options(screen)
}

// Answer chooses one of the options on screen, by pressing its number.
//
// The number is checked against the label the member was shown, and both against
// the screen the instant before the key is written: a dialog replaced by the next
// question is a different question with the same numbers on it.
//
// Pressing the number rather than walking the highlight with arrows is the safer
// guess. A key the dialog does not understand is discarded (probe-findings #5),
// so a wrong guess costs an answer that does not land — where a wrong guess about
// arrows leaves a different option highlighted and Enter still meaning yes.
func (r *Relay) Answer(number int, label string) error {
	// Two digits would be two keystrokes, and the first of them is an answer.
	if number < 1 || number > 9 {
		return fmt.Errorf("relay: %d is not a choice", number)
	}
	return r.exclusive(func() error {
		screen, _ := r.screen()
		if r.kind.Markers.Of(screen) != detect.Dialog {
			return fmt.Errorf("relay: there is no question on screen")
		}
		for _, o := range r.kind.Markers.Options(screen) {
			if o.Number == number && o.Label == label {
				_, err := r.agent.Write([]byte(strconv.Itoa(number)))
				return err
			}
		}
		return fmt.Errorf("relay: the screen has moved on from that question")
	})
}

// Type sends a member's keystrokes to the agent as they press them.
//
// Ungated, and that is the point: the member is looking at the screen those keys
// land on, so a key at a question is a decision they made rather than a line
// kolo chose to type at a moment it guessed. What the gate protects — a queued
// line going out under a dialog — is a different thing and still protected.
//
// Through the same lock as everything else, so keystrokes cannot land inside a
// queued line that is halfway written.
func (r *Relay) Type(keys string) error {
	if keys == "" {
		return nil
	}
	if len(keys) > maxKeys {
		return fmt.Errorf("relay: %d bytes is not a keystroke", len(keys))
	}
	return r.exclusive(func() error {
		_, err := r.agent.Write([]byte(keys))
		return err
	})
}

// Dismiss closes what the agent is showing, by pressing the Esc its own footer
// offers — and only on a question kolo could not read choices off.
//
// A slash command that opens a panel (/status, /config) leaves a screen carrying
// the dialog footer and no numbered options: nothing to offer as buttons, a
// queue held because a line would be swallowed, and no way back to the input box
// short of restarting an agent the whole org is using.
//
// Refused where there are choices, because there Esc is not "close this" but an
// answer — the one nobody chose. Those screens are answered by their buttons.
func (r *Relay) Dismiss() error {
	return r.exclusive(func() error {
		screen, _ := r.screen()
		if r.kind.Markers.Of(screen) != detect.Dialog {
			return fmt.Errorf("relay: there is nothing to close")
		}
		if len(r.kind.Markers.Options(screen)) > 0 {
			return fmt.Errorf("relay: that is a question — answer it")
		}
		_, err := r.agent.Write([]byte{esc})
		return err
	})
}

// Interrupt stops the agent working, and only then: Esc at an input box clears
// what is in it, and Esc at a dialog answers by cancelling.
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
// time. The lock is not held across the write, so a queue stuck against a full
// PTY buffer is still one people can add to.
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

// send writes the line, waits, and then writes the Enter. See the package
// comment and enterDelay: bundled or hard behind each other, they are read as a
// paste and nothing is submitted.
func (r *Relay) send(m Message) error {
	if _, err := r.agent.Write([]byte(m.Line())); err != nil {
		return fmt.Errorf("relay: write line: %w", err)
	}
	time.Sleep(enterDelay)
	if _, err := r.agent.Write([]byte("\r")); err != nil {
		return fmt.Errorf("relay: write enter: %w", err)
	}
	return nil
}

// clean reduces a guest's input to text, which is the whole of what a guest is
// allowed to send.
//
// Control characters go: in a terminal an escape sequence is not text, it is a
// keystroke — arrows through a dialog, or the Enter that answers one. Format
// characters go too, bidirectional overrides among them, which can make a line
// render as something other than what it says.
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
