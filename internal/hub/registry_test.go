package hub

import (
	"strings"
	"testing"
)

func registryFixture(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	err := r.Join("devbox", []string{"/work/api", "/work/web"}, []string{"claude"}, nil, nil, nil, func(any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func agentFixture(name, dir string) Agent {
	return Agent{Name: name, Host: "devbox", Dir: dir, Command: "claude"}
}

func TestAddAndList(t *testing.T) {
	r := registryFixture(t)

	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Fatal(err)
	}
	agents := r.Agents()
	if len(agents) != 1 || agents[0].Name != "checkups" {
		t.Fatalf("listed %+v", agents)
	}
	if agents[0].Status != StatusStarting {
		t.Errorf("a new agent is %q, want %q", agents[0].Status, StatusStarting)
	}
}

// TestAddRefuses covers the rules a member is told about in the response to
// their own request, rather than finding out from a list that never changes.
func TestAddRefuses(t *testing.T) {
	r := registryFixture(t)
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		agent Agent
		want  string
	}{
		{"a name already in use", agentFixture("checkups", "/work/web"), "already exists"},
		{"a directory already worked in", agentFixture("other", "/work/api"), "one claude to a directory"},
		{"a directory not lent", agentFixture("other", "/etc"), "does not lend"},
		{"a command not allowed", Agent{Name: "other", Host: "devbox", Dir: "/work/web", Command: "rm"}, "does not run"},
		{"a host not connected", Agent{Name: "other", Host: "laptop", Dir: "/work/web", Command: "claude"}, "no host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Add(tc.agent)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestLeavingTakesTheAgentsWithIt is the honest version of a list: an agent
// nobody can reach is not shown as though they could.
func TestLeavingTakesTheAgentsWithIt(t *testing.T) {
	r := registryFixture(t)
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Fatal(err)
	}

	r.Leave("devbox")
	if got := r.Agents(); len(got) != 0 {
		t.Errorf("a disconnected host left %+v behind", got)
	}
	if got := r.Hosts(); len(got) != 0 {
		t.Errorf("hosts still lists %+v", got)
	}
}

func TestOneConnectionPerHost(t *testing.T) {
	r := registryFixture(t)
	err := r.Join("devbox", []string{"/work/api"}, []string{"claude"}, nil, nil, nil, func(any) error { return nil })
	if err == nil {
		t.Fatal("a second connection claimed the same host")
	}
	if !strings.Contains(err.Error(), "already connected") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestRemoveFreesTheNameAndTheDirectory(t *testing.T) {
	r := registryFixture(t)
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Remove("checkups"); !ok {
		t.Fatal("nothing removed")
	}
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Errorf("the name and directory were not freed: %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, s := range []string{"a", "checkups", "mr-checkups", "api-2"} {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "Checkups", "with space", "-lead", "trail-", "a/b", strings.Repeat("x", 33)} {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
}

// TestJoinRestoresWhatTheHostIsRunning covers a dropped connection. The
// processes never stopped; only the hub's knowledge of them did, and the host is
// the only party that still knows.
func TestJoinRestoresWhatTheHostIsRunning(t *testing.T) {
	r := registryFixture(t)
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err != nil {
		t.Fatal(err)
	}
	was, _ := r.Agent("checkups")
	r.Leave("devbox")

	was.Status = StatusRunning
	err := r.Join("devbox", []string{"/work/api"}, []string{"claude"}, nil, nil, []Agent{was}, func(any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	got, ok := r.Agent("checkups")
	if !ok {
		t.Fatal("it did not come back")
	}
	if got.Status != StatusRunning || got.Host != "devbox" {
		t.Errorf("came back as %+v", got)
	}
	if _, err := r.Add(agentFixture("checkups", "/work/api")); err == nil {
		t.Error("a restored agent did not hold its name")
	}
}

// TestAHostThatLendsAnyCommand: '*' is the host saying every command named like
// one on its PATH may be started, and the hub's check follows that shape — a
// name passes, a path does not, and without '*' nothing changes.
func TestAHostThatLendsAnyCommand(t *testing.T) {
	r := NewRegistry()
	err := r.Join("devbox", []string{"/work/api"}, []string{AllowAny}, nil, nil, nil, func(any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	a := agentFixture("checkups", "/work/api")
	a.Command = "aider --model opus"
	if _, err := r.Add(a); err != nil {
		t.Errorf("a command the host lent any of: %v", err)
	}

	pathed := agentFixture("scripts", "/work/api")
	pathed.Command = "/opt/tools/aider"
	if _, err := r.Add(pathed); err == nil {
		t.Error("a command carrying a path was taken")
	}

	empty := agentFixture("blank", "/work/api")
	empty.Command = ""
	if _, err := r.Add(empty); err == nil {
		t.Error("no command at all was taken")
	}
}

// TestLendingAnyIsNotTheDefault: a host that enumerated its commands has not
// lent anything else, whatever it runs.
func TestLendingAnyIsNotTheDefault(t *testing.T) {
	r := registryFixture(t)
	a := agentFixture("checkups", "/work/api")
	a.Command = "aider"
	if _, err := r.Add(a); err == nil {
		t.Error("an unlisted command started on a host that lends a list")
	}
}

// TestAgentsThatNameTheirConversationsShareADirectory: the per-directory rule
// bends where the host vouches for both kinds, and its word is the only thing
// that bends it — the hub has no kinds of its own to ask.
func TestAgentsThatNameTheirConversationsShareADirectory(t *testing.T) {
	r := NewRegistry()
	err := r.Join("devbox", []string{"/work/api"}, []string{"robo", "claude"}, nil,
		[]string{"robo"}, nil, func(any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	first := agentFixture("one", "/work/api")
	first.Command = "robo"
	if _, err := r.Add(first); err != nil {
		t.Fatal(err)
	}
	second := agentFixture("two", "/work/api")
	second.Command = "robo"
	if _, err := r.Add(second); err != nil {
		t.Fatalf("a second agent that names its conversation was refused: %v", err)
	}

	// Different kinds never read one another's conversation stores, so a
	// claude beside a robo cannot come back as it however either resumes.
	third := agentFixture("three", "/work/api")
	third.Command = "claude"
	if _, err := r.Add(third); err != nil {
		t.Errorf("a different kind was refused a shared directory: %v", err)
	}

	// Two of the SAME kind are the dangerous pair, and vouching decides.
	r2 := NewRegistry()
	if err := r2.Join("devbox", []string{"/work/api"}, []string{"claude"}, nil, nil, nil, func(any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	a := agentFixture("one", "/work/api")
	b := agentFixture("two", "/work/api")
	if _, err := r2.Add(a); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Add(b); err == nil || !strings.Contains(err.Error(), "one claude to a directory") {
		t.Errorf("two unvouched claudes shared a directory: %v", err)
	}
}
