// Package relay is the only thing that writes to an agent: two writers to one
// terminal interleave, so every write goes through here.
//
// Keystrokes come from the member holding the keyboard. Interrupting is the one
// thing kolo does on somebody's behalf, and it means the screen that is up at the
// moment they ask.
//
// See docs/architecture.md "Input".
package relay

import (
	"fmt"
	"sync"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

// One press worth of keys: an escape sequence and a fast typist's backlog fit,
// a file streamed in one frame at a time does not.
const maxKeys = 256

// Sender is the agent's input, an interface so writing can be tested without an
// agent attached to it.
type Sender interface {
	Write(p []byte) (int, error)
}

type Relay struct {
	agent Sender
	kind  adapter.Adapter
	// Read fresh each time, and the picture rather than a verdict about it: what
	// may be sent is decided from the screen as it stands at the moment of asking,
	// not from a reading taken earlier.
	screen func() (string, time.Duration)

	mu      sync.Mutex
	sending bool
}

func New(agent Sender, screen func() (string, time.Duration), kind adapter.Adapter) *Relay {
	return &Relay{agent: agent, kind: kind, screen: screen}
}

func (r *Relay) state() detect.State { return r.kind.Markers.OfSettled(r.screen()) }

// Type sends a member's keystrokes to the agent as they press them. Ungated:
// the member is looking at the screen those keys land on. Through the same lock
// as everything else, so they cannot land inside the interrupt somebody else
// just pressed.
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

// Interrupt stops the agent working, and only then: the key that means stop
// while it is working means something else while it is not — Esc at an input box
// clears what is in it, and at a dialog answers by cancelling.
//
// Which key it is belongs to the agent kind. It was Esc for everything until
// kinds kolo does not ship could be described, and an agent that stops on Ctrl-C
// was being sent a key that clears its input instead.
func (r *Relay) Interrupt() error {
	return r.exclusive(func() error {
		if r.state() != detect.Busy {
			return fmt.Errorf("relay: the agent is not working")
		}
		_, err := r.agent.Write(r.kind.InterruptKey())
		return err
	})
}

// exclusive runs one write to the agent with nothing else writing at the same
// time. The lock is not held across the write, so one stuck against a full PTY
// buffer does not hold up the reads that decide what may be written next.
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
