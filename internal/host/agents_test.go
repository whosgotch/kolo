package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/hub"
)

func agentsFixture(t *testing.T) (*Agents, string) {
	t.Helper()
	dir := t.TempDir()
	return NewAgents(Config{Dirs: []string{dir}, Allow: []string{"cat", "true"}}, ""), dir
}

func spec(name, dir, command string) hub.Agent {
	return hub.Agent{Name: name, Dir: dir, Command: command, CreatedBy: hub.Person{ID: "artem", Name: "Artem"}}
}

// nextReport takes the next thing the host would tell the hub.
func nextReport(t *testing.T, a *Agents) statusReport {
	t.Helper()
	select {
	case r := <-a.reports:
		got, ok := r.(statusReport)
		if !ok {
			t.Fatalf("report is %T", r)
		}
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("no report")
		return statusReport{}
	}
}

func TestStartAndStop(t *testing.T) {
	a, dir := agentsFixture(t)

	if err := a.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatal(err)
	}
	if got := nextReport(t, a); got.Name != "checkups" || got.Status != hub.StatusRunning {
		t.Fatalf("reported %+v", got)
	}
	if names := a.Names(); len(names) != 1 || names[0] != "checkups" {
		t.Fatalf("running: %v", names)
	}

	a.Stop("checkups")
	waitFor(t, func() bool { return len(a.Names()) == 0 })

	// A stop is not a failure, so nothing further is reported.
	select {
	case r := <-a.reports:
		t.Errorf("stopping reported %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestStartRefuses is the check that matters: the hub asks, but this is the
// machine that would run the process, so this is where a refusal counts.
func TestStartRefuses(t *testing.T) {
	a, dir := agentsFixture(t)
	if err := a.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	for _, tc := range []struct {
		name         string
		agent        string
		dir, command string
		want         string
	}{
		{"a directory not lent", "other", "/etc", "cat", "does not lend"},
		{"a command not allowed", "other", dir, "rm", "does not run"},
		{"a name already running", "checkups", dir, "cat", "already running"},
		{"a directory in use", "other", dir, "cat", "already working in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := a.Start(spec(tc.agent, tc.dir, tc.command))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
}

func TestStopAll(t *testing.T) {
	a, dir := agentsFixture(t)
	if err := a.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	a.StopAll()
	waitFor(t, func() bool { return len(a.Names()) == 0 })
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

// TestAnAgentThatDiesComesBack is what long-lived means here. Nobody is at this
// machine, so an agent that goes has to be started again by something.
func TestAnAgentThatDiesComesBack(t *testing.T) {
	defer quickRestarts()()
	a, dir := agentsFixture(t)

	if err := a.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatal(err)
	}
	if got := nextReport(t, a); got.Status != hub.StatusRunning {
		t.Fatalf("reported %+v", got)
	}
	first := processOf(t, a, "checkups")

	first.Close() // as if it had crashed: no Stop, so nothing marked it stopping

	if got := nextReport(t, a); got.Status != hub.StatusStarting {
		t.Fatalf("after dying, reported %+v", got)
	}
	if got := nextReport(t, a); got.Status != hub.StatusRunning {
		t.Fatalf("after restarting, reported %+v", got)
	}
	waitFor(t, func() bool { return processOf(t, a, "checkups") != first })
}

// TestGivingUp: an agent that cannot stay up would otherwise be restarted for
// ever, so a few runs too short to count as runs stop the attempt.
func TestGivingUp(t *testing.T) {
	defer quickRestarts()()
	a, dir := agentsFixture(t)

	if err := a.Start(spec("brief", dir, "true")); err != nil {
		t.Fatal(err)
	}
	var last statusReport
	for range 2*restartLimit + 1 {
		last = nextReport(t, a)
		if last.Status == hub.StatusFailed {
			break
		}
	}
	if last.Status != hub.StatusFailed {
		t.Fatalf("never gave up; last report %+v", last)
	}
	if len(a.Names()) != 0 {
		t.Errorf("still listed: %v", a.Names())
	}
}

// TestTheStateFileBringsAgentsBack covers a host restarting: the org asked for
// these agents, and a machine coming back should bring them with it.
func TestTheStateFileBringsAgentsBack(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "agents.json")

	first := NewAgents(Config{Dirs: []string{dir}, Allow: []string{"cat"}}, state)
	if err := first.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatal(err)
	}
	nextReport(t, first)
	first.StopAll()

	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var written []hub.Agent
	if err := json.Unmarshal(b, &written); err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].CreatedBy.ID != "artem" {
		t.Fatalf("wrote %+v; who asked for it has to survive too", written)
	}

	second := NewAgents(Config{Dirs: []string{dir}, Allow: []string{"cat"}}, state)
	if err := second.Restore(); err != nil {
		t.Fatal(err)
	}
	defer second.StopAll()
	if names := second.Names(); len(names) != 1 || names[0] != "checkups" {
		t.Fatalf("came back with %v", names)
	}
	if got := second.Specs()[0]; got.CreatedBy.Name != "Artem" {
		t.Errorf("restored %+v", got)
	}
}

func processOf(t *testing.T, a *Agents, name string) *agent.Agent {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok {
		t.Fatalf("%s is not running", name)
	}
	return p.agent
}

func quickRestarts() func() {
	was := restartDelay
	restartDelay = 10 * time.Millisecond
	return func() { restartDelay = was }
}
