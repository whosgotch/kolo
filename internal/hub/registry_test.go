package hub

import (
	"strings"
	"testing"
)

func registryFixture(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	err := r.Join("devbox", []string{"/work/api", "/work/web"}, []string{"claude"}, func(any) error { return nil })
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
		{"a directory already worked in", agentFixture("other", "/work/api"), "already working in"},
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
	err := r.Join("devbox", []string{"/work/api"}, []string{"claude"}, func(any) error { return nil })
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
