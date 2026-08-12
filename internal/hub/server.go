package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// helloTimeout bounds how long a connection may stay open without saying who
	// it is.
	helloTimeout = 10 * time.Second

	// writeTimeout bounds one send to a host, so a machine that has stopped
	// acknowledging cannot hold up whoever is waiting on it.
	writeTimeout = 10 * time.Second
)

// Server is the hub: the org, the hosts lending themselves to it, and the agents
// running on them.
type Server struct {
	org      *Org
	registry *Registry
	ln       net.Listener
	srv      *http.Server

	// ctx is cancelled by Close, which is the only thing that reaches a host
	// connection. net/http does not: a websocket has been hijacked out of the
	// server's hands. Reads take this context so that shutting the hub down
	// disconnects hosts rather than leaving them blocked on a socket to a hub
	// that is gone.
	ctx    context.Context
	cancel context.CancelFunc
}

// Listen binds addr, a host:port. Use a host of 0.0.0.0 to accept connections
// from other machines.
func Listen(org *Org, addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hub: listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{org: org, registry: NewRegistry(), ln: ln, ctx: ctx, cancel: cancel}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", s.handleHost)
	mux.HandleFunc("GET /v1/agents", s.handleList)
	mux.HandleFunc("POST /v1/agents", s.handleCreate)
	mux.HandleFunc("DELETE /v1/agents/{name}", s.handleDelete)
	s.srv = &http.Server{Handler: mux}
	return s, nil
}

// Addr is where the hub is listening, with the port resolved if one was asked
// for by asking for zero.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Registry is every connected host and the agents on them.
func (s *Server) Registry() *Registry { return s.registry }

func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops serving and disconnects every host.
func (s *Server) Close() error {
	s.cancel()
	return s.srv.Close()
}

// authenticate resolves the bearer token on a request to a member.
//
// The token travels in a header rather than in the URL, because a URL is written
// to the access log of every proxy between the two machines, and to the hub's own.
func (s *Server) authenticate(r *http.Request) (Member, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Member{}, false
	}
	return s.org.VerifyMember(token)
}

func (s *Server) authenticateHost(r *http.Request) (Host, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Host{}, false
	}
	return s.org.VerifyHost(token)
}

// handleHost takes a machine lending itself to the org and keeps it connected.
func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	h, ok := s.authenticateHost(r)
	if !ok {
		// Refused before the upgrade, so an unauthenticated caller never gets a
		// websocket at all and gets a status code it can act on.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	hello, err := read[hostHello](ctx, conn, helloTimeout)
	if err != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusPolicyViolation, "expected a hello")
		return
	}

	if err := s.registry.Join(h.ID, hello.Dirs, hello.Allow, sender(s.ctx, conn)); err != nil {
		conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	defer s.registry.Leave(h.ID)

	if err := write(ctx, conn, hostWelcome{Type: "welcome", Org: s.org.Name, Host: h.ID}); err != nil {
		return
	}

	for {
		report, err := read[agentReport](ctx, conn, 0)
		if err != nil {
			return
		}
		// Anything unrecognised is ignored, so a newer host can talk to an older
		// hub without either breaking.
		if report.Type == "status" {
			s.registry.SetStatus(report.Name, report.Status, label(report.Error))
		}
	}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{
		Org:    s.org.Name,
		Hosts:  s.registry.Hosts(),
		Agents: s.registry.Agents(),
	})
}

// handleCreate asks a host to run an agent. Every member may; there are no roles,
// and the record of who asked is what stands in for them.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	member, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	if !ValidName(req.Name) {
		http.Error(w, "a name is 1-32 characters of a-z, 0-9 and dashes", http.StatusBadRequest)
		return
	}

	agent := Agent{
		Name:      req.Name,
		Host:      req.Host,
		Dir:       req.Dir,
		Command:   req.Command,
		CreatedBy: member.Person(),
	}
	send, err := s.registry.Add(agent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := send(spawn{Type: "spawn", Name: agent.Name, Dir: agent.Dir, Command: agent.Command}); err != nil {
		// The host went away between being chosen and being asked. Leaving the
		// agent listed would be listing something nobody ever started.
		s.registry.Remove(agent.Name)
		http.Error(w, "the host went away", http.StatusServiceUnavailable)
		return
	}
	created, _ := s.registry.Agent(agent.Name)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	send, ok := s.registry.Remove(name)
	if !ok {
		http.Error(w, "no agent called "+name, http.StatusNotFound)
		return
	}
	// A failed send means the host is already gone, which is the state the caller
	// was asking for.
	send(stop{Type: "stop", Name: name})
	w.WriteHeader(http.StatusNoContent)
}

// sender writes commands to one host. Writes are serialised: a spawn and a stop
// can be dispatched by two members' requests at the same instant, and two
// goroutines writing to one websocket interleave frames.
func sender(ctx context.Context, conn *websocket.Conn) Sender {
	var mu sync.Mutex
	return func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		return write(ctx, conn, v)
	}
}

type hostHello struct {
	Type    string   `json:"type"`
	Dirs    []string `json:"dirs"`
	Allow   []string `json:"allow"`
	Version string   `json:"version"`
}

type hostWelcome struct {
	Type string `json:"type"`
	Org  string `json:"org"`
	Host string `json:"host"`
}

// agentReport is a host saying what became of an agent it was asked to run.
type agentReport struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

type spawn struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Command string `json:"command"`
}

type stop struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type createRequest struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Dir     string `json:"dir"`
	Command string `json:"command"`
}

type listResponse struct {
	Org    string     `json:"org"`
	Hosts  []HostInfo `json:"hosts"`
	Agents []Agent    `json:"agents"`
}

// read takes one JSON text frame. A timeout of zero waits as long as the context
// allows, which is what a connection idling between commands does.
//
// Only a transport failure is an error. A frame that cannot be understood comes
// back as the zero value, whose type matches nothing and is therefore ignored by
// every caller — which is what lets a newer host talk to an older hub.
func read[T any](ctx context.Context, conn *websocket.Conn, timeout time.Duration) (T, error) {
	var v T
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	kind, data, err := conn.Read(ctx)
	if err != nil {
		return v, err
	}
	if kind == websocket.MessageText {
		json.Unmarshal(data, &v)
	}
	return v, nil
}

func write(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
