package host

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/hub"
	"github.com/whosgotch/kolo/internal/relay"
	"github.com/whosgotch/kolo/internal/session"
)

const (
	cols = 120
	rows = 40
)

// A run shorter than shortRun counts as a failure, so an agent that can't
// start at all isn't restarted forever.
const (
	restartLimit = 3
	shortRun     = 10 * time.Second
)

var restartDelay = time.Second

const tick = 200 * time.Millisecond

// Agents is every agent running on this machine.
type Agents struct {
	cfg Config
	// Where the running set is written across restarts; empty keeps it in memory.
	state string

	mu      sync.Mutex
	running map[string]*process
	// An org stop forgets an agent; a machine shutdown keeps it to restart.
	closing bool

	// A full buffer drops news rather than blocking a process trying to exit.
	reports chan any

	// One writer to the state file at a time.
	saveMu sync.Mutex
}

type process struct {
	spec     hub.Agent
	agent    *agent.Agent
	live     *session.Session
	input    *relay.Relay
	started  time.Time
	stopping bool
	fails    int
	// fresh means the next launch must not resume.
	fresh bool
	// Launched resuming, so dying at once reads as the resume failing.
	resumed bool
	// An exit the org asked for; doesn't count towards giving up.
	bounced bool
	// Read off the agent's screen; a dead process can't be asked.
	session string
	// For kolo doctor.
	state detect.State
	since time.Time
}

func NewAgents(cfg Config, state string) *Agents {
	return &Agents{
		cfg:     cfg,
		state:   state,
		running: map[string]*process{},
		reports: make(chan any, 64),
	}
}

// Start runs an agent, after checking it is one this machine agreed to run. A
// new agent starts fresh: a conversation left in its directory is not its own.
func (a *Agents) Start(spec hub.Agent) error {
	if err := a.reserve(spec, true, ""); err != nil {
		return err
	}
	return a.begin(spec.Name)
}

