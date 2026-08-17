package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/detect"
	"github.com/whosgotch/kolo/internal/hub"
	"github.com/whosgotch/kolo/internal/relay"
	"github.com/whosgotch/kolo/internal/session"
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

// TestStartRefuses: the hub asks, but this is the machine that would run the
// process, so this is where a refusal counts.
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

// TestAnAgentThatDiesComesBack is what long-lived means here: nobody is at this
// machine, so something else has to start it again.
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

// TestGivingUp: an agent that cannot stay up would otherwise restart for ever.
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

// fakeAgent writes a script that shows the marker the detector reads as idle and
// then waits on stdin, so the gate can be exercised without a real agent.
// The screens these scripts draw are Claude Code's, so the script is that kind:
// the markers a screen is read with come from the name of the command.
func fakeAgent(t *testing.T, dir, body string) string {
	return fakeAgentNamed(t, dir, "claude", body)
}

// fakeAgentNamed is the same, under a name of the caller's choosing. The name
// decides what kolo knows about the kind — how to read its screen, and how to
// resume it.
func fakeAgentNamed(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// screenOf is the agent's screen as everybody watching it sees it.
func screenOf(t *testing.T, a *Agents, name string) *session.Session {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok || p.live == nil {
		t.Fatalf("%s has no screen", name)
	}
	return p.live
}

func queueOf(t *testing.T, a *Agents, name string) *relay.Relay {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok {
		t.Fatalf("%s is not running", name)
	}
	return p.queue
}

// TestAMessageWaitsForAnIdleScreen is the whole point of the queue: the line is
// released only once the agent's own screen says it may be.
func TestAMessageWaitsForAnIdleScreen(t *testing.T) {
	dir := t.TempDir()
	// Busy first, then idle, so the message sits through the first state. The
	// clear matters: a real TUI redraws its footer in place, so only one marker
	// is ever on screen.
	script := fakeAgent(t, dir, "printf 'esc to interrupt\\n'\nsleep 1\nprintf '\\033[2J\\033[H? for shortcuts\\n'\ncat\n")
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	if err := a.Send("checkups", "Artem", "run the checkups"); err != nil {
		t.Fatal(err)
	}
	queue := queueOf(t, a, "checkups")
	if len(queue.Pending()) != 1 {
		t.Fatal("the message was not queued")
	}
	waitFor(t, func() bool { return len(queue.Pending()) == 0 })
}

// TestAMessageIsHeldOnAnUnrecognisedScreen: a screen the detector does not
// understand holds the queue rather than being guessed at.
func TestAMessageIsHeldOnAnUnrecognisedScreen(t *testing.T) {
	dir := t.TempDir()
	script := fakeAgent(t, dir, "cat\n")
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	if err := a.Send("checkups", "Artem", "run the checkups"); err != nil {
		t.Fatal(err)
	}
	queue := queueOf(t, a, "checkups")
	time.Sleep(10 * tick)
	if len(queue.Pending()) != 1 {
		t.Error("a message was sent to a screen nobody recognised")
	}
}

// TestAnUnknownAgentKindIsHeld: the screen is another kind's idle screen, and
// says nothing about this one. A kind kolo has no adapter for is watchable and
// never written to, however familiar its screen looks.
func TestAnUnknownAgentKindIsHeld(t *testing.T) {
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "some-other-agent", "printf '? for shortcuts\\r\\n'\ncat\n")
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	if err := a.Send("checkups", "Artem", "run the checkups"); err != nil {
		t.Fatal(err)
	}
	queue := queueOf(t, a, "checkups")
	time.Sleep(10 * tick)
	if len(queue.Pending()) != 1 {
		t.Error("a message was sent to an agent kind kolo cannot read")
	}
}

func TestSendingToAnAgentThatIsNotHere(t *testing.T) {
	a, _ := agentsFixture(t)
	if err := a.Send("nothing", "Artem", "hello"); err == nil {
		t.Error("accepted a message for an agent that is not running")
	}
	if err := a.Answer("nothing", "Artem", 1, "Yes"); err == nil {
		t.Error("accepted an answer for an agent that is not running")
	}
	if err := a.Interrupt("nothing", "Artem"); err == nil {
		t.Error("accepted an interrupt for an agent that is not running")
	}
	if err := a.Restart("nothing", "Artem"); err == nil {
		t.Error("accepted a restart for an agent that is not running")
	}
	if err := a.Fresh("nothing", "Artem"); err == nil {
		t.Error("accepted a start-fresh for an agent that is not running")
	}
}

