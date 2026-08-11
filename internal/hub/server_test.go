package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// hubFixture starts a hub with one member and returns it with that member's
// token.
func hubFixture(t *testing.T) (*Server, string) {
	t.Helper()
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", Name: "Artem", TokenHash: hash}}}

	s, err := Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, token
}

// join dials the hub as an agent and completes the handshake.
func join(t *testing.T, ctx context.Context, s *Server, token, machine string) (*websocket.Conn, welcome) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"hello","agent":"claude","machine":"`+machine+`","version":"test"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	var w welcome
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	return conn, w
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAgentJoins(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	_, w := join(t, ctx, s, token, "artem-mbp")
	if w.Type != "welcome" || w.Org != "acme" || w.Member.ID != "artem" {
		t.Errorf("welcome = %+v", w)
	}
	if w.Connection == 0 {
		t.Error("welcome carries no connection id")
	}

	waitFor(t, func() bool { return s.Presence().Len() == 1 })
	got := s.Presence().List()[0]
	if got.Member.ID != "artem" || got.Machine != "artem-mbp" {
		t.Errorf("presence = %+v", got)
	}
}

// TestBadTokensNeverGetAWebsocket is the one that matters. Authentication
// happens before the upgrade, so a caller without a valid token gets 401 and no
// socket at all.
func TestBadTokensNeverGetAWebsocket(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	other, _, _ := NewToken()
	for name, header := range map[string]string{
		"no header":       "",
		"empty bearer":    "Bearer ",
		"wrong token":     "Bearer " + other,
		"not bearer":      token,
		"basic auth":      "Basic " + token,
		"token truncated": "Bearer " + token[:len(token)-1],
	} {
		t.Run(name, func(t *testing.T) {
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
			if header != "" {
				opts.HTTPHeader.Set("Authorization", header)
			}
			conn, resp, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent", opts)
			if err == nil {
				conn.CloseNow()
				t.Fatalf("%s was allowed to connect", name)
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %v, want 401", resp)
			}
		})
	}
	if s.Presence().Len() != 0 {
		t.Errorf("a refused caller reached presence: %+v", s.Presence().List())
	}
}

// TestLeavingRemovesPresence covers the whole point of presence: it has to
// follow reality when a connection ends, however it ends.
func TestLeavingRemovesPresence(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	conn, _ := join(t, ctx, s, token, "artem-mbp")
	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	conn.CloseNow()
	waitFor(t, func() bool { return s.Presence().Len() == 0 })
}

func TestTwoMachinesOneMember(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	join(t, ctx, s, token, "laptop")
	join(t, ctx, s, token, "desktop")

	waitFor(t, func() bool { return s.Presence().Len() == 2 })
	machines := []string{s.Presence().List()[0].Machine, s.Presence().List()[1].Machine}
	if !strings.Contains(strings.Join(machines, " "), "laptop") ||
		!strings.Contains(strings.Join(machines, " "), "desktop") {
		t.Errorf("machines = %v", machines)
	}
}

// TestSilentConnectionIsDropped covers a socket that opens and says nothing.
func TestSilentConnectionIsDropped(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	// It never says hello, so it is never present.
	time.Sleep(200 * time.Millisecond)
	if s.Presence().Len() != 0 {
		t.Errorf("a connection that said nothing was recorded as present")
	}
}

func TestFirstMessageMustBeHello(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)

	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"something-else"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Error("a connection that did not say hello was kept")
	}
	if s.Presence().Len() != 0 {
		t.Error("a connection that did not say hello was recorded as present")
	}
}

func TestPresenceEndpoint(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)
	join(t, ctx, s, token, "artem-mbp")
	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/presence", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body presenceResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Org != "acme" || len(body.Connections) != 1 {
		t.Fatalf("presence = %+v", body)
	}
	if body.Connections[0].Member.ID != "artem" {
		t.Errorf("connection = %+v", body.Connections[0])
	}
}

// TestPresenceNeverCarriesASecret is why Connection holds a Person. A response
// is read by every member, and none of them should be able to read anyone's
// credentials out of it.
func TestPresenceNeverCarriesASecret(t *testing.T) {
	s, token := hubFixture(t)
	ctx := testContext(t)
	join(t, ctx, s, token, "artem-mbp")
	waitFor(t, func() bool { return s.Presence().Len() == 1 })

	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/presence", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if strings.Contains(body, s.org.Members[0].TokenHash) {
		t.Error("the response carries a token hash")
	}
	if strings.Contains(body, "token") {
		t.Errorf("the response mentions a token: %s", body)
	}
}

func TestPresenceEndpointNeedsAToken(t *testing.T) {
	s, _ := hubFixture(t)

	resp, err := http.Get("http://" + s.Addr() + "/v1/presence")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// waitFor polls until cond holds, because presence is updated by the hub's own
// goroutines rather than by the call the test just made.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
