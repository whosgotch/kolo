package hub

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// An agent is Starting from the moment the org asks for it until the host says
// otherwise, so a name is taken and shown without waiting for a round trip.
const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusFailed   = "failed"
)

const maxLabel = 64

// Agent is one agent the org has asked a host to run. The name is the
// identifier: there is no second id underneath it, and reusing a name means
// stopping the agent that has it.
type Agent struct {
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Dir       string    `json:"dir"`
	Command   string    `json:"command"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedBy Person    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// HostInfo is a connected host as the org sees it. The browser needs the
// directories and commands to offer a choice that will not be refused.
type HostInfo struct {
	ID    string    `json:"id"`
	Dirs  []string  `json:"dirs"`
	Allow []string  `json:"allow"`
	Since time.Time `json:"since"`
}

// Sender delivers one command to a host.
type Sender func(any) error

type host struct {
	info   HostInfo
	send   Sender
	agents map[string]*Agent
}

// Registry is every connected host and the agents on them.
//
// A host that disconnects takes its agents with it: they may still be running,
// but nothing here can reach them, and listing an agent nobody can watch or stop
// is worse than a short list. The host re-announces what it has on return.
type Registry struct {
	mu    sync.Mutex
	hosts map[string]*host
	now   func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{hosts: map[string]*host{}, now: time.Now}
}

// Join records a connected host and the agents it says it is already running. A
// second connection claiming the same host id is refused: two processes answering
// for one machine would make every command ambiguous.
//
// The running set is the host's word — it is the machine with the processes, and
// after a dropped connection the only party that knows. A name another host has
// claimed is skipped.
func (r *Registry) Join(id string, dirs, allow []string, running []Agent, send Sender) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.hosts[id]; taken {
		return fmt.Errorf("hub: %s is already connected", id)
	}
	h := &host{
		info:   HostInfo{ID: id, Dirs: dirs, Allow: allow, Since: r.now()},
		send:   send,
		agents: map[string]*Agent{},
	}
	r.hosts[id] = h

	for _, a := range running {
		if _, taken := r.find(a.Name); taken != nil {
			continue
		}
		a.Host = id
		h.agents[a.Name] = &a
	}
	return nil
}

func (r *Registry) Leave(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hosts, id)
}

func (r *Registry) Hosts() []HostInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]HostInfo, 0, len(r.hosts))
	for _, h := range r.hosts {
		out = append(out, h.info)
	}
	slices.SortFunc(out, func(a, b HostInfo) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// Agents lists every agent the org can currently reach, oldest first.
func (r *Registry) Agents() []Agent {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Agent
	for _, h := range r.hosts {
		for _, a := range h.agents {
			out = append(out, *a)
		}
	}
	slices.SortFunc(out, func(a, b Agent) int {
		if n := a.CreatedAt.Compare(b.CreatedAt); n != 0 {
			return n
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func (r *Registry) Agent(name string) (Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, h := r.find(name); h != nil {
		return *a, true
	}
	return Agent{}, false
}

// Add records an agent and returns the host's sender, so the caller can dispatch
// the spawn without holding the lock.
//
// Every rule the host enforces is checked here too, so the person creating an
// agent is told why it will not work. The host's own check is the one that counts.
func (r *Registry) Add(a Agent) (Sender, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	h, ok := r.hosts[a.Host]
	if !ok {
		return nil, fmt.Errorf("no host called %q is connected", a.Host)
	}
	if _, taken := r.find(a.Name); taken != nil {
		return nil, fmt.Errorf("an agent called %q already exists", a.Name)
	}
	if !slices.Contains(h.info.Dirs, a.Dir) {
		return nil, fmt.Errorf("%s does not lend %s", a.Host, a.Dir)
	}
	if !slices.Contains(h.info.Allow, a.Command) {
		return nil, fmt.Errorf("%s does not run %s", a.Host, a.Command)
	}
	// One agent per directory: resuming is directory-scoped, and two agents
	// editing the same files would collide.
	for _, other := range h.agents {
		if other.Dir == a.Dir {
			return nil, fmt.Errorf("%s is already working in %s", other.Name, a.Dir)
		}
	}

	a.CreatedAt = r.now()
	a.Status = StatusStarting
	h.agents[a.Name] = &a
	return h.send, nil
}

func (r *Registry) SetStatus(name, status, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, h := r.find(name); h != nil {
		a.Status, a.Error = status, reason
	}
}

func (r *Registry) Sender(name string) (Sender, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, h := r.find(name); h != nil {
		return h.send, true
	}
	return nil, false
}

// Remove forgets an agent and returns its host's sender.
func (r *Registry) Remove(name string) (Sender, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, h := r.find(name)
	if h == nil {
		return nil, false
	}
	delete(h.agents, a.Name)
	return h.send, true
}

// find locates an agent by name across every host. Callers must hold r.mu.
func (r *Registry) find(name string) (*Agent, *host) {
	for _, h := range r.hosts {
		if a, ok := h.agents[name]; ok {
			return a, h
		}
	}
	return nil, nil
}

// ValidName reports whether a name is one an agent may have. It ends up in a
// URL, a log line and a sentence said out loud, so it is kept to a shape all
// three can carry.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

// label reduces a string a client chose for itself to something safe to print: a
// control character here is an escape sequence in somebody else's terminal.
func label(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	for len(out) > maxLabel {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}