// TestAnAnswerReachesTheDialog: a member's choice arrives as the keystroke that
// answers the question they were shown, and only while that question is up.
func TestAnAnswerReachesTheDialog(t *testing.T) {
	dir := t.TempDir()
	// Raw mode, because that is what an agent's TUI does and it is what makes a
	// single keystroke arrive without an Enter behind it.
	script := fakeAgent(t, dir, `stty raw -echo
printf ' Do you want to create note.txt?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel\r\n'
c=$(dd bs=1 count=1 2>/dev/null)
printf '\033[2J\033[Hchose %s\r\n' "$c"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	waitFor(t, func() bool { return screenOf(t, a, "checkups").State() == detect.Dialog })

	// The label the member was shown is part of the answer: one that does not
	// match belongs to a question that has been replaced.
	if err := a.Answer("checkups", "Artem", 1, "Yes, allow all edits this session"); err == nil {
		t.Error("answered a question the member was not looking at")
	}
	if err := a.Answer("checkups", "Artem", 2, "No"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "chose 2") })
}

// TestAnInterruptReachesTheAgent: Esc, and only while the agent is working.
func TestAnInterruptReachesTheAgent(t *testing.T) {
	dir := t.TempDir()
	script := fakeAgent(t, dir, `stty raw -echo
printf '✳ Levitating…\r\n❯\r\n  esc to interrupt\r\n'
c=$(dd bs=1 count=1 2>/dev/null | od -An -t o1 | tr -d ' \n')
printf '\033[2J\033[Hgot %s\r\n' "$c"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	waitFor(t, func() bool { return screenOf(t, a, "checkups").State() == detect.Busy })

	if err := a.Interrupt("checkups", "Artem"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "got 033") })

	// The agent has stopped working, so there is nothing left to interrupt.
	waitFor(t, func() bool { return a.Interrupt("checkups", "Artem") != nil })
}

// TestOnlyAKnownAgentKindResumes: resuming is per agent kind, and an agent kind
// kolo has no resume command for starts clean rather than being guessed at.
func TestOnlyAKnownAgentKindResumes(t *testing.T) {
	got := resumeArgv("/usr/local/bin/claude")
	if !slices.Equal(got, []string{"/usr/local/bin/claude", "--continue"}) {
		t.Errorf("claude resumes with %v", got)
	}
	if got := resumeArgv("/usr/bin/something-else"); got != nil {
		t.Errorf("an unknown agent kind was given %v to resume with", got)
	}
}

// TestRestartResumesAndFreshDoesNot is the difference between the two actions.
// Both replace the process; only one keeps what the org has told it.
func TestRestartResumesAndFreshDoesNot(t *testing.T) {
	defer quickRestarts()()
	dir := t.TempDir()
	// Named claude, because that is the agent kind kolo knows how to resume. The
	// script writes what it was launched with onto its own screen.
	script := fakeAgentNamed(t, dir, "claude", `printf 'args [%s]\r\n? for shortcuts\r\n' "$*"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	// A new agent starts clean; see Agents.Start.
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "args []") })

	bounce(t, a, "checkups", a.Restart, "args [--continue]")
	bounce(t, a, "checkups", a.Fresh, "args []")
}

// bounce asks for a restart of one kind and waits for the process it brings
// back, checking what that new run says it was launched with.
func bounce(t *testing.T, a *Agents, name string, ask func(string, string) error, want string) {
	t.Helper()
	was := processOf(t, a, name)
	if err := ask(name, "Artem"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return processOf(t, a, name) != was && strings.Contains(screenOf(t, a, name).Text(), want)
	})
}

// TestRestartingIsNotAFailure: somebody restarting an agent three times in a
// minute is impatient, not proof the agent cannot run.
func TestRestartingIsNotAFailure(t *testing.T) {
	defer quickRestarts()()
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "claude", "printf '? for shortcuts\\r\\n'\nsleep 30\n")
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	for range restartLimit + 1 {
		was := processOf(t, a, "checkups")
		if err := a.Restart("checkups", "Artem"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(a.Names()) == 0 || processOf(t, a, "checkups") != was })
	}
	if len(a.Names()) != 1 {
		t.Fatal("impatience was read as an agent that will not stay running")
	}
}

// TestAFailedResumeStartsFresh: the CLI upgraded, or the state is gone. Coming
// back without the conversation and saying so beats losing it silently.
func TestAFailedResumeStartsFresh(t *testing.T) {
	defer quickRestarts()()
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "claude", `case "$*" in *--continue*) exit 1 ;; esac
printf '? for shortcuts\r\n'
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	if err := a.Restart("checkups", "Artem"); err != nil {
		t.Fatal(err)
	}

	var told bool
	for range 2*restartLimit + 1 {
		r := nextReport(t, a)
		if r.Status == hub.StatusFailed {
			t.Fatalf("gave up instead of starting fresh: %+v", r)
		}
		if strings.Contains(r.Error, "could not resume") {
			told = true
			break
		}
	}
	if !told {
		t.Error("the resume was refused and nothing said so")
	}
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "? for shortcuts") })
	if len(a.Names()) != 1 {
		t.Fatalf("did not come back: %v", a.Names())
	}
}
