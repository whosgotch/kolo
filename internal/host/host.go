// Package host is the machine half of kolo: it lends a machine to the org and
// runs the agents the org asks for on it.
//
// The connection is outbound. The machine is never listened to, which is what
// removes the inbound port, the firewall rule and the NAT problem, and what makes
// a hosted hub the same thing as one on a VPS.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/hub"
)

// Backoff bounds. A dropped wifi connection comes back in seconds, so the first
// retry is quick; a hub that is down stays down for a while, so the wait grows
// rather than hammering it.
const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second

	writeTimeout = 10 * time.Second
)

// Config is what a machine needs to reach a hub as itself.
type Config struct {
	// Hub is the base URL, such as https://hub.example.com.
	Hub string
	// Token identifies the machine. It is sent in a header, never in the URL.
	Token string
	// Dirs are the directories this machine lends. Absolute paths.
	Dirs []string
	// Allow are the commands it will run.
	Allow   []string
	Version string
}

// Event is something worth telling the operator about.
type Event struct {
	Connected bool
	Org, Host string
	Err       error
	Retry     time.Duration
	Note      string
}

// Run keeps the machine connected until ctx is cancelled.
//
// A lost connection is not a failure — machines sleep and wifi drops — so it is
// reported and retried rather than returned. Agents already running keep running
// while the hub is away; the connection is how the org reaches them, not what
// holds them up.
func Run(ctx context.Context, cfg Config, agents *Agents, onEvent func(Event)) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		err := connect(ctx, cfg, agents, func(w welcome) {
			backoff = minBackoff
			onEvent(Event{Connected: true, Org: w.Org, Host: w.Host})
		})
		if ctx.Err() != nil {
			return
		}
		wait := jitter(backoff)
		onEvent(Event{Err: err, Retry: wait})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func connect(ctx context.Context, cfg Config, agents *Agents, onWelcome func(welcome)) error {
	conn, _, err := websocket.Dial(ctx, wsURL(cfg.Hub)+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		return fmt.Errorf("host: dial: %w", err)
	}
	defer conn.CloseNow()

	// The hello carries what is already running. A host whose connection dropped
	// kept its agents; the hub lost them, because an agent it cannot reach is one
	// it should not be listing. This is what puts them back.
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "dirs": cfg.Dirs, "allow": cfg.Allow,
		"agents": agents.Specs(), "version": cfg.Version,
	})
	if err := send(ctx, conn, hello); err != nil {
		return fmt.Errorf("host: hello: %w", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("host: welcome: %w", err)
	}
	var w welcome
	if err := json.Unmarshal(data, &w); err != nil || w.Type != "welcome" {
		return fmt.Errorf("host: the hub did not welcome us")
	}
	onWelcome(w)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Commands come down while reports go up, and neither waits behind the other.
	failed := make(chan error, 2)
	go func() { failed <- obey(ctx, conn, agents) }()
	go func() { failed <- report(ctx, conn, agents) }()
	return <-failed
}

// obey runs what the hub asks for.
func obey(ctx context.Context, conn *websocket.Conn, agents *Agents) error {
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("host: connection ended: %w", err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var c command
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		switch c.Type {
		case "spawn":
			// Checked here as well as at the hub. This is the machine that will
			// run the process, so this is the only refusal that is worth
			// anything: the hub's copy of the rules exists to give a person a
			// reason, not to be relied on.
			if err := agents.Start(c.Agent); err != nil {
				agents.report(c.Agent.Name, hub.StatusFailed, err.Error())
			}
		case "stop":
			agents.Stop(c.Name)
		}
	}
}

// report carries what became of each agent up to the hub.
func report(ctx context.Context, conn *websocket.Conn, agents *Agents) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-agents.reports:
			b, err := json.Marshal(r)
			if err != nil {
				continue
			}
			if err := send(ctx, conn, b); err != nil {
				return err
			}
		}
	}
}

func send(ctx context.Context, conn *websocket.Conn, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

type welcome struct {
	Type string `json:"type"`
	Org  string `json:"org"`
	Host string `json:"host"`
}

type command struct {
	Type  string    `json:"type"`
	Name  string    `json:"name"`
	Agent hub.Agent `json:"agent"`
}

// wsURL turns a hub's base URL into a websocket one, so that a hub reached over
// HTTPS is not silently connected to in the clear.
func wsURL(base string) string {
	base = strings.TrimSuffix(base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}

// jitter spreads retries out, so that every host that dropped does not come back
// at the same instant when the hub returns.
func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
