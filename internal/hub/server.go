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
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
	"github.com/whosgotch/kolo/internal/ui"
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
	screens  *screens
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
	s := &Server{org: org, registry: NewRegistry(), screens: newScreens(), ln: ln, ctx: ctx, cancel: cancel}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", s.handleHost)
	mux.HandleFunc("GET /v1/agents", s.handleList)
	mux.HandleFunc("POST /v1/agents", s.handleCreate)
	mux.HandleFunc("DELETE /v1/agents/{name}", s.handleDelete)
	mux.HandleFunc("GET /v1/agent/{name}", s.handleScreen)
	mux.HandleFunc("GET /v1/watch/{name}", s.handleWatch)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.Handle("GET /assets/", http.FileServerFS(ui.FS))
	mux.HandleFunc("GET /{$}", s.handlePage)
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

// sessionCookie holds a member's token in the browser.
//
// It is the same secret the header carries, kept where script cannot read it. A
// separate session table would add a place for sessions to be looked up, expire
// and get out of step, and would protect nothing extra: whoever has the cookie
// has what the cookie was made from.
const sessionCookie = "kolo_session"

// authenticate resolves a request to a member, by header or by cookie.
//
// The token travels in a header rather than in the URL, because a URL is written
// to the access log of every proxy between the two machines, and to the hub's own.
func (s *Server) authenticate(r *http.Request) (Member, bool) {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return s.org.VerifyMember(token)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.org.VerifyMember(c.Value)
	}
	return Member{}, false
}

