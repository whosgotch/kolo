package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/detect"
)

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

func joinAsHost(t *testing.T, ctx context.Context, s *Server, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/host", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	conn.SetReadLimit(controlLimit)

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

func TestRelabelDoesNotTellTheHost(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	conn := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, conn, &cmd)

	if got := call(t, s, "PATCH", "/v1/agents/checkups", memberToken, `{"label":"Auth Refactor"}`); got.StatusCode != http.StatusOK {
		t.Fatalf("relabel: %s", got.Status)
	}
	if got := list(t, s, memberToken); len(got.Agents) != 1 || got.Agents[0].Name != "checkups" || got.Agents[0].Label != "Auth Refactor" {
		t.Errorf("listed %+v", got.Agents)
	}
	if got := call(t, s, "PATCH", "/v1/agents/nobody", memberToken, `{"label":"x"}`); got.StatusCode != http.StatusNotFound {
		t.Errorf("relabeling a stranger = %s", got.Status)
	}
	if got := call(t, s, "PATCH", "/v1/agents/checkups", memberToken, `{`); got.StatusCode != http.StatusBadRequest {
		t.Errorf("unreadable body = %s", got.Status)
	}

	// Name is what the host was told to spawn, and stays what the host is
	// addressed by: a label is the hub's own field, nothing to relay. Proved
	// by doing something that DOES relay (a stop) right after and checking
	// that's the very next frame: a canceled read on this library closes the
	// connection outright, so a short-deadline read can't be used to probe
	// for silence without tearing the host down first.
	call(t, s, "DELETE", "/v1/agents/checkups", memberToken, "")
	var told stop
	readFrame(t, ctx, conn, &told)
	if told.Type != "stop" || told.Name != "checkups" {
		t.Errorf("the relabel left something queued ahead of the stop: %+v", told)
	}
}

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

