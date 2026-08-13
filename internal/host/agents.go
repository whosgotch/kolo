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
	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/hub"
	"github.com/whosgotch/kolo/internal/relay"
	"github.com/whosgotch/kolo/internal/session"
)

// The screen the agents run on. Nobody is at this machine's keyboard, so there
// is no terminal to take a size from and every viewer gets the same one.
const (
	cols = 120
	rows = 40
)

// Restarting. An agent is meant to outlive the things that interrupt it, so one
// that exits is started again — but an agent that cannot start at all would
// otherwise be restarted forever, so a run too short to count as a run is
// counted as a failure instead, and three of those stop the attempt.
const (
	restartLimit = 3
	shortRun     = 10 * time.Second
)

// restartDelay is a variable so the tests do not have to wait it out.
var restartDelay = time.Second

// tick is how often the queue asks the screen whether it may send. Fast enough
// that a message does not sit visibly waiting after the agent is free, slow
// enough to be nothing next to reading a PTY.
const tick = 200 * time.Millisecond

// Agents is every agent running on this machine.
type Agents struct {
	cfg Config
	// state is where the running set is written, so that restarting the host
	// brings back what the org asked for rather than an empty machine. An empty
	// path keeps it in memory.
	state string

	mu      sync.Mutex
	running map[string]*process
	// closing is the difference between an agent the org stopped and one the
	// machine is taking down with it. The first is forgotten; the second stays
	// in the state file, to be started again when the machine comes back.
	closing bool

	// reports go to the connection, which may be down. A full buffer drops news
	// rather than blocking a process trying to exit; a reconnecting host says
	// what it has in its hello anyway.
	reports chan any
}

type process struct {
	spec     hub.Agent
	agent    *agent.Agent
	live     *session.Session
	queue    *relay.Relay
	started  time.Time
	stopping bool
	fails    int
}

func NewAgents(cfg Config, state string) *Agents {
	return &Agents{
		cfg:     cfg,
		state:   state,
		running: map[string]*process{},
		reports: make(chan any, 64),
	}
}

// Start runs an agent, after checking it is one this machine agreed to run.
func (a *Agents) Start(spec hub.Agent) error {
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
	a.running[spec.Name] = &process{spec: spec}
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
	a.mu.Unlock()

	started, err := agent.Start([]string{spec.Command}, spec.Dir, cols, rows)
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
	live := session.New(cols, rows)
	// The queue reads the same screen the hub is shown, so what decides when a
	// line may be typed is looking at what everybody else is looking at.
	queue := relay.New(started, live.Text)
	screen, closeScreen := context.WithCancel(context.Background())
	p.agent, p.started, p.live, p.queue = started, time.Now(), live, queue
	a.mu.Unlock()

	// The PTY has to be read or the agent blocks once its buffer fills. Reading
	// it into the session is also what keeps the screen current, which is what
	// the hub is sent and what the queue reads before typing anything.
	go io.Copy(live, started)
	go a.stream(screen, name, live)
	go a.release(screen, queue, live)
	go a.wait(name, started, closeScreen)
	return nil
}

// Send puts a member's line in an agent's queue. It is not typed here, however
// idle the agent looks: everything goes through the queue, so there is one path
// to the agent and one place that decides when it opens.
func (a *Agents) Send(name, from, text string) error {
	p, err := a.reach(name)
	if err != nil {
		return err
	}

	m, err := p.queue.Submit(from, text)
	if err != nil {
		return err
	}
	p.announce(event{Type: "queued", From: m.Nickname, Text: m.Text})
	return nil
}

// Answer gives the agent a member's choice from the question on its screen.
//
// Unlike a message it is never queued. The relay checks the choice against the
// screen as it stands and refuses it otherwise, so an answer either lands on the
// question the member was looking at or does not land at all.
func (a *Agents) Answer(name, from string, choice int, label string) error {
	p, err := a.reach(name)
	if err != nil {
		return err
	}
	if err := p.queue.Answer(choice, label); err != nil {
		return err
	}
	p.announce(event{Type: "answered", From: from, Text: label})
	return nil
}

// Interrupt stops the agent working, on behalf of the member who asked. It is
// what the org has instead of walking to the host's keyboard when an agent is
// going down a wrong path.
func (a *Agents) Interrupt(name, from string) error {
	p, err := a.reach(name)
	if err != nil {
		return err
	}
	if err := p.queue.Interrupt(); err != nil {
		return err
	}
	p.announce(event{Type: "interrupted", From: from})
	return nil
}

