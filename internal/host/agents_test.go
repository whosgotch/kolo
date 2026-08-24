package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
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
		{"a directory in use", "other", dir, "cat", "one agent to a directory"},
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
	var written State
	if err := json.Unmarshal(b, &written); err != nil {
		t.Fatal(err)
	}
	if len(written.Agents) != 1 || written.Agents[0].Spec.CreatedBy.ID != "artem" {
		t.Fatalf("wrote %+v; who asked for it has to survive too", written)
	}
	// What the machine was started with, so kolo doctor does not have to be
	// handed the flags kolo up was given.
	if !slices.Equal(written.Lends, []string{dir}) || !slices.Equal(written.Allows, []string{"cat"}) {
		t.Errorf("wrote %+v; what the machine lends has to survive too", written)
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

func inputOf(t *testing.T, a *Agents, name string) *relay.Relay {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok {
		t.Fatalf("%s is not running", name)
	}
	return p.input
}

func TestSendingToAnAgentThatIsNotHere(t *testing.T) {
	a, _ := agentsFixture(t)
	if err := a.Type("nothing", "x"); err == nil {
		t.Error("accepted keystrokes for an agent that is not running")
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

// TestAQuestionIsAnsweredByTypingAtIt: kolo read the choices off the screen and
// pressed one on a member's behalf once. It does not any more — the question is
// the agent's, and the member holding the keyboard presses the key themselves,
// looking at the screen it lands on.
func TestAQuestionIsAnsweredByTypingAtIt(t *testing.T) {
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
	// The room is still told a question is up. What it is, and what to press, is
	// on the screen everybody can see.
	waitFor(t, func() bool { return screenOf(t, a, "checkups").State() == detect.Dialog })

	if err := a.Type("checkups", "2"); err != nil {
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

// TestAnAgentKeepsTheFlagsItWasLentWith: what a host allows is a whole command
// line, so an agent comes back the way the host started it and the resume flag
// goes after rather than instead.
func TestAnAgentKeepsTheFlagsItWasLentWith(t *testing.T) {
	defer quickRestarts()()
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "claude", `printf 'args [%s]\r\n? for shortcuts\r\n' "$*"
sleep 30
`)
	command := script + " --model opus"
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{command}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, command)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	waitFor(t, func() bool {
		return strings.Contains(screenOf(t, a, "checkups").Text(), "args [--model opus --session-id")
	})

	bounce(t, a, "checkups", a.Restart, fmt.Sprintf("args [--model opus --resume %s]", sessionOf(t, a, "checkups")))
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
	// A new agent of a pinned kind starts with an identity kolo minted for it,
	// not with nothing.
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "args [--session-id") })
	minted := sessionOf(t, a, "checkups")

	bounce(t, a, "checkups", a.Restart, "args [--resume "+minted+"]")
	if got := sessionOf(t, a, "checkups"); got != minted {
		t.Errorf("a restart changed the identity: %s became %s", minted, got)
	}
	// Starting fresh drops the conversation, and mints the next one — so what
	// comes back is new, rather than whatever ran here last.
	bounce(t, a, "checkups", a.Fresh, "args [--session-id")
	if got := sessionOf(t, a, "checkups"); got == "" || got == minted {
		t.Errorf("a fresh start did not mint a new identity: %q", got)
	}
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
	script := fakeAgentNamed(t, dir, "claude", `case "$*" in *--resume*) exit 1 ;; esac
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

// robo is an agent kind that resumes by naming a conversation rather than by
// asking for the last one, described the way a host describes one.
func robo(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kinds.json")
	body := `{"robo": {"markers": {"idle": ["type a message"], "busy": "esc to interrupt"},
		"resume": ["--resume", "{session}"], "session": "session: ([0-9a-z-]+)"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Load(path); err != nil {
		t.Fatal(err)
	}
}

// sessionOf is the conversation this machine has taken down for an agent.
func sessionOf(t *testing.T, a *Agents, name string) string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.running[name]
	if !ok {
		t.Fatalf("%s is not running", name)
	}
	return p.session
}

// TestAnAgentIsResumedByName: the id is read off the agent's own screen, kept,
// and put back on the command line at a restart. Starting fresh drops it, or
// the conversation somebody just cleared comes back at the next restart.
func TestAnAgentIsResumedByName(t *testing.T) {
	defer quickRestarts()()
	robo(t)
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "robo", `printf 'args [%s]\r\nsession: 9f3c-11ab\r\ntype a message\r\n' "$*"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	// A new agent starts fresh, and nothing has been read off its screen yet.
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "args []") })
	waitFor(t, func() bool { return sessionOf(t, a, "checkups") == "9f3c-11ab" })

	bounce(t, a, "checkups", a.Restart, "args [--resume 9f3c-11ab]")
	bounce(t, a, "checkups", a.Fresh, "args []")
}

// TestAConversationNobodyNamedIsNotResumed: a kind that resumes by name, and a
// run that never said which conversation it was in. A command line with a hole
// in it is worse than a fresh start that says so.
func TestAConversationNobodyNamedIsNotResumed(t *testing.T) {
	defer quickRestarts()()
	robo(t)
	dir := t.TempDir()
	script := fakeAgentNamed(t, dir, "robo", `printf 'args [%s]\r\ntype a message\r\n' "$*"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	waitFor(t, func() bool { return strings.Contains(screenOf(t, a, "checkups").Text(), "args []") })

	bounce(t, a, "checkups", a.Restart, "args []")
}

// TestTheStateFileKeepsTheConversation: the machine went away mid-conversation,
// and the process that knew which one cannot be asked.
func TestTheStateFileKeepsTheConversation(t *testing.T) {
	defer quickRestarts()()
	robo(t)
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "agents.json")
	script := fakeAgentNamed(t, dir, "robo", `printf 'args [%s]\r\nsession: 9f3c-11ab\r\ntype a message\r\n' "$*"
sleep 30
`)
	first := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, state)
	if err := first.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, first)
	// On the file rather than what the process is holding: what survives the
	// machine is what was written down, and it is written a moment later.
	waitFor(t, func() bool {
		b, err := os.ReadFile(state)
		return err == nil && strings.Contains(string(b), `"session": "9f3c-11ab"`)
	})
	first.StopAll()

	second := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, state)
	if err := second.Restore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.StopAll)
	waitFor(t, func() bool {
		return strings.Contains(screenOf(t, second, "checkups").Text(), "args [--resume 9f3c-11ab]")
	})
}

