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

func TestDoctorSaysWhatEachAgentKindCosts(t *testing.T) {
	// The file kolo names as the place to describe an agent is the one it
	// was told to read, not the default path written into a sentence.
	kinds := filepath.Join(t.TempDir(), "kinds.json")
	out, ok := report(t, host.State{Allows: []string{"claude", "sh"}}, kinds)
	if !ok {
		t.Errorf("a machine with nothing wrong reported a fault:\n%s", out)
	}
	for _, want := range []string{
		"claude", "--resume {session}",
		"sh", "watch and type only",
		// Wrapped prose is asserted a word at a time: where the lines fall
		// depends on how long the agent names are.
		"browser", kinds,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// The cost of an unknown agent is explained once, naming them, however many
// there are: the same paragraph under each was most of what made the report
// hard to read.
func TestDoctorExplainsUnknownAgentsOnce(t *testing.T) {
	out, ok := report(t, host.State{Allows: []string{"sh", "cat", "env"}}, absent(t))
	if !ok {
		t.Errorf("agents kolo cannot read are a limit, not a fault:\n%s", out)
	}
	if n := strings.Count(out, "browser"); n != 1 {
		t.Errorf("the explanation appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "sh, cat and env") {
		t.Errorf("the report does not name them together:\n%s", out)
	}
}

func TestDoctorFindsACommandThatIsNotThere(t *testing.T) {
	out, ok := report(t, host.State{Allows: []string{"definitely-not-installed-xyz"}}, absent(t))
	if ok {
		t.Errorf("a command that cannot be started passed:\n%s", out)
	}
	if !strings.Contains(out, "is not on PATH") {
		t.Errorf("the report does not say why:\n%s", out)
	}
}

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

// A kind nobody described never reads as anything, so time passing is not
// evidence of a fault: lends already called it a limit, and a setup script
// that ends in kolo doctor must not fail for lending a plain shell.
func TestDoctorDoesNotFaultAnAgentItWasNeverGoingToRead(t *testing.T) {
	long := time.Now().Add(-3 * 24 * time.Hour)
	out, ok := report(t, host.State{
		Allows: []string{"sh"},
		Agents: []host.Record{agent("errands", "/work/api", "sh", "unknown", long)},
	}, absent(t))
	if !ok {
		t.Errorf("an agent kolo never claimed to read was reported as a fault:\n%s", out)
	}
	if strings.Contains(out, "said nothing kolo understands") {
		t.Errorf("the report blames markers a kind that has none:\n%s", out)
	}
	if !strings.Contains(out, "does not read this kind") {
		t.Errorf("the report does not say why its screen is unread:\n%s", out)
	}
}
