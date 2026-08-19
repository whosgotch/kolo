package hub

import (
	"context"
	"sync"
)

// conns is every connection currently open that authenticated as somebody: the
// hosts lending machines, and the browsers watching agents.
//
// Revoking is removing a line from the org file, and a member's next request
// then fails on its own. A connection already open makes no further requests —
// a watcher is handed frames until it goes away — so without this, revoking
// somebody reaches everything except the screen they are already looking at.
type conns struct {
	mu   sync.Mutex
	next int64
	open map[int64]held
}

// held is one open connection and how to end it. It remembers the hash it
// authenticated with rather than the id, so a member whose token was reissued is
// disconnected as surely as one who was removed: what matters is whether the
// secret in their hand is still in the file.
type held struct {
	id     string
	hash   string
	isHost bool
	cancel context.CancelFunc
}

func newConns() *conns { return &conns{open: map[int64]held{}} }

// add records a connection and returns the function that forgets it, to defer.
func (c *conns) add(v held) (done func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	c.open[id] = v
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.open, id)
	}
}

// dropUnknown ends every connection whose credential the org no longer holds,
// and says whose they were. Cancelling only asks the handler to stop; it removes
// its own entry on the way out.
func (c *conns) dropUnknown(o *Org) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var dropped []string
	for _, v := range c.open {
		if v.isHost && o.knowsHost(v.hash) || !v.isHost && o.knowsMember(v.hash) {
			continue
		}
		dropped = append(dropped, v.id)
		v.cancel()
	}
	return dropped
}
