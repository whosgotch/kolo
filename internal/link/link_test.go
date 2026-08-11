package link

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/hub"
)

// fixture starts a real hub and returns a config that reaches it as its one
// member. The two halves are tested against each other rather than against a
// stub, because the handshake is the thing worth checking.
func fixture(t *testing.T) (*hub.Server, Config) {
	t.Helper()
	token, hash, err := hub.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &hub.Org{Name: "acme", Members: []hub.Member{{ID: "artem", Name: "Artem", TokenHash: hash}}}

	s, err := hub.Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	return s, Config{
		Hub:     "http://" + s.Addr(),
		Token:   token,
		Agent:   "claude",
		Machine: "artem-mbp",
		Version: "test",
	}
}

// collector gathers events without races.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) add(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) connected() (Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Connected {
			return e, true
		}
	}
	return Event{}, false
}

func (c *collector) failures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Err != nil {
			n++
		}
	}
	return n
}

func TestRunRegisters(t *testing.T) {
	s, cfg := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	go Run(ctx, cfg, got.add)

	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	e, ok := got.connected()
	if !ok {
		t.Fatal("no connection was reported")
	}
	if e.Org != "acme" || e.Member.ID != "artem" {
		t.Errorf("connected as %+v in %q", e.Member, e.Org)
	}
	if got := s.Presence().List()[0]; got.Machine != "artem-mbp" || got.Agent != "claude" {
		t.Errorf("presence = %+v", got)
	}

	cancel()
	waitFor(t, func() bool { return s.Presence().Len() == 0 })
}

// TestRunReconnects is the reason Run exists rather than a single dial: an
// agent whose connection drops has to come back without being restarted.
func TestRunReconnects(t *testing.T) {
	s, cfg := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	go Run(ctx, cfg, got.add)
	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	// Take the hub away and bring it back on the same address.
	addr := s.Addr()
	s.Close()
	waitFor(t, func() bool { return got.failures() > 0 })

	revived, err := hub.Listen(orgOf(t, cfg), addr)
	if err != nil {
		t.Fatal(err)
	}
	go revived.Serve()
	defer revived.Close()

	waitFor(t, func() bool { return revived.Presence().Len() == 1 })
}

// TestRunSurvivesABadToken checks that an agent with the wrong credentials
// keeps trying rather than exiting: the fix is usually at the hub's end, and a
// process that quit cannot notice it was made welcome.
func TestRunSurvivesABadToken(t *testing.T) {
	_, cfg := fixture(t)
	cfg.Token = "kolo_not-a-real-token"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	go Run(ctx, cfg, got.add)

	waitFor(t, func() bool { return got.failures() >= 2 })
	if _, ok := got.connected(); ok {
		t.Error("a bad token connected")
	}
}

func TestPresence(t *testing.T) {
	s, cfg := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, cfg, nil)
	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	org, conns, err := Presence(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if org != "acme" || len(conns) != 1 || conns[0].Member.ID != "artem" {
		t.Errorf("presence = %q %+v", org, conns)
	}
}

func TestPresenceRejectsABadToken(t *testing.T) {
	_, cfg := fixture(t)
	cfg.Token = "kolo_not-a-real-token"

	_, _, err := Presence(context.Background(), cfg)
	if err == nil {
		t.Fatal("a bad token was accepted")
	}
	if !strings.Contains(err.Error(), "does not recognise") {
		t.Errorf("error = %v, want it to say the token is not recognised", err)
	}
}

func TestWSURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://hub.example:8080":   "ws://hub.example:8080",
		"https://hub.example":       "wss://hub.example",
		"https://hub.example/":      "wss://hub.example",
		"http://127.0.0.1:1/nested": "ws://127.0.0.1:1/nested",
	} {
		if got := wsURL(in); got != want {
			t.Errorf("wsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// orgOf rebuilds the org a config belongs to, for restarting a hub in a test.
func orgOf(t *testing.T, cfg Config) *hub.Org {
	t.Helper()
	return &hub.Org{
		Name:    "acme",
		Members: []hub.Member{{ID: "artem", Name: "Artem", TokenHash: hub.HashToken(cfg.Token)}},
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
