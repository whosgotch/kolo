// Package server is the viewer an agent serves on its own machine, reachable at
// an address only somebody holding its secret can open.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/session"
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

// Server streams a Hub to browsers over a WebSocket.
type Server struct {
	session *session.Session
	guest   Guest
	ln      net.Listener
	srv     *http.Server

	// secret makes the session's address unguessable. It is the whole of the
	// access control: anyone holding the link can watch the agent and send it
	// messages. On loopback that hardly matters, since only this machine can
	// reach it at all; the moment the server listens on a network it is the
	// only thing standing between the agent and everyone else on it.
	secret string
}

// Listen serves the session. Port 0 picks a free one, which URL then reports.
//
// host is the interface to bind: "127.0.0.1" reaches only this machine, and
// "0.0.0.0" reaches anyone who can route to it. The second is a decision, not a
// default, and the caller has to make it deliberately.
//
// guest may be nil, which makes the session watch-only: messages are refused
// rather than queued.
func Listen(sess *session.Session, host string, port int, guest Guest) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, fmt.Errorf("server: listen: %w", err)
	}
	secret, err := newSecret()
	if err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{session: sess, guest: guest, ln: ln, secret: secret}

	mux := http.NewServeMux()
	// The page and the stream sit behind the secret. The assets do not: they
	// are a copy of xterm.js and reveal nothing about the session.
	mux.HandleFunc("GET /s/{secret}", s.handlePage)
	mux.HandleFunc("GET /s/{secret}/ws", s.handleWS)
	mux.Handle("/assets/", noCache(http.FileServerFS(ui.FS)))
	s.srv = &http.Server{Handler: mux}
	return s, nil
}

// newSecret returns an unguessable path segment.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("server: generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// authorised reports whether a request carries the session's secret. The
// comparison is constant-time, so the secret cannot be recovered a character at
// a time by watching how long a refusal takes.
func (s *Server) authorised(r *http.Request) bool {
	got := r.PathValue("secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.secret)) == 1
}

// handlePage serves the viewer.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		// Not "wrong secret": a wrong guess should look exactly like an address
		// that was never a session.
		http.NotFound(w, r)
		return
	}
	page, err := ui.FS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "viewer missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(page)
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

// URL is the address to open, secret included. When the server is listening on
// every interface it reports an address another machine can actually reach,
// rather than 0.0.0.0, which no browser can open.
func (s *Server) URL() string {
	addr := s.ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = localAddress()
	}
	return fmt.Sprintf("http://%s/s/%s", net.JoinHostPort(host, fmt.Sprint(addr.Port)), s.secret)
}

// Base is the server's address without the session's secret, for assets and for
// tests that need to knock on the door without the key.
func (s *Server) Base() string {
	addr := s.ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = localAddress()
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(addr.Port))
}

// Secret is the session's secret, for tests and for anything that needs to
// rebuild the link.
func (s *Server) Secret() string { return s.secret }

// localAddress finds this machine's address on the network it is attached to,
// by asking the routing table where it would send a packet. Nothing is sent.
func localAddress() string {
	conn, err := net.Dial("udp", "203.0.113.1:9")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
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
	if !s.authorised(r) {
		http.NotFound(w, r)
		return
	}
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

	backlog, stream, unsubscribe := s.session.Subscribe()
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
			s.session.Announce(problem{"error", "message not understood"})
			continue
		}
		if s.guest == nil {
			s.session.Announce(problem{"error", "this session is watch-only"})
			continue
		}
		if err := s.guest(msg.Nickname, msg.Text); err != nil {
			s.session.Announce(problem{"error", err.Error()})
		}
	}
}

type problem struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// send writes one message: control frames as text, terminal output as binary,
// so the viewer can tell them apart without inspecting the payload.
func send(ctx context.Context, conn *websocket.Conn, m session.Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	return conn.Write(ctx, kind, m.Data)
}
