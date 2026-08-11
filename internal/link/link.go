// Package link is the client half of the hub: it keeps a member's agent
// registered for as long as the agent is running.
//
// The connection is outbound. The agent's machine is never listened to, which
// is what removes the inbound port, the firewall rule and the NAT problem, and
// what makes a hosted hub the same thing as one on a VPS.
package link

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
)

// Config is what a client needs to reach a hub as somebody.
type Config struct {
	// Hub is the base URL, such as https://hub.example.com.
	Hub string
	// Token identifies the member. It is sent in a header, never in the URL.
	Token string
	// Agent, Machine and Version describe this connection. None of them decide
	// who the member is; the token does that.
	Agent   string
	Machine string
	Version string
}

// Event is something worth telling the operator about.
type Event struct {
	// Connected is set when the hub has accepted the connection.
	Connected bool
	// Org and Member are who the hub says we are, on connection.
	Org    string
	Member hub.Person
	// Err is why the connection ended, when it ended badly.
	Err error
	// Retry is how long until the next attempt.
	Retry time.Duration
}

// Run keeps the agent registered until ctx is cancelled.
//
// It does not return an error for a lost connection, because a lost connection
// is not a failure — laptops sleep and wifi drops, and an agent that disappears
// for thirty seconds must come back on its own rather than need restarting.
// Errors are reported through onEvent and then retried.
func Run(ctx context.Context, cfg Config, onEvent func(Event)) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		err := connect(ctx, cfg, func(w welcome) {
			backoff = minBackoff // a connection that worked resets the wait
			onEvent(Event{Connected: true, Org: w.Org, Member: w.Member})
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
		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// connect makes one attempt and returns when the connection ends.
func connect(ctx context.Context, cfg Config, onWelcome func(welcome)) error {
	conn, _, err := websocket.Dial(ctx, wsURL(cfg.Hub)+"/v1/agent", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		return fmt.Errorf("link: dial: %w", err)
	}
	defer conn.CloseNow()

	hello, _ := json.Marshal(map[string]string{
		"type": "hello", "agent": cfg.Agent, "machine": cfg.Machine, "version": cfg.Version,
	})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return fmt.Errorf("link: hello: %w", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("link: welcome: %w", err)
	}
	var w welcome
	if err := json.Unmarshal(data, &w); err != nil || w.Type != "welcome" {
		return fmt.Errorf("link: hub did not welcome us")
	}
	onWelcome(w)

	// Stay until something ends it. Nothing else is exchanged yet, and anything
	// unrecognised is ignored so that a newer hub can talk to an older client.
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return fmt.Errorf("link: connection ended: %w", err)
		}
	}
}

// Presence asks the hub who is connected.
func Presence(ctx context.Context, cfg Config) (string, []hub.Connection, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(cfg.Hub, "/")+"/v1/presence", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("link: reach hub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", nil, fmt.Errorf("link: the hub does not recognise this token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("link: hub answered %s", resp.Status)
	}
	var body struct {
		Org         string           `json:"org"`
		Connections []hub.Connection `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", nil, fmt.Errorf("link: read presence: %w", err)
	}
	return body.Org, body.Connections, nil
}

type welcome struct {
	Type   string     `json:"type"`
	Org    string     `json:"org"`
	Member hub.Person `json:"member"`
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

// jitter spreads retries out, so that everyone whose connection dropped with
// the hub does not come back at the same instant when it returns.
func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
