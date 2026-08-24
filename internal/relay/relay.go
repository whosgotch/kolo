// Package relay is the sole writer to an agent's PTY: interleaved writers
// garble a terminal.
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

// maxKeys is one press worth: an escape sequence or a fast typist's backlog,
// not a pasted file.
const maxKeys = 256

type Sender interface {
	Write(p []byte) (int, error)
}

type Relay struct {
	agent Sender
	kind  adapter.Adapter
	// Read fresh on every call: gating decides from the screen now, not an
	// earlier reading.
	screen func() (string, time.Duration)

	mu      sync.Mutex
	sending bool
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
		return fmt.Errorf("relay: %d bytes is not a keystroke", len(keys))
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

// exclusive serialises writes to the agent. Not held across the write itself,
// so a write stuck on a full PTY buffer doesn't block the next read.
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
