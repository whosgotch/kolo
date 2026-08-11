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
	"github.com/whosgotch/kolo/internal/session"
)

// Backoff bounds. A dropped wifi connection comes back in seconds, so the first
// retry is quick; a hub that is down stays down for a while, so the wait grows
// rather than hammering it.
const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second

	// writeTimeout bounds one send, so a connection that has stopped
	// acknowledging cannot hold the agent's screen hostage.
	writeTimeout = 10 * time.Second
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

// Handlers are what a caller wants to know about.
type Handlers struct {
	// Event reports connections and the failures between them. Optional.
	Event func(Event)
	// Message is a line somebody in the org sent to this agent. Who they are
	// was decided by the hub from their credentials, not typed by them, so the
	// name here can be trusted in a way a self-declared nickname cannot.
	//
	// It is not a command to type: it goes to the queue, which releases it only
	// when the agent's own screen says a line may be sent.
	Message func(from, text string)
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
func Run(ctx context.Context, cfg Config, live *session.Session, on Handlers) {
	if on.Event == nil {
		on.Event = func(Event) {}
	}
	if on.Message == nil {
		on.Message = func(string, string) {}
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		err := connect(ctx, cfg, live, on, func(w welcome) {
			backoff = minBackoff // a connection that worked resets the wait
			on.Event(Event{Connected: true, Org: w.Org, Member: w.Member})
		})
		if ctx.Err() != nil {
			return
		}

		wait := jitter(backoff)
		on.Event(Event{Err: err, Retry: wait})
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
func connect(ctx context.Context, cfg Config, live *session.Session, on Handlers, onWelcome func(welcome)) error {
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reading and writing run at once: the org sends messages down while the
	// agent's screen goes up, and neither should wait behind the other.
	failed := make(chan error, 2)
	go func() { failed <- read(ctx, conn, on) }()
	go func() { failed <- stream(ctx, conn, live) }()
	return <-failed
}

// read takes what the org sends to this agent.
func read(ctx context.Context, conn *websocket.Conn, on Handlers) error {
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("link: connection ended: %w", err)
		}
		// Bytes are the agent's language, not the org's. Nothing arriving from
		// the hub is ever passed through to the agent as bytes.
		if kind != websocket.MessageText {
			continue
		}
		var m inbound
		// Anything unrecognised is ignored, so a newer hub can talk to an older
		// client without either breaking.
		if err := json.Unmarshal(data, &m); err != nil || m.Type != "message" {
			continue
		}
		on.Message(m.From, m.Text)
	}
}

// stream sends the agent's screen to the org for as long as the connection
// lasts.
//
// It subscribes the same way a browser does, which is what makes reconnecting
// safe: the subscription begins with a repaint of the screen as it stands, so a
// hub that missed everything said during an outage is brought back to the truth
// rather than left with a hole in the middle of an escape sequence.
func stream(ctx context.Context, conn *websocket.Conn, live *session.Session) error {
	if live == nil {
		<-ctx.Done() // nothing to send; the read side decides when this ends
		return ctx.Err()
	}
	backlog, updates, unsubscribe := live.Subscribe()
	defer unsubscribe()

	for _, m := range backlog {
		if err := forward(ctx, conn, m); err != nil {
			return err
		}
	}
	for m := range updates {
		if err := forward(ctx, conn, m); err != nil {
			return err
		}
	}
	return fmt.Errorf("link: the agent's screen stopped")
}

func forward(ctx context.Context, conn *websocket.Conn, m session.Message) error {
	kind := websocket.MessageBinary
	if m.Control {
		kind = websocket.MessageText
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, kind, m.Data)
}

// inbound is what the hub sends down to an agent.
type inbound struct {
	Type string `json:"type"`
	From string `json:"from"`
	Text string `json:"text"`
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
