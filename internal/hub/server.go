package hub

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/session"
	"golang.org/x/crypto/acme/autocert"
)

// The org's page, compiled into the binary. It sits here rather than in a
// package of its own because the hub is the only thing that serves it, and
// go:embed reaches into a directory below the package it is written in but
// never outside one.
//
//go:embed ui
var files embed.FS

// pages is files rooted at ui, so every path below is the path a browser asks
// for: index.html, assets/xterm.js. Sub cannot fail on a directory the
// compiler has just proved is there.
var pages, _ = fs.Sub(files, "ui")

const (
	helloTimeout = 10 * time.Second
	writeTimeout = 10 * time.Second
)

// What one frame may be. coder/websocket reads 32 KiB by default, which a
// repaint outgrows: a snapshot is as big as the screen it redraws, and a
// 120x40 grid in per-cell colour measures over 100 KB. The frame that is
// refused is the first one a host sends, so the host reconnects and sends the
// same one again for as long as the agent keeps drawing, and the agent simply
// never appears.
const (
	screenLimit  = 4 << 20
	controlLimit = 1 << 20
)

// How often a live connection is pinged, and how long a ping may go
// unanswered before the peer counts as gone. Carried on the server rather
// than read straight from here, so a test need not wait half a minute.
const (
	pingEvery  = 20 * time.Second
	pingWithin = 10 * time.Second
)

// Server is the hub: one org, its hosts, and the agents running on them.
type Server struct {
	// Replaced wholesale on claim or reload; read under the lock.
	orgMu    sync.RWMutex
	org      *Org
	registry *Registry
	screens  *screens
	typists  *typists
	journal  *journal
	// Held across any read-modify-write of the org file.
	orgFile sync.Mutex
	// Open connections, so revocation reaches streams, not just next requests.
	conns *conns
	ln    net.Listener
	srv   *http.Server
	// Set by Secure; both nil for plain http.
	acme       *autocert.Manager
	challenges net.Listener
	// Added by AlsoServe; too late once Serve has begun.
	startMu sync.Mutex
	serving bool
	extra   []net.Listener

	// How a peer that stopped answering is noticed. See pingEvery.
	ping, pingBy time.Duration

	// Cancelled by Close, the only way to reach hijacked websockets.
	ctx    context.Context
	cancel context.CancelFunc
}

// Listen binds addr, a host:port; use 0.0.0.0 to serve other machines.
func Listen(org *Org, addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hub: listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		org: org, registry: NewRegistry(), screens: newScreens(),
		typists: newTypists(), conns: newConns(), ln: ln, ctx: ctx, cancel: cancel,
		ping: pingEvery, pingBy: pingWithin,
	}

	// Beside the org file, because the record is the org's rather than the
	// machine's. Moving one moves the other. An org with no file behind it keeps
	// its journal in memory, which is what a test has and nothing else does.
	s.journal, err = openJournal(journalPath(org.path))
	if err != nil {
		log.Printf("hub: %v. This run is not being written down", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", s.handleHost)
	mux.HandleFunc("GET /v1/agents", s.handleList)
	mux.HandleFunc("POST /v1/agents", s.handleCreate)
	mux.HandleFunc("PATCH /v1/agents/{name}", s.handleRelabel)
	mux.HandleFunc("DELETE /v1/agents/{name}", s.handleDelete)
	mux.HandleFunc("GET /v1/log", s.handleLog)
	mux.HandleFunc("GET /v1/agent/{name}", s.handleScreen)
	mux.HandleFunc("GET /v1/watch/{name}", s.handleWatch)
	mux.HandleFunc("GET /join", s.handleJoinPage)
	mux.HandleFunc("POST /join", s.handleJoin)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.Handle("GET /assets/", http.FileServerFS(pages))
	mux.HandleFunc("GET /{$}", s.handlePage)

	// No other site's page may make kolo do something on a member's behalf.
	// Safe methods are left alone, so the websockets, which upgrade on GET,
	// keep their own Origin check and nothing else changes. A request with
	// neither Sec-Fetch-Site nor Origin is a program rather than a page, so
	// curl and the host half are unaffected.
	//
	// Worth having even though every mutating route already needs a token:
	// /login mints the session rather than requiring one, so without this a
	// page elsewhere could put somebody on a member they did not choose, and
	// the log would carry that name against what they went on to do.
	s.srv = &http.Server{Handler: http.NewCrossOriginProtection().Handler(mux)}
	return s, nil
}

// Addr is the listening address, with a requested port of 0 resolved.
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
		// Empty paths: the certificate comes from the autocert manager, not disk.
		serve = func(ln net.Listener) error { return s.srv.ServeTLS(ln, "", "") }
	}
	if err := serve(s.ln); err != nil && !isClosed(err) {
		return err
	}
	return nil
}

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
	s.journal.Close()
	return s.srv.Close()
}

const sessionCookie = "kolo_session"

