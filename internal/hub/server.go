package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
	"github.com/whosgotch/kolo/internal/ui"
	"golang.org/x/crypto/acme/autocert"
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
	// Replaced wholesale when somebody claims an invite or the file changes on
	// disk. Read under the lock, never reached into directly.
	orgMu     sync.RWMutex
	org       *Org
	registry  *Registry
	screens   *screens
	keyboards *keyboards
	// Held across any read-modify-write of the org file: two people opening the
	// same invite together would otherwise become one member, and a reload
	// landing between a claim's read and its write would undo it.
	orgFile sync.Mutex
	// Every connection open right now, so revoking somebody reaches the ones
	// they already have rather than only their next request.
	conns *conns
	ln    net.Listener
	srv   *http.Server
	// Set by Secure: the certificate manager, and the port 80 listener Let's
	// Encrypt reaches it on. Both nil for a hub serving plain http.
	acme       *autocert.Manager
	challenges net.Listener
	// Added by AlsoServe: plain http, for a caller on this machine. Guarded by
	// startMu, which also marks the point after which adding one is too late.
	startMu sync.Mutex
	serving bool
	extra   []net.Listener

	// Cancelled by Close, which is the only thing that reaches a host connection:
	// net/http does not, a websocket having been hijacked out of its hands.
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
	s := &Server{
		org: org, registry: NewRegistry(), screens: newScreens(),
		keyboards: newKeyboards(), conns: newConns(), ln: ln, ctx: ctx, cancel: cancel,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", s.handleHost)
	mux.HandleFunc("GET /v1/agents", s.handleList)
	mux.HandleFunc("POST /v1/agents", s.handleCreate)
	mux.HandleFunc("DELETE /v1/agents/{name}", s.handleDelete)
	mux.HandleFunc("GET /v1/agent/{name}", s.handleScreen)
	mux.HandleFunc("GET /v1/watch/{name}", s.handleWatch)
	mux.HandleFunc("GET /join", s.handleJoinPage)
	mux.HandleFunc("POST /join", s.handleJoin)
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

func (s *Server) Registry() *Registry { return s.registry }

func (s *Server) verifyMember(token string) (Member, bool) {
	s.orgMu.RLock()
	defer s.orgMu.RUnlock()
	return s.org.VerifyMember(token)
}

func (s *Server) verifyHost(token string) (Host, bool) {
	s.orgMu.RLock()
	defer s.orgMu.RUnlock()
	return s.org.VerifyHost(token)
}

func (s *Server) orgName() string {
	s.orgMu.RLock()
	defer s.orgMu.RUnlock()
	return s.org.Name
}

func (s *Server) orgPath() string {
	s.orgMu.RLock()
	defer s.orgMu.RUnlock()
	return s.org.path
}

func (s *Server) Serve() error {
	go s.watchOrg()
	s.startMu.Lock()
	s.serving = true
	extra := slices.Clone(s.extra)
	s.startMu.Unlock()

	for _, ln := range extra {
		go func() {
			if err := s.srv.Serve(ln); err != nil && !isClosed(err) {
				log.Printf("hub: %v", err)
			}
		}()
	}
	serve := s.srv.Serve
	if s.acme != nil {
		go s.serveChallenges()
		// Empty paths: the certificate comes from the manager in TLSConfig, not
		// from files on disk.
		serve = func(ln net.Listener) error { return s.srv.ServeTLS(ln, "", "") }
	}
	if err := serve(s.ln); err != nil && !isClosed(err) {
		return err
	}
	return nil
}

// isClosed reports whether an error is only this hub being shut down.
func isClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed)
}

// Close stops serving and disconnects every host.
func (s *Server) Close() error {
	s.cancel()
	if s.challenges != nil {
		s.challenges.Close()
	}
	for _, ln := range s.extra {
		ln.Close()
	}
	return s.srv.Close()
}

// sessionCookie holds a member's token in the browser: the same secret the
// header carries, kept where script cannot read it. A separate session table
// would protect nothing extra, since whoever has the cookie has what it was
// made from.
const sessionCookie = "kolo_session"

// authenticate resolves a request to a member, by header or by cookie. The token
// travels in a header rather than the URL, which every proxy in between logs.
func (s *Server) authenticate(r *http.Request) (Member, bool) {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return s.verifyMember(token)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.verifyMember(c.Value)
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

// handleLogin takes a member's token once and keeps it in a cookie, so it is not
// pasted into a form on every visit.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	if _, ok := s.verifyMember(token); !ok {
		http.Redirect(w, r, "/?refused=1", http.StatusSeeOther)
		return
	}
	s.signIn(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// signIn puts a member's token where the browser will send it back.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, token string) {
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
}

