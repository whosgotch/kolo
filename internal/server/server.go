package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// writeTimeout bounds a single send to the viewer, so a connection that has
// stopped acknowledging cannot hold its goroutine forever.
const writeTimeout = 10 * time.Second

// Server streams a Hub over a WebSocket on the loopback interface.
type Server struct {
	hub *Hub
	ln  net.Listener
	srv *http.Server
}

// Listen binds to port on localhost. Port 0 picks a free one, which URL then
// reports.
func Listen(hub *Hub, port int) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("server: listen: %w", err)
	}
	s := &Server{hub: hub, ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	s.srv = &http.Server{Handler: mux}
	return s, nil
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

	// The viewer is read-only in this milestone, and CloseRead enforces it:
	// protocol frames keep being handled, but anything the browser tries to
	// send is a protocol error that ends the connection. It also gives a
	// context that is cancelled as soon as the viewer goes away.
	ctx := conn.CloseRead(r.Context())

	backlog, stream, cancel := s.hub.Subscribe()
	defer cancel()

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
