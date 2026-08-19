package session

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// A viewer that has stopped acknowledging cannot hold up the screen it is
// watching.
const writeTimeout = 10 * time.Second

// Send puts one message on the wire: terminal output as binary, anything kolo
// says about it as text, so neither has to be told apart by looking inside.
//
// The host sends to the hub and the hub sends to the browser, and both are
// carrying the same messages to something that subscribed the same way.
func Send(ctx context.Context, conn *websocket.Conn, m Message) error {
	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, kind, m.Data)
}