func watch(t *testing.T, ctx context.Context, s *Server, token, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/watch/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("watch %s: %v", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	// A browser has no read limit; this stands in for one.
	conn.SetReadLimit(screenLimit)
	return conn
}

func openScreen(t *testing.T, ctx context.Context, s *Server, token, name string) *websocket.Conn {
	return openScreenWith(t, ctx, s, token, name, adapter.For("claude").Markers)
}

// openScreenSized is a host opening a screen at the grid it starts its PTYs
// at, which is not the grid anybody is watching from.
func openScreenSized(t *testing.T, ctx context.Context, s *Server, token, name string, cols, rows int) *websocket.Conn {
	t.Helper()
	conn := dialScreen(t, ctx, s, token, name)
	hello, err := json.Marshal(screenHello{Type: "screen", Cols: cols, Rows: rows, Markers: adapter.For("claude").Markers})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	return conn
}

func openScreenWith(t *testing.T, ctx context.Context, s *Server, token, name string, markers detect.Markers) *websocket.Conn {
	t.Helper()
	conn := dialScreen(t, ctx, s, token, name)
	hello, err := json.Marshal(screenHello{Type: "screen", Cols: 80, Rows: 24, Markers: markers})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	return conn
}

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

func dialScreen(t *testing.T, ctx context.Context, s *Server, token, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("screen %s: %v", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	conn.SetReadLimit(controlLimit)
	return conn
}

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

func TestTheScreenReachesAWatcher(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
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

	// Read on rather than once: frames already sent stay readable after the
	// screen has gone, so only the end of the connection proves it dropped.
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var err error
	for err == nil {
		_, _, err = viewer.Read(read)
	}
	if errors.Is(err, context.DeadlineExceeded) {
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

func post(t *testing.T, s *Server, path string, form url.Values, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", "http://"+s.Addr()+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func sessionOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

func TestSignIn(t *testing.T) {
	s, memberToken, _ := hubFixture(t)

	resp := post(t, s, "/login", url.Values{"token": {memberToken}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %s", resp.Status)
	}
	c := sessionOf(resp)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie is reachable or cross-site: %+v", c)
	}

	req, err := http.NewRequest("GET", "http://"+s.Addr()+"/v1/agents", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c)
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("the cookie did not carry: %s", got.Status)
	}
	var body listResponse
	if err := json.NewDecoder(got.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.You.ID != "artem" {
		t.Errorf("signed in as %+v", body.You)
	}
}

func TestSignInRefusesAndSignsOut(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)

	for _, tc := range []struct{ name, token string }{
		{"a token nobody was issued", "kolo_nobody"},
		{"nothing at all", ""},
		{"a host's token", hostToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := post(t, s, "/login", url.Values{"token": {tc.token}}, nil)
			if sessionOf(resp) != nil {
				t.Error("a cookie was handed out")
			}
			if loc := resp.Header.Get("Location"); !strings.Contains(loc, "refused") {
				t.Errorf("redirected to %q, which does not say it was refused", loc)
			}
		})
	}

	signedIn := sessionOf(post(t, s, "/login", url.Values{"token": {memberToken}}, nil))
	out := post(t, s, "/logout", nil, signedIn)
	if c := sessionOf(out); c == nil || c.MaxAge >= 0 {
		t.Errorf("signing out left %+v", c)
	}
}

func TestThePageIsServed(t *testing.T) {
	s, _, _ := hubFixture(t)

	resp := call(t, s, "GET", "/", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("<title>kolo</title>")) {
		t.Errorf("not the page: %.80s", body)
	}
	if got := call(t, s, "GET", "/assets/xterm.js", "", ""); got.StatusCode != http.StatusOK {
		t.Errorf("assets: %s", got.Status)
	}
}

// The join page is the one that goes through a template rather than being
// served whole, so it reaches the embedded files by a different route than
// the page above and is worth asking for separately.
func TestTheJoinPageIsServedWithTheOrgOnIt(t *testing.T) {
	s, _, _ := hubFixture(t)

	resp := call(t, s, "GET", "/join", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /join: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("acme")) {
		t.Errorf("the org is not named on the page somebody joins it from: %.200s", body)
	}
}

// Section 13 of the AGPL asks that anybody kolo serves over a network be
// offered its source, and a team reaches kolo through a browser and nothing
// else: the person who installed it has the repo, and not one of them does.
// Both pages, because most of a team only ever sees the second one.
func TestEveryPageOffersTheSourceTheLicensePromises(t *testing.T) {
	s, _, _ := hubFixture(t)

	for _, path := range []string{"/", "/join"} {
		resp := call(t, s, "GET", path, "", "")
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("https://github.com/whosgotch/kolo")) {
			t.Errorf("%s does not offer the source the AGPL promises whoever it is served to", path)
		}
	}
}

func TestKeysReachTheHost(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)
	openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })

	viewer := watch(t, ctx, s, memberToken, "checkups")

	send := `{"type":"keys","keys":"ls\r","from":"Somebody Else"}`
	if err := viewer.Write(ctx, websocket.MessageText, []byte(send)); err != nil {
		t.Fatal(err)
	}

	var got toAgent
	readFrame(t, ctx, control, &got)
	if got.Type != "keys" || got.Name != "checkups" || got.Keys != "ls\r" {
		t.Fatalf("the host was told %+v", got)
	}
	if got.From != "Artem" {
		t.Errorf("attributed to %q, want Artem", got.From)
	}
}

func TestAJoinerIsToldWhatTheAgentIsDoing(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	dialog := " Do you want to create note.txt?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel\r\n"
	if err := screen.Write(ctx, websocket.MessageBinary, []byte(dialog)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		live, ok := s.screens.get("checkups")
		return ok && live.State() == detect.Dialog
	})

	viewer := watch(t, ctx, s, memberToken, "checkups")
	var state struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}
	for state.Type != "state" {
		kind, data, err := viewer.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if kind != websocket.MessageText {
			continue
		}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
	}
	if state.State != "dialog" {
		t.Fatalf("a joiner was told %+v", state)
	}
}

func TestAnAgentKindWithNoMarkersClaimsNothing(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)

	screen := openScreenWith(t, ctx, s, hostToken, "checkups", detect.Markers{})
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })

	dialog := " Do you want to create note.txt?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel\r\n"
	if err := screen.Write(ctx, websocket.MessageBinary, []byte(dialog)); err != nil {
		t.Fatal(err)
	}

	live, ok := s.screens.get("checkups")
	if !ok {
		t.Fatal("no screen")
	}
	waitFor(t, func() bool { return strings.Contains(live.Text(), "note.txt") })
	if state := live.State(); state != detect.Unknown {
		t.Errorf("an unreadable screen was read as %s", state)
	}
}

