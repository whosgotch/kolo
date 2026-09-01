package session

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

const writeTimeout = 10 * time.Second

// Send writes one message: control frames as text, terminal output as binary.
func Send(ctx context.Context, conn *websocket.Conn, m Message) error {
	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, kind, m.Data)
}

// Keepalive pings conn every `every`, and gives up on a peer that has not
// answered within `within`. It returns when either happens, or when ctx ends. Without it a peer that dropped off the network is
// noticed only when TCP gives up, which is minutes rather than seconds: long
// enough that a host coming back is refused as one already connected, and
// long enough that a browser nobody is looking at keeps holding the agent's
// terminal down to the size of a window that has gone.
//
// It needs somebody else reading the connection, since that is what delivers
// the pong. Every caller here is in a read loop already.
func Keepalive(ctx context.Context, conn *websocket.Conn, every, within time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		waited, cancel := context.WithTimeout(ctx, within)
		err := conn.Ping(waited)
		cancel()
		if err != nil {
			return
		}
	}
}
