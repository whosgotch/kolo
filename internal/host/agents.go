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

	// One writer to the state file at a time. It is written from whichever
	// goroutine noticed the change — an agent starting, one exiting, one saying
	// which conversation it is in — and two of those overlapping leaves a file
	// that is neither.
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
	// The next launch must not resume: the agent is new, or the org asked for a
	// clean start. Every other launch resumes.
	fresh bool
	// Whether this run was launched with the resume command, so a run that dies
	// at once can be read as the resume failing.
	resumed bool
	// An exit the org asked for, which must not count towards giving up.
	bounced bool
	// The conversation this agent is in, for a kind whose resume command names
	// one. Read off the agent's own screen and kept across restarts, because a
	// process that has gone cannot be asked what it was doing.
	session string
	// How this agent's screen has been reading, and since when. Kept for kolo
	// doctor: an agent whose screen has said nothing kolo understands for three
	// days has markers that do not fit it, and nothing else would ever say so.
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

// runs reports whether the org may start command here: one of the command lines
// lent with -allow, or anything on PATH when the host lent '*'. The hub checks
// the same rule as syntax; this one looks, because it is the machine with the
// PATH, and it is the one that counts.
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

// resumesByName says whether this kind names its conversation on restart, and
// so may share a directory with its own kind of caution.
func resumesByName(command string) bool {
	return adapter.For(command).ResumesByName()
}

// soleIn reports whether name is this machine's only agent working in dir. It
// is consulted while deciding what an agent may be asked for: "the last
// conversation in this directory" names something only when there is one
// agent here to own it. The caller holds the lock.
func (a *Agents) soleIn(dir, name string) bool {
	for other, p := range a.running {
		if other != name && p.spec.Dir == dir {
			return false
		}
	}
	return true
}

// newSessionID mints a conversation identity: a random v4 UUID, because that
// is the shape agents expect an id to arrive in.
func newSessionID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crypto/rand never fails on Linux, but an identity is not worth a
		// panic; an agent without one starts fresh and says so.
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// baseOf is the program a command runs, for speaking about it to people. An
// empty command has no program, but it never gets this far.
func baseOf(command string) string {
	if argv := adapter.Argv(command); len(argv) > 0 {
		return filepath.Base(argv[0])
	}
	return command
}