func (a *Agents) runs(command string) bool {
	if slices.Contains(a.cfg.Allow, command) {
		return true
	}
	if !slices.Contains(a.cfg.Allow, hub.AllowAny) {
		return false
	}
	name := hub.Program(command)
	if name == "" || filepath.Base(name) != name {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func resumesByName(command string) bool {
	return adapter.For(command).ResumesByName()
}

// Callers must hold a.mu.
func (a *Agents) soleIn(dir, name string) bool {
	for other, p := range a.running {
		if other != name && p.spec.Dir == dir {
			return false
		}
	}
	return true
}

func newSessionID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func baseOf(command string) string {
	if argv := adapter.Argv(command); len(argv) > 0 {
		return filepath.Base(argv[0])
	}
	return command
}

func (a *Agents) reserve(spec hub.Agent, fresh bool, session string) error {
	spec.Dir = filepath.Clean(spec.Dir)
	if !slices.Contains(a.cfg.Dirs, spec.Dir) && !slices.Contains(a.cfg.Dirs, hub.DirAny) {
		return fmt.Errorf("this machine does not lend %s", spec.Dir)
	}
	if !a.runs(spec.Command) {
		return fmt.Errorf("this machine does not run %s", spec.Command)
	}

	a.mu.Lock()
	if _, taken := a.running[spec.Name]; taken {
		a.mu.Unlock()
		return fmt.Errorf("%s is already running here", spec.Name)
	}
	// One agent of each kind to a directory, as hub.Registry.Add also checks.
	for _, p := range a.running {
		if p.spec.Dir != spec.Dir {
			continue
		}
		sameKind := baseOf(spec.Command) == baseOf(p.spec.Command)
		if sameKind && !(resumesByName(spec.Command) && resumesByName(p.spec.Command)) {
			a.mu.Unlock()
			return fmt.Errorf("one %s to a directory: two of them asking for \"the last conversation here\" would come back as each other. A second checkout is how the same repo runs in parallel",
				baseOf(spec.Command))
		}
	}
	// Reserved before the process exists, so two spawns can't both find the
	// name free.
	a.running[spec.Name] = &process{spec: spec, fresh: fresh, session: session}
	a.mu.Unlock()
	return nil
}

func (a *Agents) begin(name string) error {
	if err := a.launch(name); err != nil {
		a.forget(name)
		return err
	}
	a.save()
	a.report(name, hub.StatusRunning, "")
	return nil
}

func (a *Agents) launch(name string) error {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.stopping {
		a.mu.Unlock()
		return fmt.Errorf("%s is no longer wanted", name)
	}
	spec, was := p.spec, p.session
	kind := adapter.For(spec.Command)
	// After the lent arguments, so the agent comes back as it was started.
	argv, resumed := adapter.Argv(spec.Command), false
	switch {
	case p.fresh && len(kind.Pin) > 0:
		id := newSessionID()
		if args, ok := kind.PinArgs(id); ok {
			p.session = id
			argv = append(argv, args...)
		}
	case !p.fresh:
		if r, ok := kind.ResumeArgs(was); ok {
			argv, resumed = append(argv, r...), true
		} else if len(kind.Continue) > 0 && a.soleIn(spec.Dir, name) {
			argv = append(argv, kind.Continue...)
		}
	}
	a.mu.Unlock()

	started, err := agent.Start(argv, spec.Dir, cols, rows)
	if err != nil {
		return err
	}

	a.mu.Lock()
	// Stop may have arrived mid-start; kill rather than leave it untracked.
	p, ok = a.running[name]
	if !ok || p.stopping {
		a.mu.Unlock()
		started.Close()
		return fmt.Errorf("%s is no longer wanted", name)
	}
	live := session.New(cols, rows, kind.Markers)
	input := relay.New(started, live.Screen, kind)
	screen, closeScreen := context.WithCancel(context.Background())
	p.agent, p.started, p.live, p.input = started, time.Now(), live, input
	p.resumed, p.fresh, p.bounced = resumed, false, false
	p.state, p.since = detect.Unknown, time.Now()
	a.mu.Unlock()

	// The PTY must be read or the agent blocks once its buffer fills.
	go io.Copy(live, started)
	go a.stream(screen, name, live)
	go a.watch(screen, name, kind, live)
	go a.wait(name, started, closeScreen)
	return nil
}

// Type gives the agent a member's keystrokes.
func (a *Agents) Type(name, keys string) error {
	v, err := a.reach(name)
	if err != nil {
		return err
	}
	return v.input.Type(keys)
}

func (a *Agents) Interrupt(name, from string) error {
	v, err := a.reach(name)
	if err != nil {
		return err
	}
	if err := v.input.Interrupt(); err != nil {
		return err
	}
	v.announce(event{Type: "interrupted", From: from})
	return nil
}

// Restart kills the process and lets supervision start it again, resuming its
// conversation.
func (a *Agents) Restart(name, from string) error { return a.bounce(name, from, false) }

// Fresh restarts the agent without its conversation.
func (a *Agents) Fresh(name, from string) error { return a.bounce(name, from, true) }

func (a *Agents) bounce(name, from string, fresh bool) error {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.agent == nil || p.stopping {
		a.mu.Unlock()
		return fmt.Errorf("%s is not running here", name)
	}
	p.bounced = true
	if fresh {
		// The id goes too, or a dying process brings back what was cleared.
		p.fresh, p.session = true, ""
	}
	running, v := p.agent, p.view()
	a.mu.Unlock()

	// On the outgoing screen, so watchers learn who did it before the repaint.
	what := "restarted"
	if fresh {
		what = "fresh"
	}
	v.announce(event{Type: what, From: from})
	running.Close()
	return nil
}

type view struct {
	live  *session.Session
	input *relay.Relay
}

func (p *process) view() view { return view{live: p.live, input: p.input} }

func (a *Agents) reach(name string) (view, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok || p.input == nil {
		return view{}, fmt.Errorf("%s is not running here", name)
	}
	return p.view(), nil
}

func (v view) announce(e event) {
	e.State = v.live.State().String()
	v.live.Announce(e)
}

// Polling: a screen arrangement has no event to subscribe to.
func (a *Agents) watch(ctx context.Context, name string, kind adapter.Adapter, live *session.Session) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	was := detect.Unknown
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if now := live.State(); now != was {
			was = now
			a.reading(name, now)
			live.Announce(event{Type: "state", State: now.String()})
		}
		if id := kind.SessionFrom(live.Text()); id != "" {
			a.remember(name, id)
		}
	}
}