// handlePage serves the one page there is. Which agent it is showing is decided
// in the browser, so opening one and reloading lands in the same place.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	page, err := ui.FS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "no page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// handleLogin takes a member's token once and keeps it in a cookie, so that it
// is not pasted into a form on every visit — which is both tiresome and good
// training for handing the token to whoever asks for it next.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	if _, ok := s.org.VerifyMember(token); !ok {
		http.Redirect(w, r, "/?refused=1", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Lax is what keeps another site from posting to the hub in a member's
		// name: a cross-site POST carries no cookie, and every route that
		// changes anything is a POST or a DELETE.
		SameSite: http.SameSiteLaxMode,
		Secure:   overTLS(r),
		MaxAge:   int((90 * 24 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", HttpOnly: true, MaxAge: -1,
		SameSite: http.SameSiteLaxMode, Secure: overTLS(r),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// overTLS reports whether the member's own connection was encrypted, which is
// not the same as the hub's: the hub carries no TLS of its own and is expected
// to sit behind something that does.
func overTLS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
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

	if err := s.registry.Join(h.ID, hello.Dirs, hello.Allow, hello.Agents, sender(s.ctx, conn)); err != nil {
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
	member, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{
		Org:    s.org.Name,
		You:    member.Person(),
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
	created, _ := s.registry.Agent(agent.Name)
	if err := send(spawn{Type: "spawn", Agent: created}); err != nil {
		// The host went away between being chosen and being asked. Leaving the
		// agent listed would be listing something nobody ever started.
		s.registry.Remove(agent.Name)
		http.Error(w, "the host went away", http.StatusServiceUnavailable)
		return
	}
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

// handleScreen takes one agent's terminal from the machine running it.
//
// A connection of its own, per agent, rather than everything multiplexed down
// the host's control socket. It costs a few sockets and means a screen that
// stalls or drops belongs to one agent instead of all of them.
func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	h, ok := s.authenticateHost(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	// A host may only carry the screen of an agent the hub agrees is its own,
	// so one machine cannot answer for another machine's agent.
	if a, ok := s.registry.Agent(name); !ok || a.Host != h.ID {
		http.Error(w, "no agent called "+name+" here", http.StatusNotFound)
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

	hello, err := read[screenHello](ctx, conn, helloTimeout)
	if err != nil || hello.Type != "screen" || hello.Cols <= 0 || hello.Rows <= 0 {
		conn.Close(websocket.StatusPolicyViolation, "expected a screen size")
		return
	}

	live := s.screens.open(name, hello.Cols, hello.Rows)
	defer s.screens.close(name, live)

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Bytes are the agent's language and go to the terminal. Text is what
		// kolo says about the agent — the queue, the state — and is passed on
		// to viewers without being read, since the hub has no opinion about it.
		if kind == websocket.MessageBinary {
			live.Write(data)
		} else {
			live.Announce(json.RawMessage(data))
		}
	}
}

// handleWatch fans one agent's screen out to a member's browser.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	member, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	live, ok := s.screens.get(name)
	if !ok {
		http.Error(w, "no screen for "+name, http.StatusNotFound)
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
		defer cancel()
		s.takeFrom(ctx, conn, member, name)
	}()

	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()

	for _, m := range backlog {
		if err := forward(ctx, conn, m); err != nil {
			return
		}
	}
	if err := forward(ctx, conn, catchUp(live)); err != nil {
		return
	}
	// Waiting on the context as well as the screen, so that a browser which has
	// gone is let go of when takeFrom notices rather than whenever the agent next
	// draws something. A quiet agent could hold that goroutine for hours.
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-updates:
			if !ok {
				return
			}
			if err := forward(ctx, conn, m); err != nil {
				return
			}
		}
	}
}

// catchUp is what a joining viewer is missing. The repaint gives it the screen,
// but what may be done with that screen was announced by the host when it last
// changed — which was before this browser arrived. Somebody opening an agent
// that is mid-question would otherwise see the question and be offered no way to
// answer it until the next one.
func catchUp(live *session.Session) session.Message {
	b, _ := json.Marshal(struct {
		Type    string          `json:"type"`
		State   string          `json:"state"`
		Options []detect.Option `json:"options"`
	}{"state", live.State().String(), detect.Options(live.Text())})
	return session.Message{Control: true, Data: b}
}

// takeFrom carries what a member sends to the machine running the agent. It also
// notices the browser going away, which is what ends the connection.
//
// Who the message is from is decided here, from the credentials the connection
// was opened with. Nothing the browser says about that is read, so a name on a
// message cannot be somebody else's.
func (s *Server) takeFrom(ctx context.Context, conn *websocket.Conn, member Member, name string) {
	for {
		msg, err := read[viewerMessage](ctx, conn, 0)
		if err != nil {
			return
		}
		// The few things a member may do, and the hub reads none of them beyond
		// their name. What an answer means, and whether the agent is in a state
		// to take it, is known on the machine holding the screen.
		switch msg.Type {
		case "message", "answer", "interrupt", "restart", "fresh":
		default:
			continue
		}
		send, ok := s.registry.Sender(name)
		if !ok {
			continue
		}
		send(toAgent{
			Type: msg.Type, Name: name, From: member.Name,
			Text: msg.Text, Choice: msg.Choice, Label: msg.Label,
		})
	}
}

// forward sends one message to a viewer. Terminal output goes as binary and
// everything kolo says about it goes as text, so a browser can tell them apart
// without looking inside.
func forward(ctx context.Context, conn *websocket.Conn, m session.Message) error {
	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, kind, m.Data)
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
	Agents  []Agent  `json:"agents"`
	Version string   `json:"version"`
}

// viewerMessage is what a browser may send: words, an answer to the question on
// screen, a stop, a restart, or a start-fresh. Keystrokes are not among them and
// never will be — kolo submits with Enter, and Enter means something else
// entirely when the agent has a question up.
//
// An answer is a choice, not a key: the number of an option and the label the
// member was shown next to it. The label travels so the machine running the
// agent can refuse an answer to a question that has since been replaced.
type viewerMessage struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Choice int    `json:"choice"`
	Label  string `json:"label"`
}

type toAgent struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	From   string `json:"from"`
	Text   string `json:"text,omitempty"`
	Choice int    `json:"choice,omitempty"`
	Label  string `json:"label,omitempty"`
}

type screenHello struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
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

// spawn carries the whole record, not just what is needed to start a process.
// The host writes it down and reads it back in its hello, so who asked for an
// agent and when survives both machines restarting.
type spawn struct {
	Type  string `json:"type"`
	Agent Agent  `json:"agent"`
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
	You    Person     `json:"you"`
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
