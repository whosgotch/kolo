package hub

import (
	"context"
	"sync"
)

// conns tracks the open authenticated connections, so revocation can reach
// them even though they make no further request.
type conns struct {
	mu   sync.Mutex
	next int64
	open map[int64]held
}

// held remembers the token hash, not the id: a reissued token still revokes.
type held struct {
	id     string
	hash   string
	isHost bool
	cancel context.CancelFunc
}

func newConns() *conns { return &conns{open: map[int64]held{}} }

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