func (a *Agents) reading(name string, now detect.State) {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.state == now {
		a.mu.Unlock()
		return
	}
	p.state, p.since = now, time.Now()
	a.mu.Unlock()
	a.save()
}

func (a *Agents) remember(name, id string) {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.session == id {
		a.mu.Unlock()
		return
	}
	p.session = id
	a.mu.Unlock()
	a.save()
}

type event struct {
	Type  string `json:"type"`
	From  string `json:"from,omitempty"`
	Text  string `json:"text,omitempty"`
	State string `json:"state,omitempty"`
}

func (a *Agents) wait(name string, started *agent.Agent, closeScreen context.CancelFunc) {
	err := started.Wait()
	// This process's screen ends with it, so watchers repaint from the new one.
	closeScreen()

	a.mu.Lock()
	p, ok := a.running[name]
	switch {
	case !ok:
		a.mu.Unlock()
		return
	case p.stopping:
		delete(a.running, name)
		a.mu.Unlock()
		a.save()
		return
	}
	reason := reasonFor(err, "the agent exited")
	switch {
	case p.bounced:
		// An exit the org asked for isn't a failure, however short the run.
		p.bounced = false
	case time.Since(p.started) > shortRun:
		p.fails = 0
	case p.resumed:
		// Dying at once reads as a refused resume; the refused id goes too.
		p.fresh, p.session = true, ""
		reason = "could not resume the conversation; starting fresh"
	default:
		p.fails++
	}
	if p.fails >= restartLimit {
		delete(a.running, name)
		a.mu.Unlock()
		a.save()
		a.report(name, hub.StatusFailed, reasonFor(err, "it will not stay running"))
		return
	}
	a.mu.Unlock()

	a.report(name, hub.StatusStarting, reason)
	time.Sleep(restartDelay)
	if err := a.launch(name); err != nil {
		return
	}
	a.report(name, hub.StatusRunning, "")
}

// Stop ends an agent for good. Stopping something not running isn't an error.
func (a *Agents) Stop(name string) {
	a.mu.Lock()
	p, ok := a.running[name]
	var running *agent.Agent
	if ok {
		// Set before the kill, so wait's restart doesn't race this stop.
		p.stopping = true
		running = p.agent
	}
	a.mu.Unlock()

	switch {
	case running != nil:
		running.Close()
	case ok:
		// Nothing will call wait, so forget here.
		a.forget(name)
		a.save()
	}
}

// StopAll ends every agent for host shutdown; what ran stays in the state file.
func (a *Agents) StopAll() {
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()

	for _, name := range a.Names() {
		a.Stop(name)
	}
}

// Specs is what this machine is running.
func (a *Agents) Specs() []hub.Agent {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]hub.Agent, 0, len(a.running))
	for _, p := range a.running {
		spec := p.spec
		spec.Status = hub.StatusRunning
		if p.agent == nil {
			spec.Status = hub.StatusStarting
		}
		out = append(out, spec)
	}
	slices.SortFunc(out, func(x, y hub.Agent) int { return x.CreatedAt.Compare(y.CreatedAt) })
	return out
}