func (s *Server) authenticate(r *http.Request) (Member, bool) {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return s.verifyMember(token)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.verifyMember(c.Value)
	}
	return Member{}, false
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	page, err := fs.ReadFile(pages, "index.html")
	if err != nil {
		http.Error(w, "no page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	if _, ok := s.verifyMember(token); !ok {
		http.Redirect(w, r, "/?refused=1", http.StatusSeeOther)
		return
	}
	s.signIn(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Lax: mutating routes are POST or DELETE, and cross-site ones carry
		// no cookie.
		SameSite: http.SameSiteLaxMode,
		Secure:   overTLS(r),
		MaxAge:   int((90 * 24 * time.Hour).Seconds()),
	})
}

// handleJoinPage serves the join form. The invite rides in the URL fragment,
// which never reaches the server.
func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	page, err := template.ParseFS(pages, "join.html")
	if err != nil {
		http.Error(w, "no page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page.Execute(w, struct{ Org string }{s.orgName()})
}

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

	// One claim at a time: Claim reads and writes the org file.
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

// overTLS reports whether this request arrived encrypted, proxy or not.
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

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	h, ok := s.authenticateHost(r)
	if !ok {
		// Before the upgrade, so refusals are a status code, not a dead socket.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(controlLimit)

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	go func() {
		defer cancel()
		session.Keepalive(ctx, conn, s.ping, s.pingBy)
	}()

	// Recorded before the hello, so removal during connect drops the conn.
	defer s.conns.add(held{id: h.ID, hash: h.TokenHash, isHost: true, cancel: cancel})()

	hello, err := read[hostHello](ctx, conn, helloTimeout)
	if err != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusPolicyViolation, "expected a hello")
		return
	}

	if err := s.registry.Join(h.ID, hello.Dirs, hello.Allow, hello.Found, hello.ByName, hello.Agents, sender(s.ctx, conn)); err != nil {
		conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	log.Printf("hub: %s joined %s, running %s", h.ID, s.orgName(), build(hello.Version))
	defer func() {
		for _, name := range s.registry.Leave(h.ID) {
			s.journal.add(Entry{Agent: name, What: WhatGone, Text: h.ID + " disconnected"})
			s.journal.forget(name)
		}
	}()

	if err := write(ctx, conn, hostWelcome{Type: "welcome", Org: s.orgName(), Host: h.ID}); err != nil {
		return
	}

	for {
		report, err := read[agentReport](ctx, conn, 0)
		if err != nil {
			return
		}
		// Unrecognised types are ignored, so newer hosts suit older hubs.
		if report.Type == "status" {
			s.registry.SetStatus(report.Name, report.Status, label(report.Error, maxLabel))
			if report.Status == StatusFailed {
				s.journal.add(Entry{Agent: report.Name, What: WhatFailed, Text: report.Error})
			}
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

// handleLog is the record of who asked for what: an agent's own history, or
// the whole org's when no agent is named.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limit := defaultEntries
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = min(max(n, 0), keepEntries)
	}
	writeJSON(w, http.StatusOK, logResponse{
		Entries: s.journal.tail(r.URL.Query().Get("agent"), limit),
	})
}

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
	s.journal.add(Entry{
		Agent: created.Name, What: WhatCreated, Who: member.Person(),
		Text: created.Dir + " · " + created.Command,
	})
	if err := send(spawn{Type: "spawn", Agent: created}); err != nil {
		// The host vanished between choosing and asking; drop the phantom.
		s.registry.Remove(agent.Name)
		s.journal.add(Entry{Agent: agent.Name, What: WhatFailed, Text: "the host went away"})
		http.Error(w, "the host went away", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleRelabel(w http.ResponseWriter, r *http.Request) {
	member, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	var req relabelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	newLabel := label(req.Label, maxLabel)
	agent, err := s.registry.SetLabel(name, newLabel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.journal.add(Entry{Agent: name, What: WhatRelabeled, Who: member.Person(), Text: newLabel})
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	member, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	send, ok := s.registry.Remove(name)
	if !ok {
		http.Error(w, "no agent called "+name, http.StatusNotFound)
		return
	}
	s.journal.add(Entry{Agent: name, What: WhatStopped, Who: member.Person()})
	s.journal.forget(name)
	// A failed send means the host is already gone, which is the wanted state.
	send(stop{Type: "stop", Name: name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	h, ok := s.authenticateHost(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
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
	// Repaints arrive here, so this is the one that has to be generous.
	conn.SetReadLimit(screenLimit)

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	go func() {
		defer cancel()
		session.Keepalive(ctx, conn, s.ping, s.pingBy)
	}()

	hello, err := read[screenHello](ctx, conn, helloTimeout)
	if err != nil || hello.Type != "screen" || hello.Cols <= 0 || hello.Rows <= 0 {
		conn.Close(websocket.StatusPolicyViolation, "expected a screen size")
		return
	}

	// Markers come from the host; the hub knows no agent kinds itself.
	live := s.screens.open(name, hello.Cols, hello.Rows, hello.Markers)
	defer s.screens.close(name, live)

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Binary goes onto the screen; text is kolo's own, announced unread.
		if kind == websocket.MessageBinary {
			live.Write(data)
		} else {
			live.Announce(json.RawMessage(data))
		}
	}
}

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
	// A pasted prompt arrives as one keys message, so this is not 32 KiB either.
	conn.SetReadLimit(controlLimit)

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// Revocation has to reach open streams, not just later requests.
	defer s.conns.add(held{id: member.ID, hash: member.TokenHash, cancel: cancel})()
	go func() {
		defer cancel()
		s.takeFrom(ctx, conn, member, name)
	}()
	go func() {
		defer cancel()
		session.Keepalive(ctx, conn, s.ping, s.pingBy)
	}()

	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()
	defer s.resize(name, conn, 0, 0)

	for _, m := range backlog {
		if err := session.Send(ctx, conn, m); err != nil {
			return
		}
	}
	if err := session.Send(ctx, conn, catchUp(live)); err != nil {
		return
	}
	// Joiners learn who typed last.
	if who, typed := s.typists.get(name); typed {
		live.Announce(struct {
			Type string `json:"type"`
			Who  string `json:"who"`
			ID   string `json:"id"`
		}{"keyboard", who.Name, who.ID})
	}
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

// catchUp announces the agent state a joiner missed before subscribing.
func catchUp(live *session.Session) session.Message {
	b, _ := json.Marshal(struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}{"state", live.State().String()})
	return session.Message{Control: true, Data: b}
}

// takeFrom relays a member's messages to the host. Who a message is from
// comes from the connection's credentials, never from the message.
func (s *Server) takeFrom(ctx context.Context, conn *websocket.Conn, member Member, name string) {
	for {
		msg, err := read[viewerMessage](ctx, conn, 0)
		if err != nil {
			return
		}
		switch msg.Type {
		case "size":
			s.resize(name, conn, msg.Cols, msg.Rows)
			continue
		case "keys":
			if msg.Keys == "" {
				continue
			}
			// Typing is claiming: whoever's keys arrive last is the typist.
			if was, changed := s.typists.set(name, member.Person()); changed {
				s.sayKeyboard(name, member.Person(), was)
			}
			s.journal.typed(name, member.Person(), msg.Keys)
		case "interrupt", "restart", "fresh":
			// Whatever was half typed is not going to be sent now.
			s.journal.add(Entry{Agent: name, What: done(msg.Type), Who: member.Person()})
			s.journal.forget(name)
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

func done(action string) string {
	switch action {
	case "interrupt":
		return WhatInterrupted
	case "restart":
		return WhatRestarted
	default:
		return WhatFresh
	}
}

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

// resize proposes one browser's size; screens.propose settles what all watch.
func (s *Server) resize(name string, at any, cols, rows int) {
	cols, rows, changed := s.screens.propose(name, at, cols, rows)
	if !changed {
		return
	}
	if send, ok := s.registry.Sender(name); ok {
		send(toAgent{Type: "resize", Name: name, Cols: cols, Rows: rows})
	}
}

// sender serialises writes to one host: two members dispatching at once must
// not interleave frames on one websocket.
func sender(ctx context.Context, conn *websocket.Conn) Sender {
	var mu sync.Mutex
	return func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		return write(ctx, conn, v)
	}
}

// build is what a host says it is running. Hosts that predate the hub reading
// this said nothing, which is worth a word of its own rather than a blank.
func build(version string) string {
	if version == "" {
		return "an unknown build"
	}
	return "kolo " + version
}

type hostHello struct {
	Type    string   `json:"type"`
	Dirs    []string `json:"dirs"`
	Allow   []string `json:"allow"`
	Found   []string `json:"found"`
	ByName  []string `json:"by_name"`
	Agents  []Agent  `json:"agents"`
	Version string   `json:"version"`
}

type viewerMessage struct {
	Type string `json:"type"`
	// Raw terminal input, already encoded by the browser.
	Keys string `json:"keys"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
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

type agentReport struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// spawn carries the whole record; hosts replay it in their hello on reconnect.
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

type relabelRequest struct {
	Label string `json:"label"`
}

type listResponse struct {
	Org    string     `json:"org"`
	You    Person     `json:"you"`
	Hosts  []HostInfo `json:"hosts"`
	Agents []Agent    `json:"agents"`
}

// defaultEntries is enough for a page to open on, without a browser that asked
// for nothing being sent a month.
const defaultEntries = 100

type logResponse struct {
	Entries []Entry `json:"entries"`
}

// read takes one JSON text frame; timeout 0 waits on ctx. An unreadable
// frame yields the zero value, so new hosts suit old hubs.
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
