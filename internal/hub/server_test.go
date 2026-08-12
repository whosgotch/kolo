package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// hubFixture starts a hub with one member and one host, and returns both tokens.
func hubFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	memberToken, memberHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	hostToken, hostHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{
		Name:    "acme",
		Members: []Member{{ID: "artem", Name: "Artem", TokenHash: memberHash}},
		Hosts:   []Host{{ID: "devbox", TokenHash: hostHash}},
	}

	s, err := Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, memberToken, hostToken
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// joinAsHost dials as a machine lending /work/api and /work/web.
func joinAsHost(t *testing.T, ctx context.Context, s *Server, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	hello := `{"type":"hello","dirs":["/work/api","/work/web"],"allow":["claude"],"version":"test"}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatal(err)
	}
	var w hostWelcome
	readFrame(t, ctx, conn, &w)
	if w.Type != "welcome" || w.Org != "acme" || w.Host != "devbox" {
		t.Fatalf("welcome = %+v", w)
	}
	return conn
}

func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

// call makes an authenticated request to the hub.
func call(t *testing.T, s *Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+s.Addr()+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func create(t *testing.T, s *Server, token, body string) *http.Response {
	t.Helper()
	return call(t, s, "POST", "/v1/agents", token, body)
}

func list(t *testing.T, s *Server, token string) listResponse {
	t.Helper()
	resp := call(t, s, "GET", "/v1/agents", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %s", resp.Status)
	}
	var got listResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

func TestAuthenticate(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)

	for _, tc := range []struct {
		name   string
		header string
		want   bool
	}{
		{"a member's token", "Bearer " + memberToken, true},
		{"no header at all", "", false},
		{"the token without the scheme", memberToken, false},
		{"a token nobody was issued", "Bearer kolo_nobody", false},
		{"a host's token", "Bearer " + hostToken, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest("GET", "/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if _, ok := s.authenticate(r); ok != tc.want {
				t.Errorf("authenticate = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestHostJoins(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	joinAsHost(t, ctx, s, hostToken)

	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
	got := list(t, s, memberToken)
	if len(got.Hosts) != 1 || got.Hosts[0].ID != "devbox" {
		t.Fatalf("hosts = %+v", got.Hosts)
	}
	if len(got.Hosts[0].Dirs) != 2 || got.Hosts[0].Allow[0] != "claude" {
		t.Errorf("the host's terms did not survive: %+v", got.Hosts[0])
	}
}

// TestCreateReachesTheHost is the whole of this milestone: a member on one
// machine asks for an agent and the machine lending itself is told to run it.
func TestCreateReachesTheHost(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	resp := create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %s", resp.Status)
	}

	var cmd spawn
	readFrame(t, ctx, conn, &cmd)
	if cmd.Type != "spawn" || cmd.Agent.Name != "checkups" || cmd.Agent.Dir != "/work/api" || cmd.Agent.Command != "claude" {
		t.Fatalf("the host was told %+v", cmd)
	}
	// The host writes this down and reads it back on reconnect, so attribution
	// has to be in the command rather than only in the hub's memory.
	if cmd.Agent.CreatedBy.ID != "artem" || cmd.Agent.CreatedAt.IsZero() {
		t.Errorf("the spawn did not carry who asked: %+v", cmd.Agent)
	}

	got := list(t, s, memberToken)
	if len(got.Agents) != 1 {
		t.Fatalf("agents = %+v", got.Agents)
	}
	if got.Agents[0].Status != StatusStarting {
		t.Errorf("before the host answers, status is %q", got.Agents[0].Status)
	}
	if got.Agents[0].CreatedBy.ID != "artem" {
		t.Errorf("created by %+v, want artem", got.Agents[0].CreatedBy)
	}

	report := `{"type":"status","name":"checkups","status":"running"}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(report)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		a, ok := s.Registry().Agent("checkups")
		return ok && a.Status == StatusRunning
	})
}

