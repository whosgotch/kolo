package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// Nobody is at this machine's keyboard, so there is no terminal to take a size
// from and every viewer gets the same one.
const (
	cols = 120
	rows = 40
)

// A run too short to count as a run is counted as a failure instead, so an agent
// that cannot start at all is not restarted forever.
const (
	restartLimit = 3
	shortRun     = 10 * time.Second
)

// A variable so the tests do not have to wait it out.
var restartDelay = time.Second

// How often the screen is read, to tell the room what the agent is doing.
const tick = 200 * time.Millisecond

// Agents is every agent running on this machine.
type Agents struct {
	cfg Config
	// Where the running set is written, so a host restart brings back what the
	// org asked for. An empty path keeps it in memory.
	state string

	mu      sync.Mutex
	running map[string]*process
	// The difference between an agent the org stopped and one the machine is
	// taking down with it: the first is forgotten, the second stays in the state
	// file to be started again.
	closing bool

	// A full buffer drops news rather than blocking a process trying to exit; a
	// reconnecting host says what it has in its hello anyway.
	reports chan any
}

type process struct {
	spec     hub.Agent
	agent    *agent.Agent
	live     *session.Session
	input    *relay.Relay
	started  time.Time
	stopping bool
	fails    int
	// The next launch must not resume: the agent is new, or the org asked for a
	// clean start. Every other launch resumes.
	fresh bool
	// Whether this run was launched with the resume command, so a run that dies
	// at once can be read as the resume failing.
	resumed bool
	// An exit the org asked for, which must not count towards giving up.
	bounced bool
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
func (a *Agents) Start(spec hub.Agent) error { return a.start(spec, true) }

func (a *Agents) start(spec hub.Agent, fresh bool) error {
	spec.Dir = filepath.Clean(spec.Dir)
	if !slices.Contains(a.cfg.Dirs, spec.Dir) {
		return fmt.Errorf("this machine does not lend %s", spec.Dir)
	}
	if !slices.Contains(a.cfg.Allow, spec.Command) {
		return fmt.Errorf("this machine does not run %s", spec.Command)
	}

	a.mu.Lock()
	if _, taken := a.running[spec.Name]; taken {
		a.mu.Unlock()
		return fmt.Errorf("%s is already running here", spec.Name)
	}
	for other, p := range a.running {
		if p.spec.Dir == spec.Dir {
			a.mu.Unlock()
			return fmt.Errorf("%s is already working in %s", other, spec.Dir)
		}
	}
	// Reserved before the process exists, so two spawns arriving together cannot
	// both find the name free.
	a.running[spec.Name] = &process{spec: spec, fresh: fresh}
	a.mu.Unlock()

	if err := a.launch(spec.Name); err != nil {
		a.forget(spec.Name)
		return err
	}
	a.save()
	a.report(spec.Name, hub.StatusRunning, "")
	return nil
}

// launch starts the process for an agent already recorded in running.
func (a *Agents) launch(name string) error {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.stopping {
		a.mu.Unlock()
		return fmt.Errorf("%s is no longer wanted", name)
	}
	spec := p.spec
	// The resume flag goes after the arguments the host lent the command with,
	// so an agent comes back the way it was started rather than the way kolo
	// would have started it.
	argv, resumed := adapter.Argv(spec.Command), false
	if r := adapter.For(spec.Command).Resume; len(r) > 0 && !p.fresh {
		argv, resumed = append(argv, r...), true
	}
	a.mu.Unlock()

	started, err := agent.Start(argv, spec.Dir, cols, rows)
	if err != nil {
		return err
	}

	a.mu.Lock()
	// Stop may have arrived while the process was starting, in which case it is
	// killed here rather than left running with nobody tracking it.
	p, ok = a.running[name]
	if !ok || p.stopping {
		a.mu.Unlock()
		started.Close()
		return fmt.Errorf("%s is no longer wanted", name)
	}
	kind := adapter.For(spec.Command)
	live := session.New(cols, rows, kind.Markers)
	// Input reads the same screen the hub is shown, so a member answering a
	// question is answering the one everybody else is looking at.
	input := relay.New(started, live.Screen, kind)
	screen, closeScreen := context.WithCancel(context.Background())
	p.agent, p.started, p.live, p.input = started, time.Now(), live, input
	p.resumed, p.fresh, p.bounced = resumed, false, false
	a.mu.Unlock()

	// The PTY has to be read or the agent blocks once its buffer fills. Reading
	// it into the session is also what keeps the screen current.
	go io.Copy(live, started)
	go a.stream(screen, name, live)
	go a.watch(screen, input, live)
	go a.wait(name, started, closeScreen)
	return nil
}

// Answer gives the agent a member's choice from the question on its screen. The
// relay checks the choice against the screen as it stands, so an answer either
// lands on the question the member saw or does not land at all.
func (a *Agents) Answer(name, from string, choice int, label string) error {
	v, err := a.reach(name)
	if err != nil {
		return err
	}
	if err := v.input.Answer(choice, label); err != nil {
		return err
	}
	v.announce(event{Type: "answered", From: from, Text: label})
	return nil
}

// Type gives the agent a member's keystrokes. Not announced: who holds the
// keyboard is the news, and the hub says that.
func (a *Agents) Type(name, keys string) error {
	v, err := a.reach(name)
	if err != nil {
		return err
	}
	return v.input.Type(keys)
}

// Resize follows the size the org's browsers agreed on, so the agent draws a
// screen that fits in all of them.
func (a *Agents) Resize(name string, cols, rows int) error {
	a.mu.Lock()
	p, ok := a.running[name]
	if !ok || p.agent == nil {
		a.mu.Unlock()
		return fmt.Errorf("%s is not running here", name)
	}
	running, live := p.agent, p.live
	a.mu.Unlock()

	// The emulator first: the agent redraws the moment it is told, and a redraw
	// arriving at a screen still modelled at the old size wraps.
	live.Resize(cols, rows)
	return running.Resize(cols, rows)
}

// Interrupt stops the agent working, on behalf of the member who asked.
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

// Restart ends the agent's process and lets the supervision start it again,
// resuming its conversation. Unlike an interrupt it needs no particular screen:
// killing a process is safe on every screen there is.
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
		p.fresh = true
	}
	running, v := p.agent, p.view()
	a.mu.Unlock()

	// Said on the screen that is about to go, so everyone watching learns who did
	// it before they are repainted from the new process.
	what := "restarted"
	if fresh {
		what = "fresh"
	}
	v.announce(event{Type: what, From: from})
	running.Close()
	return nil
}