func (a *Agents) Names() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.running))
	for name := range a.running {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// State is what this machine remembers about its work.
type State struct {
	Lends  []string `json:"lends,omitempty"`
	Allows []string `json:"allows,omitempty"`
	Agents []Record `json:"agents"`
}

// Record is one agent remembered across restarts.
type Record struct {
	Spec    hub.Agent `json:"spec"`
	Session string    `json:"session,omitempty"`
	// idle, busy, dialog or unknown.
	State string    `json:"state,omitempty"`
	Since time.Time `json:"since,omitzero"`
}

// ReadState reads a machine's last state file; a missing file is not an error.
func ReadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("host: read %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return State{}, fmt.Errorf("host: parse %s: %w", path, err)
	}
	return state, nil
}

// Restore starts everything this machine was running when it last stopped.
func (a *Agents) Restore() error {
	if a.state == "" {
		return nil
	}
	state, err := ReadState(a.state)
	if err != nil {
		return err
	}
	for _, rec := range state.Agents {
		if err := a.reserve(rec.Spec, false, rec.Session); err != nil {
			a.report(rec.Spec.Name, hub.StatusFailed, err.Error())
		}
	}
	for _, rec := range state.Agents {
		if err := a.begin(rec.Spec.Name); err != nil {
			a.report(rec.Spec.Name, hub.StatusFailed, err.Error())
		}
	}
	return nil
}

func (a *Agents) save() {
	a.mu.Lock()
	closing := a.closing
	a.mu.Unlock()

	if a.state == "" || closing {
		return
	}
	b, err := json.MarshalIndent(State{
		Lends: a.cfg.Dirs, Allows: a.cfg.Allow, Agents: a.records(),
	}, "", "  ")
	if err != nil {
		return
	}

	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(a.state), 0o700); err != nil {
		return
	}
	// Written whole and renamed: a half-written file reads as an empty machine.
	tmp := a.state + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, a.state); err != nil {
		os.Remove(tmp)
	}
}

// Oldest first, so a machine coming back restores in the order the org made them.
func (a *Agents) records() []Record {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]Record, 0, len(a.running))
	for _, p := range a.running {
		out = append(out, Record{
			Spec: p.spec, Session: p.session,
			State: p.state.String(), Since: p.since,
		})
	}
	slices.SortFunc(out, func(x, y Record) int { return x.Spec.CreatedAt.Compare(y.Spec.CreatedAt) })
	return out
}

func (a *Agents) forget(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.running, name)
}

func (a *Agents) report(name, status, reason string) {
	select {
	case a.reports <- statusReport{"status", name, status, reason}:
	default:
	}
}

type statusReport struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func reasonFor(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// Subscribes like a browser, so reconnecting opens with a repaint.
func (a *Agents) stream(ctx context.Context, name string, live *session.Session) {
	backoff := minBackoff
	for ctx.Err() == nil {
		a.push(ctx, name, live)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (a *Agents) push(ctx context.Context, name string, live *session.Session) error {
	conn, _, err := websocket.Dial(ctx, wsURL(a.cfg.Hub)+"/v1/agent/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.cfg.Token}},
	})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(controlLimit)

	// Markers travel with the screen, so the hub needs no adapter table.
	hello, _ := json.Marshal(map[string]any{
		"type": "screen", "cols": cols, "rows": rows, "markers": live.Markers(),
	})
	if err := send(ctx, conn, hello); err != nil {
		return err
	}

	// Reading is how a dead connection gets noticed.
	ctx = conn.CloseRead(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		session.Keepalive(ctx, conn, pingEvery, pingWithin)
	}()

	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()

	for _, m := range backlog {
		if err := session.Send(ctx, conn, m); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-updates:
			if !ok {
				return nil
			}
			if err := session.Send(ctx, conn, m); err != nil {
				return err
			}
		}
	}
}

func (a *Agents) refuse(name, reason string) {
	a.mu.Lock()
	var live *session.Session
	if p, ok := a.running[name]; ok {
		live = p.live
	}
	a.mu.Unlock()
	if live != nil {
		live.Announce(event{Type: "refused", Text: reason})
	}
}
