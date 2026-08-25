package hub

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Agent status values. Starting lasts until the host reports otherwise.
const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusFailed   = "failed"
)

const maxLabel = 64

// AllowAny is the -allow entry that lends every command on the host's PATH.
// A command under it is still named like one on PATH: program name, no directory.
const AllowAny = "*"

// DirAny is the -dir entry that lends the host's whole filesystem: agents may
// be started in any directory.
const DirAny = "*"

// runs reports whether a host lending allow may be asked to start command:
// one of the command lines it named, or anything on PATH when it lent '*'.
// Syntax only; whether the program exists is checked on the host.
func runs(allow []string, command string) bool {
	if slices.Contains(allow, command) {
		return true
	}
	if !slices.Contains(allow, AllowAny) {
		return false
	}
	name := Program(command)
	return name != "" && filepath.Base(name) == name
}

// Program is the word at the front of a command line, or empty when there is
// none.
func Program(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (h *host) resumesByName(command string) bool {
	return slices.Contains(h.info.ByName, command)
}

// Agent is one agent a host was asked to run. Name is its identifier — it
// addresses the agent in every URL and protocol message, host included, so
// it does not change once picked. Label is a member's own word for it, the
// hub's alone to know: renaming is a label edit, nothing a host needs
// telling or a live connection needs reopening for.
type Agent struct {
	Name      string    `json:"name"`
	Label     string    `json:"label,omitempty"`
	Host      string    `json:"host"`
	Dir       string    `json:"dir"`
	Command   string    `json:"command"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedBy Person    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type HostInfo struct {
	ID    string   `json:"id"`
	Dirs  []string `json:"dirs"`
	Allow []string `json:"allow"`
	// Found is which of the agent kinds kolo has heard of this machine has
	// installed, offered as suggestions when the host lends any command.
	// Suggestion is all it is: nothing here decides what may start.
	Found []string `json:"found,omitempty"`
	// ByName is which of the lent commands resume by naming their
	// conversation rather than asking for the last one. The host's word, not
	// looked up here: what a kind does on restart is its machine's to know,
	// and this is only carried so the one-agent-per-directory rule can bend
	// where it is safe.
	ByName []string  `json:"by_name,omitempty"`
	Since  time.Time `json:"since"`
}

// Sender delivers one command to a host.
type Sender func(any) error

type host struct {
	info   HostInfo
	send   Sender
	agents map[string]*Agent
}

// Registry tracks connected hosts and their agents. A host that disconnects
// takes its agents with it.
type Registry struct {
	mu    sync.Mutex
	hosts map[string]*host
	now   func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{hosts: map[string]*host{}, now: time.Now}
}

// Join records a host and the agents it reports already running. A second
// connection for the same id is refused; names another host claimed are skipped.
func (r *Registry) Join(id string, dirs, allow, found, byName []string, running []Agent, send Sender) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.hosts[id]; taken {
		return fmt.Errorf("hub: %s is already connected", id)
	}
	h := &host{
		info:   HostInfo{ID: id, Dirs: dirs, Allow: allow, Found: found, ByName: byName, Since: r.now()},
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

// Leave drops a host and reports the agents that went out of reach with it, which
// is news the org is owed: they may still be running, but nothing here can watch
// or stop them.
func (r *Registry) Leave(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	h, ok := r.hosts[id]
	if !ok {
		return nil
	}
	delete(r.hosts, id)
	return slices.Sorted(maps.Keys(h.agents))
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

// Agents lists every reachable agent, oldest first.
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

// Add records an agent and returns its host's sender, for dispatching the
// spawn without holding the lock.
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
	if !slices.Contains(h.info.Dirs, a.Dir) && !slices.Contains(h.info.Dirs, DirAny) {
		return nil, fmt.Errorf("%s does not lend %s", a.Host, a.Dir)
	}
	if !runs(h.info.Allow, a.Command) {
		if slices.Contains(h.info.Allow, AllowAny) && Program(a.Command) != "" {
			return nil, fmt.Errorf("%s runs commands by name — put %s's directory on PATH, or lend it with -allow",
				a.Host, Program(a.Command))
		}
		return nil, fmt.Errorf("%s does not run %s", a.Host, a.Command)
	}
	// One agent of each kind to a directory: a kind resuming "the last
	// conversation here" would come back as its neighbour. Naming or pinning
	// an id proves ownership, so such kinds may share.
	for _, other := range h.agents {
		if other.Dir != a.Dir {
			continue
		}
		sameKind := filepath.Base(Program(a.Command)) == filepath.Base(Program(other.Command))
		if sameKind && !(h.resumesByName(a.Command) && h.resumesByName(other.Command)) {
			return nil, fmt.Errorf("one %s to a directory: two of them asking for \"the last conversation here\" would come back as each other. A second checkout is how the same repo runs in parallel",
				filepath.Base(Program(a.Command)))
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

// SetLabel changes what an agent is called on screen. Name, which the host
// and every open connection address it by, is untouched — this is the hub's
// own field, so nothing beyond the registry needs to hear about it.
func (r *Registry) SetLabel(name, label string) (Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, h := r.find(name)
	if h == nil {
		return Agent{}, fmt.Errorf("no agent called %q", name)
	}
	a.Label = label
	return *a, nil
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

// ValidName reports whether s is a usable agent name in URLs and log lines.
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

// label reduces a client-chosen string to something safe to print; the bound
// is the caller's.
func label(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	for len(out) > max {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}
