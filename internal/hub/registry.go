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

// Agent statuses. An agent is Starting from the moment the org asks for it until
// the host says otherwise, so a name is taken and shown immediately rather than
// after a round trip nobody can see.
const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusFailed   = "failed"
)

// maxLabel bounds the strings a client chooses for itself. They are displayed,
// so they are kept to a length that fits a line rather than trusted.
const maxLabel = 64

// Agent is one agent the org has asked a host to run.
//
// The name is the identifier: there is no second id underneath it. One name per
// org means a person can say "the checkups agent" and be understood, and it
// means a URL is readable. Reusing a name means stopping the agent that has it.
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

// HostInfo is a connected host as the org sees it: which directories it lends
// and which commands it will run. The browser needs both to offer a choice that
// will not be refused.
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
// A host that disconnects takes its agents out of the registry with it. They may
// still be running on the far machine, but nothing here can reach them, and a
// list that shows an agent nobody can watch or stop is worse than a short one.
// The host re-announces what it has when it comes back.
type Registry struct {
	mu    sync.Mutex
	hosts map[string]*host
	now   func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{hosts: map[string]*host{}, now: time.Now}
}

// Join records a connected host. A second connection claiming the same host id
// is refused rather than allowed to take over: two processes answering for one
// machine would make every command ambiguous, and the usual cause is a host
// started twice by mistake.
func (r *Registry) Join(id string, dirs, allow []string, send Sender) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.hosts[id]; taken {
		return fmt.Errorf("hub: %s is already connected", id)
	}
	r.hosts[id] = &host{
		info:   HostInfo{ID: id, Dirs: dirs, Allow: allow, Since: r.now()},
		send:   send,
		agents: map[string]*Agent{},
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
// Every rule the host enforces is checked here too. The host checks again, and
// has to: it is the machine running the process and the only place a refusal is
// worth anything. This copy exists so the person creating an agent is told why
// it will not work, in the response to their own request.
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
	// One agent per directory. Resuming a conversation is directory-scoped and
	// two agents editing the same files would collide, so the second one is
	// refused rather than allowed to fight the first.
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

// SetStatus records what a host says became of an agent.
func (r *Registry) SetStatus(name, status, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, h := r.find(name); h != nil {
		a.Status, a.Error = status, reason
	}
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

// ValidName reports whether a name is one an agent may have.
//
// It ends up in a URL, in a log line and in a sentence somebody says out loud,
// so it is kept to the shape all three can carry.
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

// label reduces a string a client chose for itself to something safe to print.
// A control character here would be an escape sequence rendered in somebody
// else's terminal.
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