// reach finds an agent that is running and has a screen to act on.
func (a *Agents) reach(name string) (*process, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok || p.queue == nil {
		return nil, fmt.Errorf("%s is not running here", name)
	}
	return p, nil
}

// announce fills in what everyone watching needs alongside the news itself: what
// the agent looks like now, and how much is still waiting.
func (p *process) announce(e event) {
	e.Pending = len(p.queue.Pending())
	e.State = p.live.State().String()
	e.Options = p.queue.Options()
	p.live.Announce(e)
}

// release gives the agent one queued line whenever its screen says it may take
// one, and tells everyone watching what happened.
//
// Polling rather than waking on a change: what it is waiting for is a screen
// arriving at a particular arrangement, which has no event to subscribe to.
func (a *Agents) release(ctx context.Context, queue *relay.Relay, live *session.Session) {
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

		// Announced on change rather than every tick, so that a page can show
		// why a message is waiting without being told sixty times a second. The
		// choices count as a change of their own: one question answered and
		// replaced by the next never leaves the dialog state, and a page left
		// showing the old one would offer an answer to a question that has gone.
		now, options := live.State(), queue.Options()
		if now != was || !slices.Equal(options, asked) {
			was, asked = now, options
			live.Announce(event{
				Type: "state", State: now.String(),
				Options: options, Pending: len(queue.Pending()),
			})
		}

		m, err := queue.Tick()
		if err != nil || m == nil {
			continue
		}
		live.Announce(event{
			Type: "sent", From: m.Nickname, Text: m.Text,
			Pending: len(queue.Pending()), State: live.State().String(),
		})
	}
}

// event is what a page is told about the queue. The screen shows what the agent
// is doing; this is what kolo is doing with what people said to it.
type event struct {
	Type    string          `json:"type"`
	From    string          `json:"from,omitempty"`
	Text    string          `json:"text,omitempty"`
	Pending int             `json:"pending"`
	State   string          `json:"state,omitempty"`
	Options []detect.Option `json:"options,omitempty"`
}

// wait watches one agent and starts it again when it goes.
func (a *Agents) wait(name string, started *agent.Agent, closeScreen context.CancelFunc) {
	err := started.Wait()
	// This process\'s screen ends with it. A restart makes a new one, and
	// whoever is watching is repainted from that rather than from the last
	// picture of something that has gone.
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
	if time.Since(p.started) > shortRun {
		p.fails = 0
	} else {
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

	a.report(name, hub.StatusStarting, reasonFor(err, "the agent exited"))
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
	if ok {
		// Set before the kill, so the restart in wait sees it and does not race
		// the stop it is about to observe.
		p.stopping = true
	}
	a.mu.Unlock()

	if ok && p.agent != nil {
		p.agent.Close()
	}
	if ok && p.agent == nil {
		// Killed between being recorded and being started; nothing will call
		// wait, so it is forgotten here.
		a.forget(name)
		a.save()
	}
}

// StopAll ends every agent, for when the host itself is going away. What was
// running stays in the state file: the org asked for these, and the machine
// coming back should bring them with it.
func (a *Agents) StopAll() {
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()

	for _, name := range a.Names() {
		a.Stop(name)
	}
}

// Specs is what this machine is running, for telling a hub that has just met it
// — or met it again after the connection dropped.
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
		// One failing to come back does not stop the others. It is reported the
		// moment there is a hub to report it to.
		if err := a.Start(spec); err != nil {
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
// process lives.
//
// It subscribes exactly as a browser does, which is what makes reconnecting
// safe: a subscription opens with a repaint of the screen as it stands, so a hub
// that missed an outage is brought back to the truth rather than left with a
// hole in the middle of an escape sequence.
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

	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()

	for _, m := range backlog {
		if err := sendScreen(ctx, conn, m); err != nil {
			return err
		}
	}
	for m := range updates {
		if err := sendScreen(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

// sendScreen puts terminal output on the wire as binary and anything kolo says
// about it as text, so neither has to be told apart by looking inside.
func sendScreen(ctx context.Context, conn *websocket.Conn, m session.Message) error {
	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, kind, m.Data)
}

// refuse tells an agent's watchers why something they sent went nowhere.
func (a *Agents) refuse(name, reason string) {
	a.mu.Lock()
	p, ok := a.running[name]
	a.mu.Unlock()
	if ok && p.live != nil {
		p.live.Announce(event{Type: "refused", Text: reason})
	}
}
