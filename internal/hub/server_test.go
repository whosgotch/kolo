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

// TestCreateReachesTheHost: a member on one machine asks for an agent and the
// machine lending itself is told to run it.
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

// TestReconnectingRestoresTheList: a dropped connection never stopped the
// processes, and the host says what it has when it comes back.
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
	// With the markers, as a host does: the screens these tests draw are Claude
	// Code's, and the hub is told how to read one rather than knowing.
	return openScreenWith(t, ctx, s, token, name, adapter.For("claude").Markers)
}

func openScreenWith(t *testing.T, ctx context.Context, s *Server, token, name string, markers detect.Markers) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/agent/"+name, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("screen %s: %v", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	hello, err := json.Marshal(screenHello{Type: "screen", Cols: 80, Rows: 24, Markers: markers})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
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

// TestTheScreenReachesAWatcher: what the agent draws on one machine is what
// somebody sees on another.
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

	// Read on rather than once: what a viewer was already told stays readable
	// after the screen has gone, so the end of the connection is what to wait for.
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

// post sends a form without following the redirect, so the response that sets
// the cookie is the one under test.
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

// TestSignIn: a member pastes their token once and the browser carries it after
// that, where script cannot read it.
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

	// Served without a token: the page is what asks for one.
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

// TestKeysReachTheHost: what the member holding the keyboard presses goes to the
// machine running the agent, with the hub's word for who pressed it.
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
	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"take"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, held := s.keyboards.holder("checkups"); return held })

	send := `{"type":"keys","keys":"ls\r","from":"Somebody Else"}`
	if err := viewer.Write(ctx, websocket.MessageText, []byte(send)); err != nil {
		t.Fatal(err)
	}

	var got toAgent
	readFrame(t, ctx, control, &got)
	if got.Type != "keys" || got.Name != "checkups" || got.Keys != "ls\r" {
		t.Fatalf("the host was told %+v", got)
	}
	// The name is decided from the connection's credentials, so a browser
	// claiming to be somebody else is simply not read.
	if got.From != "Artem" {
		t.Errorf("attributed to %q, want Artem", got.From)
	}
}

// TestKeysAreRefusedWithoutTheKeyboard is the whole of the concurrency rule. A
// browser can ask; the hub decides, because it is the only thing that knows who
// is holding what.
func TestKeysAreRefusedWithoutTheKeyboard(t *testing.T) {
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
	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"keys","keys":"rm -rf /"}`)); err != nil {
		t.Fatal(err)
	}
	// An interrupt after it, to prove the connection is alive and being read: if
	// the keys had gone through they would arrive first.
	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"interrupt"}`)); err != nil {
		t.Fatal(err)
	}

	var got toAgent
	readFrame(t, ctx, control, &got)
	if got.Type != "interrupt" {
		t.Fatalf("the host was told %+v; keys went through without the keyboard", got)
	}
}

// TestAJoinerIsToldWhatItMayDo: somebody opening an agent mid-question sees the
// question repainted, and must be given the way to answer it — which the host
// announced before this browser existed.
func TestAJoinerIsToldWhatItMayDo(t *testing.T) {
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
		Type    string          `json:"type"`
		State   string          `json:"state"`
		Options []detect.Option `json:"options"`
	}
	// Past the size and the repaint, which are the rest of catching up.
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
	if state.State != "dialog" || len(state.Options) != 2 {
		t.Fatalf("a joiner was told %+v", state)
	}
	if state.Options[1].Label != "No" || !state.Options[0].Selected {
		t.Errorf("the choices arrived as %+v", state.Options)
	}
}

// TestAnAgentKindWithNoMarkersClaimsNothing: a host lending a command kolo has
// no adapter for sends no markers, and the hub then says nothing about that
// agent's screen rather than reading it with somebody else's. It is still
// watched and still typed at; what it loses is the board being able to say what
// it is doing.
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
	if options := live.Options(); len(options) != 0 {
		t.Errorf("choices were offered off a screen nothing is known about: %+v", options)
	}
}

// TestAnAnswerAndAnInterruptReachTheHost: the two things a member does that are
// not words. The hub passes both on unread; the screen's machine decides.
func TestAnAnswerAndAnInterruptReachTheHost(t *testing.T) {
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
		// Unrecognised first: it must be dropped rather than passed on, so the
		// frames read below are the two that were meant.
		`{"type":"keystroke","text":""}`,
		`{"type":"answer","choice":2,"label":"No"}`,
		`{"type":"interrupt"}`,
	} {
		if err := viewer.Write(ctx, websocket.MessageText, []byte(send)); err != nil {
			t.Fatal(err)
		}
	}

	var answer toAgent
	readFrame(t, ctx, control, &answer)
	if answer.Type != "answer" || answer.Choice != 2 || answer.Label != "No" {
		t.Fatalf("the host was told %+v", answer)
	}
	if answer.From != "Artem" || answer.Name != "checkups" {
		t.Errorf("answer arrived as %+v", answer)
	}

	var interrupt toAgent
	readFrame(t, ctx, control, &interrupt)
	if interrupt.Type != "interrupt" || interrupt.Name != "checkups" || interrupt.From != "Artem" {
		t.Errorf("the host was told %+v", interrupt)
	}
}

// TestARestartAndAStartFreshReachTheHost: both are the process rather than the
// screen, and the hub passes them on like everything else — attributed, unread.
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

// TestTheKeyboardIsVisibleToWatchers: who is typing reaches everyone watching,
// not only whoever took it. It is the only thing keeping two people off one
// terminal, so it has to be seen.
func TestTheKeyboardIsVisibleToWatchers(t *testing.T) {
	ctx := testContext(t)
	s, memberToken, _, screen := withAgent(t, ctx)

	viewer := watch(t, ctx, s, memberToken, "checkups")
	readUntilBytes(t, ctx, viewer) // the repaint

	// A second browser takes the keyboard; the first is watching only.
	taker := watch(t, ctx, s, memberToken, "checkups")
	if err := taker.Write(ctx, websocket.MessageText, []byte(`{"type":"take"}`)); err != nil {
		t.Fatal(err)
	}
	_ = screen

	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		kind, data, err := viewer.Read(deadline)
		if err != nil {
			t.Fatalf("never saw the keyboard change hands: %v", err)
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