// handleJoinPage asks whoever opened an invite what to call them. The invite
// itself is in the fragment, which never reaches here.
func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	page, err := template.ParseFS(ui.FS, "join.html")
	if err != nil {
		http.Error(w, "no page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page.Execute(w, struct{ Org string }{s.orgName()})
}

// handleJoin spends an invite on a new member and signs them in, so the whole of
// joining is opening a link and saying a name.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	invite := strings.TrimSpace(r.FormValue("invite"))
	name := strings.TrimSpace(r.FormValue("name"))
	if invite == "" || name == "" {
		http.Redirect(w, r, "/join?refused=1", http.StatusSeeOther)
		return
	}

	path := s.orgPath()
	if path == "" {
		http.Error(w, "this hub cannot take new members", http.StatusNotImplemented)
		return
	}

	// One claim at a time: two people opening the same link together would
	// otherwise read the same file, and the second write would drop the first.
	s.orgFile.Lock()
	defer s.orgFile.Unlock()

	org, member, token, err := Claim(path, invite, name)
	if err != nil {
		if errors.Is(err, ErrNoInvite) || errors.Is(err, ErrInviteSpent) {
			http.Redirect(w, r, "/join?refused=1", http.StatusSeeOther)
			return
		}
		http.Error(w, "could not join", http.StatusInternalServerError)
		return
	}
	s.orgMu.Lock()
	s.org = org
	s.orgMu.Unlock()
	log.Printf("hub: %s joined %s as %s", member.Name, org.Name, member.ID)

	s.signIn(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", HttpOnly: true, MaxAge: -1,
		SameSite: http.SameSiteLaxMode, Secure: overTLS(r),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// overTLS reports whether the member's own connection was encrypted, which is not
// the hub's: it carries no TLS itself and sits behind something that does.
func overTLS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) authenticateHost(r *http.Request) (Host, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Host{}, false
	}
	return s.verifyHost(token)
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

	// Recorded before the hello, so a machine removed from the org while it was
	// connecting is dropped rather than waited on.
	defer s.conns.add(held{id: h.ID, hash: h.TokenHash, isHost: true, cancel: cancel})()

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

	if err := write(ctx, conn, hostWelcome{Type: "welcome", Org: s.orgName(), Host: h.ID}); err != nil {
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
		Org:    s.orgName(),
		You:    member.Person(),
		Hosts:  s.registry.Hosts(),
		Agents: s.registry.Agents(),
	})
}

// handleCreate asks a host to run an agent. Every member may: there are no roles,
// and the record of who asked stands in for them.
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

// handleScreen takes one agent's terminal from the machine running it. A
// connection per agent rather than everything multiplexed down the host's
// control socket, so a screen that stalls belongs to one agent instead of all.
func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	h, ok := s.authenticateHost(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	// A host may only carry the screen of an agent the hub agrees is its own,
	// so one machine cannot answer for another machine's agent.
	a, ok := s.registry.Agent(name)
	if !ok || a.Host != h.ID {
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

	// The hub reads this screen too — to catch a joiner up on what may be done
	// with it — so it needs the markers of the kind drawing it. They come from
	// the host, which is where the kinds are configured; the hub knows no agent
	// kinds of its own and does not have to be upgraded to learn one.
	live := s.screens.open(name, hello.Cols, hello.Rows, hello.Markers)
	defer s.screens.close(name, live)

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Bytes are the agent's language and go to the terminal. Text is what kolo
		// says about it, passed on unread: the hub has no opinion about it.
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
	// A member removed from the org while watching stops watching. Their next
	// request would fail anyway; a stream of frames is not a next request.
	defer s.conns.add(held{id: member.ID, hash: member.TokenHash, cancel: cancel})()
	go func() {
		defer cancel()
		s.takeFrom(ctx, conn, member, name)
	}()

	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()
	// A browser that closes is a hand off the keyboard, or the agent stays stuck
	// with a typist who has gone home.
	defer s.dropKeyboard(name, member, conn)
	defer s.resize(name, conn, 0, 0)

	for _, m := range backlog {
		if err := session.Send(ctx, conn, m); err != nil {
			return
		}
	}
	if err := session.Send(ctx, conn, catchUp(live)); err != nil {
		return
	}
	// Who is typing is part of what a joiner is missing: without it a second
	// browser shows a free keyboard and two people take it in the same second.
	if who, held := s.keyboards.holder(name); held {
		live.Announce(struct {
			Type string `json:"type"`
			Who  string `json:"who"`
			ID   string `json:"id"`
		}{"keyboard", who.Name, who.ID})
	}
	// On the context as well as the screen, so a browser that has gone is let go
	// of when takeFrom notices. A quiet agent could hold the goroutine for hours.
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-updates:
			if !ok {
				return
			}
			if err := session.Send(ctx, conn, m); err != nil {
				return
			}
		}
	}
}

