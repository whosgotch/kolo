// Package host is the machine half of kolo: it lends a machine to the org
// and runs the agents the org asks for.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/hub"
	"github.com/whosgotch/kolo/internal/relay"
	"github.com/whosgotch/kolo/internal/session"
)

const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second

	writeTimeout = 10 * time.Second

	// coder/websocket reads 32 KiB by default. What comes down this socket is
	// the org's commands, and a pasted prompt rides in one of them.
	controlLimit = 1 << 20

	// How a hub that went away without closing anything is noticed.
	pingEvery  = 20 * time.Second
	pingWithin = 10 * time.Second
)

// Config is what a machine needs to reach a hub as itself.
type Config struct {
	Hub   string
	Token string
	// Absolute paths.
	Dirs    []string
	Allow   []string
	Version string
}

// Event is something worth telling the operator about.
type Event struct {
	Connected bool
	Org, Host string
	Err       error
	Retry     time.Duration
}

// Run keeps the machine connected until ctx is cancelled. Lost connections
// are retried, not failed; agents keep running while the hub is away.
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
	conn.SetReadLimit(controlLimit)

	// The hello carries what's running, so a reconnect puts its agents back
	// on the hub's list.
	byName := make([]string, 0, len(cfg.Allow))
	for _, command := range cfg.Allow {
		if adapter.For(command).ResumesByName() {
			byName = append(byName, command)
		}
	}
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "dirs": cfg.Dirs, "allow": cfg.Allow,
		"found": adapter.Discovered(), "by_name": byName,
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

	// Commands come down while reports go up, neither waiting behind the other.
	failed := make(chan error, 2)
	go func() { failed <- obey(ctx, conn, agents) }()
	go func() { failed <- report(ctx, conn, agents) }()
	// A hub that went away without closing anything is otherwise never
	// noticed: nothing is expected of it while the org is quiet, so this
	// machine would sit lending itself to nobody.
	go func() {
		defer cancel()
		session.Keepalive(ctx, conn, pingEvery, pingWithin)
	}()
	return <-failed
}

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
			// The refusal that counts: this machine runs the process. See
			// hub.Registry.Add for the hub's copy of the checks.
			if err := agents.Start(c.Agent); err != nil {
				agents.report(c.Agent.Name, hub.StatusFailed, err.Error())
			}
		case "stop":
			agents.Stop(c.Name)
		case "keys":
			// A keystroke arriving just after the agent stopped isn't worth a
			// line on everybody's screen, so that stays silent. A refused
			// paste is worth one: somebody meant to send it, and without this
			// it disappears with nothing said anywhere.
			if err := agents.Type(c.Name, c.Keys); errors.Is(err, relay.ErrTooMuch) {
				agents.refuse(c.Name, err.Error())
			}
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
	Type  string    `json:"type"`
	Name  string    `json:"name"`
	From  string    `json:"from"`
	Keys  string    `json:"keys"`
	Cols  int       `json:"cols"`
	Rows  int       `json:"rows"`
	Agent hub.Agent `json:"agent"`
}

// wsURL turns a base URL into a websocket one, so https is never silently
// connected to in the clear.
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

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