func TestAnInterruptReachesTheHost(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)
	openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })

	viewer := watch(t, ctx, s, memberToken, "checkups")
	for _, send := range []string{
		// Dropped, not passed on, so the frame read below is the interrupt.
		`{"type":"keystroke","text":""}`,
		`{"type":"answer","choice":2,"label":"No"}`,
		`{"type":"interrupt"}`,
	} {
		if err := viewer.Write(ctx, websocket.MessageText, []byte(send)); err != nil {
			t.Fatal(err)
		}
	}

	var interrupt toAgent
	readFrame(t, ctx, control, &interrupt)
	if interrupt.Type != "interrupt" || interrupt.Name != "checkups" || interrupt.From != "Artem" {
		t.Errorf("the host was told %+v", interrupt)
	}
}

func TestARestartAndAStartFreshReachTheHost(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)
	openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })

	viewer := watch(t, ctx, s, memberToken, "checkups")
	for _, send := range []string{`{"type":"restart"}`, `{"type":"fresh"}`} {
		if err := viewer.Write(ctx, websocket.MessageText, []byte(send)); err != nil {
			t.Fatal(err)
		}
	}

	for _, want := range []string{"restart", "fresh"} {
		var got toAgent
		readFrame(t, ctx, control, &got)
		if got.Type != want || got.Name != "checkups" || got.From != "Artem" {
			t.Errorf("the host was told %+v, want a %s", got, want)
		}
	}
}