// catchUp is what a joining viewer is missing. The repaint gives it the screen,
// but what the agent is doing was announced before this browser arrived — so an
// agent opened while it is working would look like one sitting idle.
func catchUp(live *session.Session) session.Message {
	b, _ := json.Marshal(struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}{"state", live.State().String()})
	return session.Message{Control: true, Data: b}
}

// takeFrom carries what a member sends to the machine running the agent, and
// notices the browser going away.
//
// Who a message is from is decided here, from the credentials the connection was
// opened with, so a name on a message cannot be somebody else's.
func (s *Server) takeFrom(ctx context.Context, conn *websocket.Conn, member Member, name string) {
	for {
		msg, err := read[viewerMessage](ctx, conn, 0)
		if err != nil {
			return
		}
		// The hub reads none of these beyond their name: whether the agent can
		// take what is being sent is known on the machine with the screen. Who may
		// type is the exception, because who a member is is known here and nowhere
		// else.
		switch msg.Type {
		case "take":
			s.giveKeyboard(name, member, conn)
			continue
		case "release":
			s.dropKeyboard(name, member, conn)
			continue
		case "size":
			s.resize(name, conn, msg.Cols, msg.Rows)
			continue
		case "keys":
			// Checked per keystroke rather than once: the keyboard changes hands
			// while a member is mid-word, and the half typed after it is not theirs
			// to send.
			if !s.keyboards.holds(name, conn) || msg.Keys == "" {
				continue
			}
		case "interrupt", "restart", "fresh":
		default:
			continue
		}
		send, ok := s.registry.Sender(name)
		if !ok {
			continue
		}
		send(toAgent{Type: msg.Type, Name: name, From: member.Name, Keys: msg.Keys})
	}
}

// giveKeyboard hands one agent's keyboard to a member and tells everybody
// watching. Nobody is asked and nobody can refuse: the agent belongs to the org,
// and taking the keyboard in front of everyone is the whole of the protocol.
func (s *Server) giveKeyboard(name string, member Member, at any) {
	was, taken := s.keyboards.take(name, member.Person(), at)
	if !taken {
		return
	}
	s.sayKeyboard(name, member.Person(), was)
}

// dropKeyboard gives it up, whether the member let go or their browser did.
func (s *Server) dropKeyboard(name string, member Member, at any) {
	if !s.keyboards.release(name, at) {
		return
	}
	s.sayKeyboard(name, Person{}, member.Person())
}

// sayKeyboard announces who is typing, through the agent's own screen, so it
// arrives interleaved with what the agent is doing rather than out of band.
func (s *Server) sayKeyboard(name string, who, was Person) {
	live, ok := s.screens.get(name)
	if !ok {
		return
	}
	live.Announce(struct {
		Type string `json:"type"`
		Who  string `json:"who,omitempty"`
		ID   string `json:"id,omitempty"`
		Was  string `json:"was,omitempty"`
	}{"keyboard", who.Name, who.ID, was.Name})
}

// resize records what one browser can draw and asks the host for a size every
// browser can. See smallest: the agent's terminal is one grid shown in several
// windows, and the smallest is the only one that fits in all of them.
func (s *Server) resize(name string, at any, cols, rows int) {
	cols, rows, changed := s.screens.propose(name, at, cols, rows)
	if !changed {
		return
	}
	if send, ok := s.registry.Sender(name); ok {
		send(toAgent{Type: "resize", Name: name, Cols: cols, Rows: rows})
	}
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

// viewerMessage is what a browser may send. Keys go through raw, from the one
// member holding the keyboard: they are watching the screen those keys land on,
// so Enter at a question is a decision rather than an accident.
type viewerMessage struct {
	Type string `json:"type"`
	// Keys are raw terminal input, already encoded by the browser's terminal.
	Keys string `json:"keys"`
	// The size the browser can draw, for agreeing one everybody can see.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type toAgent struct {
	Type string `json:"type"`
	Name string `json:"name"`
	From string `json:"from"`
	Keys string `json:"keys,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type screenHello struct {
	Type    string         `json:"type"`
	Cols    int            `json:"cols"`
	Rows    int            `json:"rows"`
	Markers detect.Markers `json:"markers"`
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
// Only a transport failure is an error. An unreadable frame comes back as the
// zero value, whose type matches nothing and is ignored by every caller — which
// is what lets a newer host talk to an older hub.
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
