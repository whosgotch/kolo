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
