// Package host is the machine half of kolo: it lends a machine to the org and
// runs the agents the org asks for on it.
//
// The connection is outbound and the machine is never listened to, which removes
// the inbound port, the firewall rule and the NAT problem.
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

// Dropped wifi comes back in seconds, so the first retry is quick; a hub that is
// down stays down, so the wait grows rather than hammering it.
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
// reported and retried. Agents keep running while the hub is away: the connection
// is how the org reaches them, not what holds them up.
func Run(ctx context.Context, agents *Agents, onEvent func(Event)) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		err := connect(ctx, agents.cfg, agents, func(w welcome) {
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

	// The hello carries what is already running. A host that dropped kept its
	// agents while the hub let them go, and this is what puts them back.
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
			// The refusal that counts, this being the machine that runs the
			// process. See hub.Registry.Add for the hub's copy of the checks.
			if err := agents.Start(c.Agent); err != nil {
				agents.report(c.Agent.Name, hub.StatusFailed, err.Error())
			}
		case "stop":
			agents.Stop(c.Name)
		case "answer":
			if err := agents.Answer(c.Name, c.From, c.Choice, c.Label); err != nil {
				agents.refuse(c.Name, err.Error())
			}
		case "keys":
			// Silently: a keystroke that arrives a moment after the agent stopped
			// is not worth a line on everybody's screen.
			agents.Type(c.Name, c.Keys)
		case "resize":
			agents.Resize(c.Name, c.Cols, c.Rows)
		case "interrupt":
			if err := agents.Interrupt(c.Name, c.From); err != nil {
				agents.refuse(c.Name, err.Error())
			}
		case "restart":
			if err := agents.Restart(c.Name, c.From); err != nil {
				agents.refuse(c.Name, err.Error())
			}
		case "fresh":
			if err := agents.Fresh(c.Name, c.From); err != nil {
				agents.refuse(c.Name, err.Error())
			}
		}
	}
}

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
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	From   string    `json:"from"`
	Choice int       `json:"choice"`
	Label  string    `json:"label"`
	Keys   string    `json:"keys"`
	Cols   int       `json:"cols"`
	Rows   int       `json:"rows"`
	Agent  hub.Agent `json:"agent"`
}

// wsURL turns a hub's base URL into a websocket one, so a hub reached over HTTPS
// is not silently connected to in the clear.
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

// jitter spreads retries out, so hosts that dropped together do not all come back
// at the same instant.
func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