// view is one agent's screen and input as they stood when they were looked up.
// Taken together under the lock, because a restart replaces both: reading them
// off the process struct means one from before the restart and one from after.
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

// announce fills in what the agent looks like now, alongside the news itself.
func (v view) announce(e event) {
	e.State = v.live.State().String()
	e.Options = v.input.Options()
	v.live.Announce(e)
}

// watch tells everybody what the agent's screen has become. Polling, because a
// screen arriving at a particular arrangement has no event to subscribe to.
func (a *Agents) watch(ctx context.Context, input *relay.Relay, live *session.Session) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	was := detect.Unknown
	var asked []detect.Option
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// On change rather than every tick. The choices count as a change of
		// their own: one question answered and replaced by the next never leaves
		// the dialog state, and a page showing the old one would offer an answer
		// to a question that has gone.
		now, options := live.State(), input.Options()
		if now != was || !slices.Equal(options, asked) {
			was, asked = now, options
			live.Announce(event{Type: "state", State: now.String(), Options: options})
		}
	}
}

// event is what a page is told about an agent. The screen shows what the agent
// is doing; this is what kolo did to it, and who did it.
type event struct {
	Type    string          `json:"type"`
	From    string          `json:"from,omitempty"`
	Text    string          `json:"text,omitempty"`
	State   string          `json:"state,omitempty"`
	Options []detect.Option `json:"options,omitempty"`
}

// wait watches one agent and starts it again when it goes.
func (a *Agents) wait(name string, started *agent.Agent, closeScreen context.CancelFunc) {
	err := started.Wait()
	// This process's screen ends with it, so watchers are repainted from the new
	// one rather than from the last picture of something that has gone.
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
		// An exit the org asked for is not a failure, however short the run.
		p.bounced = false
	case time.Since(p.started) > shortRun:
		p.fails = 0
	case p.resumed:
		// A resumed run dying at once reads as the resume being refused. Starting
		// clean and saying so beats losing the context silently.
		p.fresh = true
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

// Stop ends an agent for good. Stopping something that is not running is not an
// error: the caller wanted it gone and it is.
func (a *Agents) Stop(name string) {
	a.mu.Lock()
	p, ok := a.running[name]
	var running *agent.Agent
	if ok {
		// Set before the kill, so the restart in wait sees it and does not race
		// the stop it is about to observe.
		p.stopping = true
		running = p.agent
	}
	a.mu.Unlock()

	switch {
	case running != nil:
		running.Close()
	case ok:
		// Killed between being recorded and being started; nothing will call
		// wait, so it is forgotten here.
		a.forget(name)
		a.save()
	}
}

// StopAll ends every agent, for when the host itself is going away. What was
// running stays in the state file, to come back with the machine.
func (a *Agents) StopAll() {
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()

	for _, name := range a.Names() {
		a.Stop(name)
	}
}

// Specs is what this machine is running, for a hub that has just met it — or met
// it again after the connection dropped.
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

// Restore starts everything this machine was running when it last stopped.
func (a *Agents) Restore() error {
	if a.state == "" {
		return nil
	}
	b, err := os.ReadFile(a.state)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("host: read %s: %w", a.state, err)
	}
	var specs []hub.Agent
	if err := json.Unmarshal(b, &specs); err != nil {
		return fmt.Errorf("host: parse %s: %w", a.state, err)
	}
	for _, spec := range specs {
		// One failing to come back does not stop the others. These agents were
		// mid-life when the machine went, so they resume rather than start fresh.
		if err := a.start(spec, false); err != nil {
			a.report(spec.Name, hub.StatusFailed, err.Error())
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
	b, err := json.MarshalIndent(a.Specs(), "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(a.state), 0o700); err != nil {
		return
	}
	os.WriteFile(a.state, b, 0o600)
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

// stream keeps this agent's screen going up to the hub for as long as the
// process lives. It subscribes exactly as a browser does, which is what makes
// reconnecting safe: a subscription opens with a repaint of the screen as it
// stands, so a hub that missed an outage is not left mid-escape-sequence.
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

	hello, _ := json.Marshal(map[string]any{"type": "screen", "cols": cols, "rows": rows})
	if err := send(ctx, conn, hello); err != nil {
		return err
	}

	// The hub says nothing on this socket, so reading it is only ever how the
	// connection dying is noticed. Without that, an agent sitting quietly at its
	// prompt writes nothing, discovers nothing, and never comes back after the
	// hub restarts — its screen is simply missing until it happens to move.
	ctx = conn.CloseRead(ctx)

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

// refuse tells an agent's watchers why something they sent went nowhere.
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