// reserve checks the rules and takes the name, before any process exists.
// Launching waits until every agent being brought back has claimed its place,
// so one restored beside another knows it is not alone in its directory.
func (a *Agents) reserve(spec hub.Agent, fresh bool, session string) error {
	spec.Dir = filepath.Clean(spec.Dir)
	if !slices.Contains(a.cfg.Dirs, spec.Dir) {
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
	// One agent of each kind to a directory: the same rule the hub checks,
	// from the machine that actually knows. Two of one kind that ask for "the
	// last conversation here" would come back as each other; different kinds
	// never read one another's history.
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
	// Reserved before the process exists, so two spawns arriving together cannot
	// both find the name free.
	a.running[spec.Name] = &process{spec: spec, fresh: fresh, session: session}
	a.mu.Unlock()
	return nil
}

// begin launches an agent already reserved, and says so. A launch that failed
// releases the name again.
func (a *Agents) begin(name string) error {
	if err := a.launch(name); err != nil {
		a.forget(name)
		return err
	}
	a.save()
	a.report(name, hub.StatusRunning, "")
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
	spec, was := p.spec, p.session
	kind := adapter.For(spec.Command)
	// The resume flag goes after the arguments the host lent the command with,
	// so an agent comes back the way it was started rather than the way kolo
	// would have started it.
	argv, resumed := adapter.Argv(spec.Command), false
	switch {
	case p.fresh && len(kind.Pin) > 0:
		// A first start of a kind kolo pins: mint the identity here, so this
		// conversation belongs to this agent whatever else runs beside it.
		id := newSessionID()
		if args, ok := kind.PinArgs(id); ok {
			p.session = id
			argv = append(argv, args...)
		}
	case !p.fresh:
		if r, ok := kind.ResumeArgs(was); ok {
			argv, resumed = append(argv, r...), true
		} else if len(kind.Continue) > 0 && a.soleIn(spec.Dir, name) {
			// No id names this agent's conversation — it predates identities,
			// or the state went missing. "The last conversation here" is only
			// safe to ask for while nobody else works here; beside a neighbour
			// it would come back as that neighbour.
			argv = append(argv, kind.Continue...)
		}
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
	live := session.New(cols, rows, kind.Markers)
	// Input reads the same screen the hub is shown, so a member stopping the
	// agent is stopping the one everybody else is looking at.
	input := relay.New(started, live.Screen, kind)
	screen, closeScreen := context.WithCancel(context.Background())
	p.agent, p.started, p.live, p.input = started, time.Now(), live, input
	p.resumed, p.fresh, p.bounced = resumed, false, false
	// From the launch rather than from the first change: an agent whose screen
	// never says anything kolo understands would otherwise have been unknown
	// since the beginning of time, which is the one case worth noticing.
	p.state, p.since = detect.Unknown, time.Now()
	a.mu.Unlock()

	// The PTY has to be read or the agent blocks once its buffer fills. Reading
	// it into the session is also what keeps the screen current.
	go io.Copy(live, started)
	go a.stream(screen, name, live)
	go a.watch(screen, name, kind, live)
	go a.wait(name, started, closeScreen)
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
		// The id goes with it. Otherwise the process dying before the new one has
		// said what it is now would bring back the conversation somebody just
		// asked to be rid of.
		p.fresh, p.session = true, ""
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
	v.live.Announce(e)
}

// watch tells everybody what the agent's screen has become, and takes down the
// conversation it says it is in. Polling, because a screen arriving at a
// particular arrangement has no event to subscribe to.
//
// What it says is that the agent is asking something, never what it is asking:
// the question belongs to the agent, and to whoever takes its keyboard.
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

		// On change rather than every tick.
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

// reading records how an agent's screen is being read, and from when. Written
// down for the same reason the conversation is: the process that knew goes away,
// and a machine coming back should be able to say what was happening.
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

// remember records the conversation an agent says it is in, so a restart can ask
// for that one by name. Written down when it changes, because the state file is
// what survives the machine going away mid-conversation.
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

// event is what a page is told about an agent. The screen shows what the agent
// is doing; this is what kolo did to it, and who did it.
type event struct {
	Type  string `json:"type"`
	From  string `json:"from,omitempty"`
	Text  string `json:"text,omitempty"`
	State string `json:"state,omitempty"`
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
		// clean and saying so beats losing the context silently. The id goes with
		// it: it is the one that was just refused.
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

// State is what this machine remembers about its own work: what it was lent for,
// and what it was running.
//
// Written by the host and read by kolo doctor, which is why it holds what the
// machine was started with as well as what it is doing. A diagnostic that has to
// be handed the same flags kolo up was given is one nobody runs.
type State struct {
	Lends  []string `json:"lends,omitempty"`
	Allows []string `json:"allows,omitempty"`
	Agents []Record `json:"agents"`
}

// Record is one agent as this machine remembers it: what the org asked for, the
// conversation it was in, and how its screen has been reading.
//
// The last two are written down here and nowhere else — the hub lists agents and
// has no use for either, and the machine that ran the process is the only party
// that saw them.
type Record struct {
	Spec    hub.Agent `json:"spec"`
	Session string    `json:"session,omitempty"`
	// State is idle, busy, dialog or unknown, as the words detect uses.
	State string    `json:"state,omitempty"`
	Since time.Time `json:"since,omitzero"`
}

// ReadState is what a machine last wrote down about itself. A machine that has
// never run anything has nothing to say, which is not an error.
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
		// One failing to come back does not stop the others. These agents were
		// mid-life when the machine went, so they resume rather than start
		// fresh. All of them claim their place before any of them launches.
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
	// Written whole and renamed into place, as the org file is: this is what a
	// machine reads to find out what it was running, and a half-written one is a
	// machine that was running nothing.
	tmp := a.state + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, a.state); err != nil {
		os.Remove(tmp)
	}
}

// records is what to write down, oldest first, so a machine coming back brings
// its agents back in the order the org made them.
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

	// The markers travel with the screen they describe: this machine is the one
	// that knows what kind of agent is drawing it, and a hub told the kind's name
	// could only look it up in a table that has to agree with this one.
	hello, _ := json.Marshal(map[string]any{
		"type": "screen", "cols": cols, "rows": rows, "markers": live.Markers(),
	})
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
