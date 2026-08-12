package host

import (
	"strings"
	"testing"
	"time"
)

func agentsFixture(t *testing.T) (*Agents, string) {
	t.Helper()
	dir := t.TempDir()
	return NewAgents([]string{dir}, []string{"cat", "true"}), dir
}

// nextReport takes the next thing the host would tell the hub.
func nextReport(t *testing.T, a *Agents) map[string]string {
	t.Helper()
	select {
	case r := <-a.reports:
		got, ok := r.(map[string]string)
		if !ok {
			t.Fatalf("report is %T", r)
		}
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("no report")
		return nil
	}
}

func TestStartAndStop(t *testing.T) {
	a, dir := agentsFixture(t)

	if err := a.Start("checkups", dir, "cat"); err != nil {
		t.Fatal(err)
	}
	if got := nextReport(t, a); got["name"] != "checkups" || got["status"] != statusRunning {
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
	if err := a.Start("checkups", dir, "cat"); err != nil {
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
			err := a.Start(tc.agent, tc.dir, tc.command)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestAnAgentThatExitsIsReported: nobody is watching this machine, so an agent
// that dies has to say so rather than quietly disappear from the list.
func TestAnAgentThatExitsIsReported(t *testing.T) {
	a, dir := agentsFixture(t)

	if err := a.Start("brief", dir, "true"); err != nil {
		t.Fatal(err)
	}
	if got := nextReport(t, a); got["status"] != statusRunning {
		t.Fatalf("reported %+v", got)
	}
	got := nextReport(t, a)
	if got["name"] != "brief" || got["status"] != statusFailed {
		t.Fatalf("reported %+v", got)
	}
	if len(a.Names()) != 0 {
		t.Errorf("still listed: %v", a.Names())
	}
}

func TestStopAll(t *testing.T) {
	a, dir := agentsFixture(t)
	if err := a.Start("checkups", dir, "cat"); err != nil {
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
