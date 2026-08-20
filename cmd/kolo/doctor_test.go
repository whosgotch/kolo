package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

// write puts a state file where the doctor will look, and returns its path.
func writeState(t *testing.T, state host.State) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.json")
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// absent is a path with nothing at it, for the checks that take a file kolo
// does not need to exist.
func absent(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nothing.json")
}

func report(t *testing.T, state host.State, kinds string) (string, bool) {
	t.Helper()
	var out strings.Builder
	ok, err := doctor(&out, writeState(t, state), kinds)
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), ok
}

func agent(name, dir, command, state string, since time.Time) host.Record {
	return host.Record{
		Spec:  hub.Agent{Name: name, Dir: dir, Command: command},
		State: state,
		Since: since,
	}
}

// TestDoctorSaysWhatEachAgentKindCosts is the question a host actually has,
// which is not whether an agent is supported but what will not work if they
// lend it.
func TestDoctorSaysWhatEachAgentKindCosts(t *testing.T) {
	out, ok := report(t, host.State{Allows: []string{"claude", "sh"}}, absent(t))
	if !ok {
		t.Errorf("a machine with nothing wrong reported a fault:\n%s", out)
	}
	for _, want := range []string{
		"claude", "--continue",
		// An agent kolo knows nothing about runs, and the report says what that
		// costs rather than calling it unsupported.
		"sh runs and is shared, but kolo cannot read its screen",
		"kinds.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// TestDoctorFindsACommandThatIsNotThere: the person who finds out otherwise is
// a member in a browser, and the person who can fix it lent the machine.
func TestDoctorFindsACommandThatIsNotThere(t *testing.T) {
	out, ok := report(t, host.State{Allows: []string{"definitely-not-installed-xyz"}}, absent(t))
	if ok {
		t.Errorf("a command that cannot be started passed:\n%s", out)
	}
	if !strings.Contains(out, "is not on PATH") {
		t.Errorf("the report does not say why:\n%s", out)
	}
}

// TestDoctorNoticesMarkersThatStoppedFitting is what the whole command is for.
// A CLI that shipped a new footer breaks nothing that says so — until somebody
// presses stop and nothing happens.
func TestDoctorNoticesMarkersThatStoppedFitting(t *testing.T) {
	long := time.Now().Add(-3 * 24 * time.Hour)
	out, ok := report(t, host.State{
		Allows: []string{"claude"},
		Agents: []host.Record{agent("releases", "/work/api", "claude", "unknown", long)},
	}, absent(t))
	if ok {
		t.Errorf("an agent nothing could be read from passed:\n%s", out)
	}
	for _, want := range []string{"releases", "said nothing kolo understands", "3 days", "claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// TestDoctorLetsAnAgentStart: one that has drawn nothing yet has said nothing
// yet, which is not a fault.
func TestDoctorLetsAnAgentStart(t *testing.T) {
	out, ok := report(t, host.State{
		Allows: []string{"claude"},
		Agents: []host.Record{agent("checkups", "/work/api", "claude", "unknown", time.Now())},
	}, absent(t))
	if !ok {
		t.Errorf("an agent that had just started was reported as a fault:\n%s", out)
	}
	if !strings.Contains(out, "starting") {
		t.Errorf("the report does not say it is starting:\n%s", out)
	}
}

// TestDoctorReportsWhatAgentsAreDoing, for the ones it can read.
func TestDoctorReportsWhatAgentsAreDoing(t *testing.T) {
	out, ok := report(t, host.State{
		Allows: []string{"claude"},
		Agents: []host.Record{agent("checkups", "/work/api", "claude", "busy", time.Now().Add(-90*time.Minute))},
	}, absent(t))
	if !ok {
		t.Errorf("a healthy machine reported a fault:\n%s", out)
	}
	if !strings.Contains(out, "busy") || !strings.Contains(out, "1 hour") {
		t.Errorf("the report does not say what it is doing, or since when:\n%s", out)
	}
}

// TestDoctorRefusesAKindsFileTheHostWouldRefuse: the file is the diagnosis, and
// saying so beats a report about a machine that will not start.
func TestDoctorRefusesAKindsFileTheHostWouldRefuse(t *testing.T) {
	kinds := filepath.Join(t.TempDir(), "kinds.json")
	if err := os.WriteFile(kinds, []byte(`{"robo": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, ok := report(t, host.State{Allows: []string{"claude"}}, kinds)
	if ok {
		t.Errorf("a kinds file kolo will not read passed:\n%s", out)
	}
	if !strings.Contains(out, "robo") {
		t.Errorf("the report does not name the entry:\n%s", out)
	}
}

// TestDoctorOnAMachineThatHasDoneNothing says what to do rather than printing
// empty headings at somebody who has just installed kolo.
func TestDoctorOnAMachineThatHasDoneNothing(t *testing.T) {
	var out strings.Builder
	ok, err := doctor(&out, absent(t), absent(t))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("a fresh machine was reported as a fault:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "kolo up") {
		t.Errorf("the report does not say what to do:\n%s", out.String())
	}
}
