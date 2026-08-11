package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func serve(t *testing.T, hub *Hub) *Server {
	t.Helper()
	srv, err := Listen(hub, 0)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return srv
}

func dial(t *testing.T, ctx context.Context, srv *Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL(), "http")+"ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func TestServeCatchesAViewerUp(t *testing.T) {
	hub := NewHub(80, 24)
	hub.Write([]byte("already on screen"))
	srv := serve(t, hub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dial(t, ctx, srv)

	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText || !strings.Contains(string(data), `"type":"size"`) {
		t.Errorf("first frame = %v %q, want a size control frame", kind, data)
	}

	if kind, data, err = conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageBinary || !strings.Contains(string(data), "already on screen") {
		t.Errorf("second frame = %v, want a snapshot carrying the screen", kind)
	}

	hub.Write([]byte("and then this"))
	if kind, data, err = conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageBinary || string(data) != "and then this" {
		t.Errorf("live frame = %v %q", kind, data)
	}
}

// TestViewerCannotSend pins the read-only guarantee at the transport: there is
// no path from the browser to the agent in this milestone, and the connection
// ends rather than quietly ignoring the attempt.
func TestViewerCannotSend(t *testing.T) {
	srv := serve(t, NewHub(80, 24))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dial(t, ctx, srv)

	if _, _, err := conn.Read(ctx); err != nil { // size frame
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("let me in")); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return // the server hung up, as it should
		}
	}
}

func TestServesTheViewerPage(t *testing.T) {
	srv := serve(t, NewHub(80, 24))

	for _, path := range []string{"", "assets/xterm.js", "assets/xterm.css"} {
		resp, err := http.Get(srv.URL() + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /%s = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET /%s served nothing", path)
		}
	}
}