func TestCreateRefusals(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	ok := create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	if ok.StatusCode != http.StatusCreated {
		t.Fatalf("create: %s", ok.Status)
	}
	var cmd spawn
	readFrame(t, ctx, conn, &cmd)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"a name with a space", `{"name":"my agent","host":"devbox","dir":"/work/web","command":"claude"}`, http.StatusBadRequest},
		{"no name at all", `{"host":"devbox","dir":"/work/web","command":"claude"}`, http.StatusBadRequest},
		{"unreadable json", `{`, http.StatusBadRequest},
		{"a name already taken", `{"name":"checkups","host":"devbox","dir":"/work/web","command":"claude"}`, http.StatusConflict},
		{"a directory in use", `{"name":"other","host":"devbox","dir":"/work/api","command":"claude"}`, http.StatusConflict},
		{"a directory not lent", `{"name":"other","host":"devbox","dir":"/etc","command":"claude"}`, http.StatusConflict},
		{"a command not allowed", `{"name":"other","host":"devbox","dir":"/work/web","command":"rm"}`, http.StatusConflict},
		{"a host not connected", `{"name":"other","host":"laptop","dir":"/work/web","command":"claude"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := create(t, s, memberToken, tc.body); got.StatusCode != tc.want {
				t.Errorf("create = %s, want %d", got.Status, tc.want)
			}
		})
	}
	if got := list(t, s, memberToken); len(got.Agents) != 1 {
		t.Errorf("a refusal left something behind: %+v", got.Agents)
	}
}

func TestDeleteTellsTheHost(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, conn, &cmd)

	if got := call(t, s, "DELETE", "/v1/agents/checkups", memberToken, ""); got.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %s", got.Status)
	}
	var told stop
	readFrame(t, ctx, conn, &told)
	if told.Type != "stop" || told.Name != "checkups" {
		t.Errorf("the host was told %+v", told)
	}
	if got := list(t, s, memberToken); len(got.Agents) != 0 {
		t.Errorf("still listed: %+v", got.Agents)
	}
	if got := call(t, s, "DELETE", "/v1/agents/checkups", memberToken, ""); got.StatusCode != http.StatusNotFound {
		t.Errorf("deleting it twice = %s", got.Status)
	}
}

// TestTokensReachOnlyTheirOwnRoutes: a host's token runs processes and a
// member's does not, so neither may be used where the other belongs.
func TestTokensReachOnlyTheirOwnRoutes(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)

	if got := call(t, s, "GET", "/v1/agents", hostToken, ""); got.StatusCode != http.StatusUnauthorized {
		t.Errorf("a host listed agents: %s", got.Status)
	}
	if got := call(t, s, "GET", "/v1/agents", "", ""); got.StatusCode != http.StatusUnauthorized {
		t.Errorf("an anonymous caller listed agents: %s", got.Status)
	}
	_, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + memberToken}},
	})
	if err == nil {
		t.Error("a member's token opened the host socket")
	}
}

// TestASecondHostIsRefused covers a host started twice by mistake: two processes
// answering for one machine would make every command ambiguous.
func TestASecondHostIsRefused(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + hostToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	hello := `{"type":"hello","dirs":["/work/api"],"allow":["claude"],"version":"test"}`
	conn.Write(ctx, websocket.MessageText, []byte(hello))

	if _, _, err := conn.Read(ctx); err == nil {
		t.Error("the second connection was welcomed")
	}
	if got := list(t, s, memberToken); len(got.Hosts) != 1 {
		t.Errorf("hosts = %+v", got.Hosts)
	}
}

// TestAHostLeavingTakesItsAgents is honest listing: an agent nobody can reach is
// not shown as though they could. It comes back when the host reconnects.
func TestAHostLeavingTakesItsAgents(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, conn, &cmd)

	conn.CloseNow()
	waitFor(t, func() bool { return len(s.Registry().Agents()) == 0 })
	if got := list(t, s, memberToken); len(got.Hosts) != 0 {
		t.Errorf("hosts = %+v", got.Hosts)
	}
}

// TestReconnectingRestoresTheList is the gap this milestone closes: a dropped
// connection never stopped the processes, and the host says what it has when it
// comes back.
func TestReconnectingRestoresTheList(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, conn, &cmd)

	conn.CloseNow()
	waitFor(t, func() bool { return len(s.Registry().Agents()) == 0 })

	back, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + hostToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { back.CloseNow() })

	hello, err := json.Marshal(map[string]any{
		"type": "hello", "dirs": []string{"/work/api"}, "allow": []string{"claude"},
		"agents": []Agent{{Name: "checkups", Dir: "/work/api", Command: "claude", Status: StatusRunning,
			CreatedBy: Person{ID: "artem", Name: "Artem"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	var w hostWelcome
	readFrame(t, ctx, back, &w)

	got := list(t, s, memberToken)
	if len(got.Agents) != 1 || got.Agents[0].Name != "checkups" {
		t.Fatalf("agents = %+v", got.Agents)
	}
	if got.Agents[0].Status != StatusRunning || got.Agents[0].CreatedBy.ID != "artem" {
		t.Errorf("came back as %+v", got.Agents[0])
	}
}

// watch opens a member's view of an agent.
func watch(t *testing.T, ctx context.Context, s *Server, token, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/watch/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("watch %s: %v", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// openScreen connects an agent's terminal as its host would.
func openScreen(t *testing.T, ctx context.Context, s *Server, token, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("screen %s: %v", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"screen","cols":80,"rows":24}`)); err != nil {
		t.Fatal(err)
	}
	return conn
}