func TestWatchersSeeWhoIsTyping(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, viewer)

	typer := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, typer)
	if err := typer.Write(ctx, websocket.MessageText, []byte(`{"type":"keys","keys":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	_ = screen

	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		kind, data, err := viewer.Read(deadline)
		if err != nil {
			t.Fatalf("never saw who is typing: %v", err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var event struct {
			Type, Who string
		}
		json.Unmarshal(data, &event)
		if event.Type == "keyboard" {
			if event.Who != "Artem" {
				t.Errorf("watchers were told %+v", event)
			}
			return
		}
	}
}

// A page on another site may not make kolo act for somebody who is signed in.
// The token is not the defence here: a browser attaches the cookie itself, so
// what is checked is where the request was written, not who it is from.
func TestAnotherSitesPageCannotAct(t *testing.T) {
	s, memberToken, _ := hubFixture(t)

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"a modern browser says so", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"an old one is caught by Origin", map[string]string{"Origin": "http://evil.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "http://"+s.Addr()+"/v1/agents",
				bytes.NewBufferString(`{"name":"theirs","host":"devbox","dir":"/work/api","command":"claude"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Cookie", sessionCookie+"="+memberToken)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

// Kolo's own page, and any program holding a token, are unaffected: one says
// same-origin, the other sends neither header because it is not a browser.
func TestKolosOwnPageAndProgramsStillAct(t *testing.T) {
	s, memberToken, hostToken := hubFixture(t)
	ctx := testContext(t)
	joinAsHost(t, ctx, s, hostToken)

	req, err := http.NewRequest("POST", "http://"+s.Addr()+"/v1/agents",
		bytes.NewBufferString(`{"name":"ours","host":"devbox","dir":"/work/api","command":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", sessionCookie+"="+memberToken)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("kolo's own page: status = %d, want 201", resp.StatusCode)
	}

	// No Sec-Fetch-Site and no Origin, the way curl or a script arrives.
	if got := create(t, s, memberToken, `{"name":"scripted","host":"devbox","dir":"/work/web","command":"claude"}`); got.StatusCode != http.StatusCreated {
		t.Errorf("a program: status = %d, want 201", got.StatusCode)
	}
}

// A repaint is as big as the screen it draws, and the websocket default of
// 32 KiB is smaller than a colourful one. The frame that would be refused is
// the first one a host sends, so the agent would never appear at all.
func TestABigRepaintReachesAWatcher(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, viewer)

	repaint := bytes.Repeat([]byte("x"), 200<<10)
	if err := screen.Write(ctx, websocket.MessageBinary, repaint); err != nil {
		t.Fatalf("a %d-byte repaint could not be sent: %v", len(repaint), err)
	}
	if got := readUntilBytes(t, ctx, viewer); len(got) != len(repaint) {
		t.Errorf("the watcher got %d bytes of a %d-byte repaint", len(got), len(repaint))
	}
}

// A pasted prompt arrives as one keys message, well past 32 KiB once the
// browser has JSON-escaped it.
func TestALongPasteReachesTheHost(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken := hubFixture(t)
	control := joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
	create(t, s, memberToken, `{"name":"checkups","host":"devbox","dir":"/work/api","command":"claude"}`)
	var cmd spawn
	readFrame(t, ctx, control, &cmd)
	openScreen(t, ctx, s, hostToken, "checkups")
	waitFor(t, func() bool { _, ok := s.screens.get("checkups"); return ok })

	viewer := watch(t, ctx, s, memberToken, "checkups")
	paste := strings.Repeat("a", 60<<10)
	msg, err := json.Marshal(viewerMessage{Type: "keys", Keys: paste})
	if err != nil {
		t.Fatal(err)
	}
	if err := viewer.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatalf("a %d-byte paste could not be sent: %v", len(paste), err)
	}
	var got toAgent
	readFrame(t, ctx, control, &got)
	if got.Type != "keys" || len(got.Keys) != len(paste) {
		t.Errorf("the host got %s of %d bytes, for a %d-byte paste", got.Type, len(got.Keys), len(paste))
	}
}

// A host that dropped off the network answers nothing. Until the hub notices,
// Registry.Join refuses the machine's reconnect as one already connected, so
// how long this takes is how long that machine is locked out of its own org.
func TestASilentHostIsDropped(t *testing.T) {
	ctx := testContext(t)
	s, _, hostToken := hubFixture(t)
	// In test time rather than the half minute a running hub waits. Set
	// before anything connects, which is the only reader.
	s.ping, s.pingBy = 20*time.Millisecond, 200*time.Millisecond

	joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })

	// Nothing reads this connection from here on, so nothing answers a ping.
	// A machine whose wifi went is indistinguishable from this.
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 0 })

	// And the machine can come back under the same id.
	joinAsHost(t, ctx, s, hostToken)
	waitFor(t, func() bool { return len(s.Registry().Hosts()) == 1 })
}

// A host opens every screen at the size it starts every PTY at, and its old
// connection is not known to be dead yet. What was agreed about the screen
// before this one has to go with it, or every browser watches the host's
// size in a window that is not that size.
func TestAReplacedScreenForgetsTheOldSize(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, hostToken, _ := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	sized, _ := json.Marshal(viewerMessage{Type: "size", Cols: 80, Rows: 24})
	if err := viewer.Write(ctx, websocket.MessageText, sized); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return sizeOf(s, "checkups") == size{80, 24} })

	// The agent restarted. Nothing has read the old connection since, which is
	// what a host that dropped off the network looks like.
	was, _ := s.screens.get("checkups")
	openScreenSized(t, ctx, s, hostToken, "checkups", 120, 40)
	waitFor(t, func() bool {
		now, ok := s.screens.get("checkups")
		return ok && now != was
	})

	// The dropped viewer comes back and proposes the same window again. Held
	// over from the old screen, that proposal reads as no change and the new
	// screen is left at the host's size.
	back := watch(t, ctx, s, memberToken, "checkups")
	if err := back.Write(ctx, websocket.MessageText, sized); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return sizeOf(s, "checkups") == size{80, 24} })
}

// sizeOf is the grid a screen is being drawn on, read the way anything else
// reads it, since the maps behind it belong to whichever goroutine holds the
// lock.
func sizeOf(s *Server, name string) size {
	live, ok := s.screens.get(name)
	if !ok {
		return size{}
	}
	cols, rows := live.Size()
	return size{cols, rows}
}

// A size is a number a browser sent, and the hub builds a grid that size.
func TestProposedSizesAreBounded(t *testing.T) {
	live := newScreens()
	live.open("checkups", 120, 40, detect.Markers{})

	cols, rows, _ := live.propose("checkups", "a browser", 1<<20, 1<<20)
	if cols != roof.cols || rows != roof.rows {
		t.Errorf("a proposal of 1048576x1048576 agreed on %dx%d, want the roof of %dx%d",
			cols, rows, roof.cols, roof.rows)
	}
	cols, rows, _ = live.propose("checkups", "a browser", 2, 1)
	if cols != floor.cols || rows != floor.rows {
		t.Errorf("a proposal of 2x1 agreed on %dx%d, want the floor of %dx%d",
			cols, rows, floor.cols, floor.rows)
	}
}
