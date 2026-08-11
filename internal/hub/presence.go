package hub

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxLabel bounds the strings a client chooses for itself. They are displayed,
// so they are kept to a length that fits a line rather than trusted.
const maxLabel = 64

// Connection is one connected agent.
//
// Presence is keyed by connection rather than by member, because one person is
// routinely in two places at once — a laptop and a desktop, or the same machine
// after a reconnect that overlapped the drop. Collapsing those into "artem is
// online" would make leaving ambiguous: one machine going away must not take the
// other's entry with it.
type Connection struct {
	// ID is unique to this connection for as long as the hub runs.
	ID int64 `json:"id"`
	// Member is who the hub decided this is, from their token. It is never
	// taken from anything the client said about itself.
	Member Member `json:"member"`
	// Agent and Machine are what the client calls itself.
	Agent   string    `json:"agent"`
	Machine string    `json:"machine"`
	Since   time.Time `json:"since"`
}

// Presence is who is connected right now.
//
// It is deliberately not persisted. Presence is a fact about the present, and a
// hub restart means nobody is connected until they dial back — which is true,
// rather than a stale list to reconcile.
type Presence struct {
	mu    sync.Mutex
	next  int64
	conns map[int64]Connection
	now   func() time.Time
}

// NewPresence returns an empty Presence.
func NewPresence() *Presence {
	return &Presence{conns: map[int64]Connection{}, now: time.Now}
}

// Join records a connected agent and returns its entry. The caller is expected
// to Leave with the returned ID when the connection ends, whatever the reason.
func (p *Presence) Join(m Member, agent, machine string) Connection {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.next++
	c := Connection{
		ID:      p.next,
		Member:  m,
		Agent:   label(agent),
		Machine: label(machine),
		Since:   p.now(),
	}
	p.conns[c.ID] = c
	return c
}

// Leave removes a connection. Leaving twice, or leaving something that was never
// there, is not an error: a connection can end in more than one way and the
// paths that clean up should not have to agree on which of them got there first.
func (p *Presence) Leave(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, id)
}

// List returns everyone connected, in a stable order: by member, then by how
// long they have been connected. Stable because people read this output and
// compare it between runs.
func (p *Presence) List() []Connection {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Connection, 0, len(p.conns))
	for _, c := range p.conns {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Connection) int {
		if n := cmp.Compare(a.Member.ID, b.Member.ID); n != 0 {
			return n
		}
		if n := a.Since.Compare(b.Since); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// Len is how many agents are connected.
func (p *Presence) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// label reduces a name a client chose for itself to something safe to print.
//
// These strings are shown in a terminal by kolo who. A member is authenticated,
// but authenticated is not the same as trusted with the cursor: a control
// character here would be an escape sequence rendered in someone else's
// terminal.
func label(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	for len(out) > maxLabel {
		// Cut on a rune boundary, not a byte one.
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if out == "" {
		return "unknown"
	}
	return out
}
