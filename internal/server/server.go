package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/ui"
)

// writeTimeout bounds a single send to the viewer, so a connection that has
// stopped acknowledging cannot hold its goroutine forever.
const writeTimeout = 10 * time.Second

// Guest receives a message a viewer sent. It returns an error if the message
// cannot be accepted, which the viewer is told about.
//
// What it must not do is write to the agent. The handler hands the message to
// the queue, and the queue releases it later, if and when the agent's screen
// says a line may be sent (internal/relay).
type Guest func(nickname, text string) error

// Server streams a Hub over a WebSocket on the loopback interface.
type Server struct {
	hub   *Hub
	guest Guest
	ln    net.Listener
	srv   *http.Server
}

// Listen binds to port on localhost. Port 0 picks a free one, which URL then
// reports.
//
// guest may be nil, which makes the session watch-only: messages are refused
// rather than queued.
func Listen(hub *Hub, port int, guest Guest) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("server: listen: %w", err)
	}
	s := &Server{hub: hub, guest: guest, ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.Handle("/", noCache(http.FileServerFS(ui.FS)))
	s.srv = &http.Server{Handler: mux}
	return s, nil
}

// noCache keeps a browser from showing a page baked into an older binary. The
// viewer is compiled in, so it changes when kolo does, and a cached copy of it
// is always the wrong one.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// URL is where the viewer connects.
func (s *Server) URL() string {
	return "http://" + s.ln.Addr().String() + "/"
}

// Serve runs until Close. It is meant to be called in its own goroutine.
func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops serving and disconnects the viewer.
func (s *Server) Close() error { return s.srv.Close() }

// handleWS sends the new viewer its catch-up and then everything that follows.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reading and writing run separately: a viewer that says nothing for an
	// hour must keep receiving, and a viewer that talks must not have to wait
	// its turn behind the terminal stream.
	go s.read(ctx, cancel, conn)

	backlog, stream, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	for _, m := range backlog {
		if send(ctx, conn, m) != nil {
			return
		}
	}
	for m := range stream {
		if send(ctx, conn, m) != nil {
			return
		}
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// inbound is what a viewer may send. There is exactly one kind, and it carries
// no way to express a keystroke: a guest sends words, and the queue decides
// when, or whether, they reach the agent.
type inbound struct {
	Type     string `json:"type"`
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
}

// read takes messages from the viewer until it goes away, which cancels the
// write side too.
func (s *Server) read(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Binary is the agent's language, not a guest's. Nothing a viewer sends
		// is ever passed through as bytes.
		if kind != websocket.MessageText {
			continue
		}

		var msg inbound
		if err := json.Unmarshal(data, &msg); err != nil || msg.Type != "message" {
			s.hub.Announce(problem{"error", "message not understood"})
			continue
		}
		if s.guest == nil {
			s.hub.Announce(problem{"error", "this session is watch-only"})
			continue
		}
		if err := s.guest(msg.Nickname, msg.Text); err != nil {
			s.hub.Announce(problem{"error", err.Error()})
		}
	}
}

type problem struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// send writes one message: control frames as text, terminal output as binary,
// so the viewer can tell them apart without inspecting the payload.
func send(ctx context.Context, conn *websocket.Conn, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	return conn.Write(ctx, kind, m.Data)
}
