package host

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sync"

	"github.com/whosgotch/kolo/internal/agent"
)

// The screen the agents run on. Nobody is at this machine's keyboard, so there
// is no terminal to take a size from and every viewer gets the same one.
const (
	cols = 120
	rows = 40
)

const (
	statusRunning = "running"
	statusFailed  = "failed"
)

// Agents is every agent running on this machine.
type Agents struct {
	dirs  []string
	allow []string

	mu      sync.Mutex
	running map[string]*process

	// reports go to the connection, which may be down. A full buffer drops the
	// oldest news rather than blocking a process that is trying to exit; the
	// hub is told the current state again when the host reconnects.
	reports chan any
}

type process struct {
	agent    *agent.Agent
	dir      string
	stopping bool
}

func NewAgents(dirs, allow []string) *Agents {
	return &Agents{
		dirs:    dirs,
		allow:   allow,
		running: map[string]*process{},
		reports: make(chan any, 64),
	}
}

// Start runs an agent, after checking it is one this machine agreed to run.
func (a *Agents) Start(name, dir, command string) error {
	dir = filepath.Clean(dir)
	if !slices.Contains(a.dirs, dir) {
		return fmt.Errorf("this machine does not lend %s", dir)
	}
	if !slices.Contains(a.allow, command) {
		return fmt.Errorf("this machine does not run %s", command)
	}

	a.mu.Lock()
	if _, taken := a.running[name]; taken {
		a.mu.Unlock()
		return fmt.Errorf("%s is already running here", name)
	}
	for other, p := range a.running {
		if p.dir == dir {
			a.mu.Unlock()
			return fmt.Errorf("%s is already working in %s", other, dir)
		}
	}
	// Reserved before the process exists, so two spawns arriving together cannot
	// both find the name free.
	a.running[name] = &process{dir: dir}
	a.mu.Unlock()

	started, err := agent.Start([]string{command}, dir, cols, rows)
	if err != nil {
		a.forget(name)
		return err
	}

	a.mu.Lock()
	a.running[name].agent = started
	a.mu.Unlock()

	// The PTY has to be read or the agent blocks once its buffer fills. The
	// screen goes to the hub from here next; until then it is drained.
	go io.Copy(io.Discard, started)
	go a.wait(name, started)

	a.reportf(name, statusRunning, "")
	return nil
}

// wait watches one agent and says what became of it.
func (a *Agents) wait(name string, started *agent.Agent) {
	err := started.Wait()

	a.mu.Lock()
	p, ok := a.running[name]
	stopping := ok && p.stopping
	delete(a.running, name)
	a.mu.Unlock()

	if stopping {
		return
	}
	reason := "the agent exited"
	if err != nil {
		reason = err.Error()
	}
	a.reportf(name, statusFailed, reason)
}

// Stop ends an agent. Stopping something that is not running is not an error:
// the caller wanted it gone and it is.
func (a *Agents) Stop(name string) {
	a.mu.Lock()
	p, ok := a.running[name]
	if ok {
		p.stopping = true
	}
	a.mu.Unlock()

	if ok && p.agent != nil {
		p.agent.Close()
	}
}

// StopAll ends every agent, for when the host itself is going away.
func (a *Agents) StopAll() {
	a.mu.Lock()
	names := make([]string, 0, len(a.running))
	for name := range a.running {
		names = append(names, name)
	}
	a.mu.Unlock()

	for _, name := range names {
		a.Stop(name)
	}
}

// Names is every agent running here.
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

func (a *Agents) forget(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.running, name)
}

func (a *Agents) reportf(name, status, reason string) {
	select {
	case a.reports <- map[string]string{"type": "status", "name": name, "status": status, "error": reason}:
	default:
	}
}
