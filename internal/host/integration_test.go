package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/hub"
)

// This is the smallest complete user journey: a real host dials a real hub,
// a member starts a PTY-backed agent, watches it, types, sees the result, and
// stops it. The agent is deterministic and needs no model account.
func TestHubHostAndBrowserJourney(t *testing.T) {
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "fake-agent", `
printf '? for shortcuts\r\n'
while IFS= read -r line; do
  printf 'heard %s\r\n? for shortcuts\r\n' "$line"
done
`)

	memberToken, memberHash, err := hub.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	hostToken, hostHash, err := hub.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	server, err := hub.Listen(&hub.Org{
		Name:    "acme",
		Members: []hub.Member{{ID: "artem", Name: "Artem", TokenHash: memberHash}},
		Hosts:   []hub.Host{{ID: "devbox", TokenHash: hostHash}},
	}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	go func() {
		if err := server.Serve(); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agents := NewAgents(Config{
		Hub: "http://" + server.Addr(), Token: hostToken,
		Dirs: []string{dir}, Allow: []string{script}, Version: "test",
	}, filepath.Join(t.TempDir(), "agents.json"))
	t.Cleanup(agents.StopAll)
	go Run(ctx, agents, nil)

	requestBody, err := json.Marshal(map[string]string{
		"name": "checkups", "host": "devbox", "dir": dir, "command": script,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(requestBody)
	var created *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		created = memberRequest(t, server, memberToken, http.MethodPost, "/v1/agents", body)
		if created.StatusCode == http.StatusCreated {
			break
		}
		created.Body.Close()
		time.Sleep(20 * time.Millisecond)
	}
	if created == nil || created.StatusCode != http.StatusCreated {
		if created == nil {
			t.Fatal("the host never connected")
		}
		problem, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("create: %s: %s", created.Status, problem)
	}
	created.Body.Close()

	viewer := dialViewer(t, ctx, server, memberToken, "checkups")
	waitForOutput(t, ctx, viewer, "? for shortcuts")
	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"keys","keys":"hello\r"}`)); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, ctx, viewer, "heard hello")

	stopped := memberRequest(t, server, memberToken, http.MethodDelete, "/v1/agents/checkups", "")
	defer stopped.Body.Close()
	if stopped.StatusCode != http.StatusNoContent {
		problem, _ := io.ReadAll(stopped.Body)
		t.Fatalf("stop: %s: %s", stopped.Status, problem)
	}
	waitFor(t, func() bool { return len(agents.Names()) == 0 })
}

func memberRequest(t *testing.T, server *hub.Server, token, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+server.Addr()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func dialViewer(t *testing.T, ctx context.Context, server *hub.Server, token, name string) *websocket.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err := websocket.Dial(ctx, "ws://"+server.Addr()+"/v1/watch/"+name, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
		if err == nil {
			t.Cleanup(func() { conn.CloseNow() })
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the agent never exposed a screen")
	return nil
}

func waitForOutput(t *testing.T, parent context.Context, viewer *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	for {
		kind, data, err := viewer.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; output %q", want, err, output.String())
		}
		if kind != websocket.MessageBinary {
			continue
		}
		output.Write(data)
		if strings.Contains(output.String(), want) {
			return
		}
	}
}
