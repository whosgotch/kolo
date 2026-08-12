package host

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/hub"
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

// Agents is every agent running on this machine.
type Agents struct {
	dirs  []string
	allow []string
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
	started  time.Time
	stopping bool
	fails    int
}

func NewAgents(dirs, allow []string, state string) *Agents {
	return &Agents{
		dirs:    dirs,
		allow:   allow,
		state:   state,
		running: map[string]*process{},
		reports: make(chan any, 64),
	}
}

// Start runs an agent, after checking it is one this machine agreed to run.
func (a *Agents) Start(spec hub.Agent) error {
	spec.Dir = filepath.Clean(spec.Dir)
	if !slices.Contains(a.dirs, spec.Dir) {
		return fmt.Errorf("this machine does not lend %s", spec.Dir)
	}
	if !slices.Contains(a.allow, spec.Command) {
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
	p.agent, p.started = started, time.Now()
	a.mu.Unlock()

	// The PTY has to be read or the agent blocks once its buffer fills. The
	// screen goes to the hub from here next; until then it is drained.
	go io.Copy(io.Discard, started)
	go a.wait(name, started)
	return nil
}

// wait watches one agent and starts it again when it goes.
func (a *Agents) wait(name string, started *agent.Agent) {
	err := started.Wait()

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
