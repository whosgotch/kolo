package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// helloTimeout bounds how long a connection may stay open without saying who it
// is. A socket that opens and then says nothing costs a goroutine and an entry
// in nobody's presence list.
const helloTimeout = 10 * time.Second

// Server is the hub: it answers agents dialling in and questions about who is
// connected.
type Server struct {
	org      *Org
	presence *Presence
	ln       net.Listener
	srv      *http.Server
}

// Listen binds addr and prepares to serve. addr is a host:port; use a host of
// 0.0.0.0 to accept connections from other machines.
func Listen(org *Org, addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hub: listen: %w", err)
	}
	s := &Server{org: org, presence: NewPresence(), ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agent", s.handleAgent)
	mux.HandleFunc("GET /v1/presence", s.handlePresence)
	s.srv = &http.Server{Handler: mux}
	return s, nil
}

// Addr is where the hub is listening, with the port resolved if one was asked
// for by asking for zero.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Presence is who is connected.
func (s *Server) Presence() *Presence { return s.presence }

// Serve runs until Close.
func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops serving and disconnects everyone.
func (s *Server) Close() error { return s.srv.Close() }

// authenticate resolves the bearer token on a request to a member.
//
// The token travels in a header rather than in the URL, because a URL is written
// to the access log of every proxy between the two machines, and to the hub's
// own. A header is not a secret channel, but it is not one that is written down
// by default either.
func (s *Server) authenticate(r *http.Request) (Member, bool) {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return Member{}, false
	}
	return s.org.Verify(token)
}

// handleAgent takes a connection from a member's agent and records it as
// present for as long as it lasts.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	member, ok := s.authenticate(r)
	if !ok {
		// Refused before the upgrade, so an unauthenticated caller never gets a
		// websocket at all, and gets an ordinary status code it can act on.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	greeting, err := readHello(r.Context(), conn)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "expected a hello")
		return
	}

	c := s.presence.Join(member, greeting.Agent, greeting.Machine)
	defer s.presence.Leave(c.ID)

	if err := writeJSON(r.Context(), conn, welcome{
		Type:       "welcome",
		Org:        s.org.Name,
		Member:     member.Person(),
		Connection: c.ID,
	}); err != nil {
		return
	}

	// Nothing else is exchanged yet. The read keeps the connection honest —
	// it ends when the agent goes away — and ignores anything it does not
	// recognise, so an older hub meets a newer client without either breaking.
	for {
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
	}
}

// handlePresence answers who is connected. Members may see each other; a caller
// without a token may not.
func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presenceResponse{
		Org:         s.org.Name,
		Connections: s.presence.List(),
	})
}

// hello is the first thing a client sends. Everything in it describes the
// client's own machine; who the client *is* was settled by the token.
type hello struct {
	Type    string `json:"type"`
	Agent   string `json:"agent"`
	Machine string `json:"machine"`
	Version string `json:"version"`
}

// welcome tells a client who the hub decided it is, which is worth saying out
// loud: it is how a member notices they are connected as somebody else.
type welcome struct {
	Type       string `json:"type"`
	Org        string `json:"org"`
	Member     Person `json:"member"`
	Connection int64  `json:"connection"`
}

type presenceResponse struct {
	Org         string       `json:"org"`
	Connections []Connection `json:"connections"`
}

func readHello(ctx context.Context, conn *websocket.Conn) (hello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()

	kind, data, err := conn.Read(ctx)
	if err != nil {
		return hello{}, err
	}
	if kind != websocket.MessageText {
		return hello{}, fmt.Errorf("hub: hello was not text")
	}
	var h hello
	if err := json.Unmarshal(data, &h); err != nil {
		return hello{}, err
	}
	if h.Type != "hello" {
		return hello{}, fmt.Errorf("hub: first message was %q", h.Type)
	}
	return h, nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}
