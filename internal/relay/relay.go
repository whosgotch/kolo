// Package relay is the sole writer to an agent's PTY: interleaved writers
// garble a terminal.
//
// See docs/reference.md, "Input model".
package relay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

// maxKeys bounds one message, sized for a paste rather than a keystroke.
const maxKeys = 64 << 10

// ErrTooMuch is a message past maxKeys.
var ErrTooMuch = errors.New("relay: more than one paste at a time")

type Sender interface {
	Write(p []byte) (int, error)
}

type Relay struct {
	agent Sender
	kind  adapter.Adapter
	// Read fresh on every call: gating decides from the screen now.
	screen func() (string, time.Duration)

	mu sync.Mutex
}

func New(agent Sender, screen func() (string, time.Duration), kind adapter.Adapter) *Relay {
	return &Relay{agent: agent, kind: kind, screen: screen}
}

func (r *Relay) state() detect.State { return r.kind.Markers.OfSettled(r.screen()) }

// Type sends keystrokes to the agent ungated; the member is looking at the
// screen they land on.
func (r *Relay) Type(keys string) error {
	if keys == "" {
		return nil
	}
	if len(keys) > maxKeys {
		return fmt.Errorf("%w: %d bytes at once, and %d is the most that goes through in one message",
			ErrTooMuch, len(keys), maxKeys)
	}
	return r.exclusive(func() error {
		_, err := r.agent.Write([]byte(keys))
		return err
	})
}

// Interrupt stops the agent, but only while it's busy: the same key means
// something else at an input box or a dialog.
func (r *Relay) Interrupt() error {
	return r.exclusive(func() error {
		if r.state() != detect.Busy {
			return fmt.Errorf("relay: the agent is not working")
		}
		_, err := r.agent.Write(r.kind.InterruptKey())
		return err
	})
}

// exclusive serialises writes to the agent. A second member waits for the
// current paste or keystroke instead of losing input because both typed at once.
func (r *Relay) exclusive(write func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return write()
}