// TestTheStateFileSaysHowAScreenIsReading is what kolo doctor stands on: an
// agent whose markers do not fit it reads as unknown for its whole life, and
// nothing else in kolo would ever say so.
func TestTheStateFileSaysHowAScreenIsReading(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "agents.json")
	// Named for a kind kolo has no adapter for, so nothing matches its screen.
	script := fakeAgentNamed(t, dir, "stranger", "printf '? for shortcuts\\r\\n'\nsleep 30\n")
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{script}}, state)
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("checkups", dir, script)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)

	written, err := ReadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(written.Agents) != 1 {
		t.Fatalf("wrote %+v", written)
	}
	rec := written.Agents[0]
	if rec.State != "unknown" {
		t.Errorf("a screen nothing is known about was written down as %q", rec.State)
	}
	if rec.Since.IsZero() {
		t.Error("nothing says since when, so nobody can tell a moment from three days")
	}
}

// TestAHostThatLendsAnyCommandRunsWhatsOnItsPath: with '*' the machine runs any
// command named like one on PATH, and refuses a path or a program that is not
// there — the hub cannot know either, so this is where both are found out.
func TestAHostThatLendsAnyCommandRunsWhatsOnItsPath(t *testing.T) {
	dir := t.TempDir()
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{hub.AllowAny}}, "")

	if err := a.Start(spec("checkups", dir, "cat")); err != nil {
		t.Fatalf("a command on PATH: %v", err)
	}
	nextReport(t, a)

	for _, tc := range []struct {
		name, agent, command string
	}{
		{"a path instead of a name", "scripts", "/bin/cat"},
		{"a program that is not there", "ghost", "no-such-agent-anywhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := a.Start(spec(tc.agent, dir, tc.command))
			if err == nil || !strings.Contains(err.Error(), "does not run") {
				t.Errorf("accepted %q", tc.command)
			}
		})
	}
}

// TestAgentsThatNameTheirConversationsShareADirectory: the same rule the hub
// checks, from the machine that actually knows the kinds. Two agents of a kind
// that names its conversations work one directory; a kind that asks for "the
// last conversation here" does not join them.
func TestAgentsThatNameTheirConversationsShareADirectory(t *testing.T) {
	robo(t)
	dir := t.TempDir()
	program := fakeAgentNamed(t, dir, "robo", `printf 'session: 1111-2222\n'; sleep 30`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{program}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("one", dir, program)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	if err := a.Start(spec("two", dir, program)); err != nil {
		t.Fatalf("a second agent that names its conversation was refused: %v", err)
	}
	nextReport(t, a)
}

// TestTwoAgentsOfAPinnedKindShareADirectory: claude's arrangement, rehearsed
// with a fake. Each agent is given its own identity at birth and comes back to
// it at a restart — so neither can ever come back as the other.
func TestTwoAgentsOfAPinnedKindShareADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinds.json")
	body := `{"pind": {"markers": {"busy": "working"},
		"pin": ["--session-id", "{session}"], "resume": ["--resume", "{session}"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Load(path); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	program := fakeAgentNamed(t, dir, "pind", `printf 'args [%s]\r\nworking\r\n' "$*"
sleep 30
`)
	a := NewAgents(Config{Dirs: []string{dir}, Allow: []string{program}}, "")
	t.Cleanup(a.StopAll)

	if err := a.Start(spec("one", dir, program)); err != nil {
		t.Fatal(err)
	}
	nextReport(t, a)
	if err := a.Start(spec("two", dir, program)); err != nil {
		t.Fatalf("a second agent of a pinned kind was refused: %v", err)
	}
	nextReport(t, a)

	waitFor(t, func() bool { return sessionOf(t, a, "one") != "" && sessionOf(t, a, "two") != "" })
	first, second := sessionOf(t, a, "one"), sessionOf(t, a, "two")
	if first == second {
		t.Fatalf("both agents were handed the same conversation: %s", first)
	}

	bounce(t, a, "two", a.Restart, "args [--resume "+second+"]")
	if got := sessionOf(t, a, "two"); got != second {
		t.Errorf("a restart changed two's identity: %s became %s", second, got)
	}
}