// withAgent gets a hub to the state where one agent exists and its screen is up.
func withAgent(t *testing.T, ctx context.Context) (_ *Server, memberToken, hostToken string, screen *websocket.Conn) {
	t.Helper()
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)

	screen = openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })
	return s, memberToken, hostToken, screen
}

// readUntilBytes takes frames until terminal output arrives, skipping the
// control frames a viewer also gets.
func readUntilBytes(t *testing.T, ctx context.Context, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if kind == websocket.MessageBinary {
			return data
		}
	}
}

// TestTheScreenReachesAWatcher is what the milestone is for: what the agent
// draws on one machine is what somebody sees on another.
func TestTheScreenReachesAWatcher(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	// The subscription opens with a repaint, so what arrives first describes the
	// screen as it already stands rather than only what happens next.
	if got := readUntilBytes(t, ctx, viewer); len(got) == 0 {
		t.Fatal("no repaint on joining")
	}

	if err := screen.Write(ctx, websocket.MessageBinary, []byte("hello from the agent")); err != nil {
		t.Fatal(err)
	}
	if got := readUntilBytes(t, ctx, viewer); string(got) != "hello from the agent" {
		t.Errorf("watcher got %q", got)
	}
}

// TestWatchersDoNotDisturbEachOther: people are meant to watch together, and a
// second viewer joining must not interrupt the first.
func TestWatchersDoNotDisturbEachOther(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	first := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, first)
	second := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, second)

	if err := screen.Write(ctx, websocket.MessageBinary, []byte("both of you")); err != nil {
		t.Fatal(err)
	}
	for i, viewer := range []*websocket.Conn{first, second} {
		if got := readUntilBytes(t, ctx, viewer); string(got) != "both of you" {
			t.Errorf("viewer %d got %q", i, got)
		}
	}
}

// TestARestartedScreenReplacesTheOld: a new process is a new screen, and the old
// viewers are dropped rather than left watching something that has gone.
func TestARestartedScreenReplacesTheOld(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken, first := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, viewer)

	was, _ := s.screens.get("checkups")
	first.CloseNow()
	second := openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool {
		now, ok := s.screens.get("checkups")
		return ok && now != was
	})

	if _, _, err := viewer.Read(ctx); err == nil {
		t.Error("a viewer of the old screen was left connected to it")
	}
	back := watch(t, ctx, s, memberToken, "checkups")
	if err := second.Write(ctx, websocket.MessageBinary, []byte("started again")); err != nil {
		t.Fatal(err)
	}
	readUntilBytes(t, ctx, back)
}

func TestScreenRoutesRefuseTheWrongToken(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken, _ := withAgent(t, ctx)

	if _, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent/checkups", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + memberToken}},
	}); err == nil {
		t.Error("a member carried an agent's screen")
	}
	if _, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/watch/checkups", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + hostToken}},
	}); err == nil {
		t.Error("a host watched an agent")
	}
	if _, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/watch/nothing", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + memberToken}},
	}); err == nil {
		t.Error("watching an agent that does not exist succeeded")
	}
	if _, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent/nothing", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + hostToken}},
	}); err == nil {
		t.Error("a host carried a screen for an agent it was never asked to run")
	}
}
