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

func serve(t *testing.T, hub *Hub, guest Guest) *Server {
	t.Helper()
	srv, err := Listen(hub, "127.0.0.1", 0, guest)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return srv
}

func dial(t *testing.T, ctx context.Context, srv *Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL(), "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func TestServeCatchesAViewerUp(t *testing.T) {
	hub := NewHub(80, 24)
	hub.Write([]byte("already on screen"))
	srv := serve(t, hub, nil)

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

// TestGuestMessageReachesTheQueue checks the one thing a viewer may send. The
// handler stands in for the queue, and the point is what it receives: words and
// a name, with no way to express a keystroke.
func TestGuestMessageReachesTheQueue(t *testing.T) {
	type got struct{ nickname, text string }
	received := make(chan got, 1)
	srv := serve(t, NewHub(80, 24), func(nickname, text string) error {
		received <- got{nickname, text}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dial(t, ctx, srv)

	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"message","nickname":"ada","text":"what does this do?"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-received:
		if m.nickname != "ada" || m.text != "what does this do?" {
			t.Errorf("handler got %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message never reached the handler")
	}
}

// TestBinaryFromAViewerIsIgnored keeps the one asymmetry that matters: binary
// is the agent's language. Bytes from a browser are never passed through.
func TestBinaryFromAViewerIsIgnored(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := serve(t, NewHub(80, 24), func(string, string) error {
		called <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dial(t, ctx, srv)

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("\x1b[B\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Error("binary from a viewer was handed on as a message")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestWatchOnlySessionRefusesMessages covers a server built without a queue:
// the message is refused and the viewer is told, rather than silently dropped.
func TestWatchOnlySessionRefusesMessages(t *testing.T) {
	srv := serve(t, NewHub(80, 24), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dial(t, ctx, srv)

	if _, _, err := conn.Read(ctx); err != nil { // size frame
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err != nil { // snapshot
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"message","nickname":"a","text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "watch-only") {
		t.Errorf("reply = %q, want it to say the session is watch-only", data)
	}
}

func TestServesTheViewerPage(t *testing.T) {
	srv := serve(t, NewHub(80, 24), nil)

	for _, path := range []string{srv.URL(), srv.Base() + "/assets/xterm.js", srv.Base() + "/assets/xterm.css"} {
		resp, err := http.Get(path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s served nothing", path)
		}
	}
}

// TestTheSecretIsTheDoor is the whole of the access control once the server
// listens on a network: a link without the right secret must look exactly like
// an address that was never a session.
func TestTheSecretIsTheDoor(t *testing.T) {
	srv := serve(t, NewHub(80, 24), nil)
	base := srv.Base()

	for name, path := range map[string]string{
		"no secret":      base + "/s/",
		"wrong secret":   base + "/s/" + strings.Repeat("a", 43),
		"empty path":     base + "/",
		"guessed prefix": base + "/s/" + srv.Secret()[:10],
		"secret plus":    srv.URL() + "x",
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Get(path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
			}
		})
	}
}

// TestTheStreamNeedsTheSecretToo keeps the door from being walked around: the
// page is not the only way in.
func TestTheStreamNeedsTheSecretToo(t *testing.T) {
	srv := serve(t, NewHub(80, 24), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wrong := "ws" + strings.TrimPrefix(srv.Base(), "http") + "/s/" + strings.Repeat("a", 43) + "/ws"
	conn, resp, err := websocket.Dial(ctx, wrong, nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("a stream was served without the secret")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %v, want 404", resp)
	}
}
