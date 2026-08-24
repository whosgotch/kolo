package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/whosgotch/kolo/internal/detect"
)

// TestAKindIsTheBinaryNotTheCommandLine: a host lends a command line, and what
// kolo knows about it is decided by the program at the front of it. Neither the
// directory it was found in nor the flags it was lent with change the kind.
func TestAKindIsTheBinaryNotTheCommandLine(t *testing.T) {
	for _, command := range []string{"claude", "/opt/homebrew/bin/claude", "claude --model opus"} {
		if got := For(command).Resume; !slices.Equal(got, []string{"--continue"}) {
			t.Errorf("%q resumes with %v", command, got)
		}
	}
	// An agent kind kolo has no adapter for is inert rather than guessed at: it
	// cannot be resumed, so every restart of one is fresh.
	for _, command := range []string{"something-else", "", "   "} {
		if got := For(command).Resume; got != nil {
			t.Errorf("%q was given %v to resume with", command, got)
		}
	}
}

func TestArgvSplitsOnWhitespace(t *testing.T) {
	got := Argv("  claude   --model opus ")
	if !slices.Equal(got, []string{"claude", "--model", "opus"}) {
		t.Errorf("split into %v", got)
	}
}

// TestLoadAddsAKindKoloDoesNotShip is the point of the file: an org running an
// agent kolo has never heard of describes it rather than waiting for a release.
func TestLoadAddsAKindKoloDoesNotShip(t *testing.T) {
	defer restoreKinds()()
	path := filepath.Join(t.TempDir(), "kinds.json")
	write(t, path, `{"robo": {"markers": {"busy": "thinking", "idle": ["type a message"]}, "resume": ["-c"]}}`)

	added, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"robo"}) {
		t.Errorf("added %v", added)
	}
	if got := For("/opt/robo --fast").Resume; !slices.Equal(got, []string{"-c"}) {
		t.Errorf("robo resumes with %v", got)
	}
	if For("robo").Markers.Busy != "thinking" {
		t.Error("robo's screen is not read with robo's markers")
	}
	// The kinds kolo ships with survive a file that does not mention them.
	if For("claude").Resume == nil {
		t.Error("configuring a kind lost the built-in ones")
	}
}

// TestLoadReplacesAKindKoloDoesShip: an agent that moved its footer between
// releases is fixed on the machine running it, without one of kolo.
func TestLoadReplacesAKindKoloDoesShip(t *testing.T) {
	defer restoreKinds()()
	path := filepath.Join(t.TempDir(), "kinds.json")
	write(t, path, `{"claude": {"markers": {"busy": "esc to stop"}}}`)

	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := For("claude"); got.Markers.Busy != "esc to stop" || got.Resume != nil {
		t.Errorf("claude was merged with the built-in rather than replacing it: %+v", got)
	}
}

// TestLoadRefusesWhatWouldGoUnnoticed: a file kolo cannot read, and an entry
// that would leave the agent exactly as unreadable as saying nothing.
func TestLoadRefusesWhatWouldGoUnnoticed(t *testing.T) {
	defer restoreKinds()()
	dir := t.TempDir()
	for _, bad := range []string{`{"robo": {}}`, `{"robo": {"markers": {}}}`, `{"/bin/robo": {"resume": ["-c"]}}`, `{`} {
		path := filepath.Join(dir, "kinds.json")
		write(t, path, bad)
		if _, err := Load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// TestLoadWithoutAFileIsFine: most machines run the kinds kolo knows.
func TestLoadWithoutAFileIsFine(t *testing.T) {
	added, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || added != nil {
		t.Errorf("added %v, %v", added, err)
	}
}

// restoreKinds puts the shipped table back, Load having replaced it.
func restoreKinds() func() {
	was := kinds
	return func() { kinds = was }
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAKindThatResumesByNamingAConversation is the gap this closes: an agent
// whose resume command wants an id rather than "the last one".
func TestAKindThatResumesByNamingAConversation(t *testing.T) {
	defer restoreKinds()()
	path := filepath.Join(t.TempDir(), "kinds.json")
	write(t, path, `{"robo": {"markers": {"busy": "working"},
		"resume": ["--resume", "{session}"], "session": "session: ([0-9a-f-]+)"}}`)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	robo := For("robo")

	id := robo.SessionFrom("robo v2\r\nsession: 9f3c-11ab\r\ntype a message\r\n")
	if id != "9f3c-11ab" {
		t.Fatalf("read %q off the screen", id)
	}
	got, ok := robo.ResumeArgs(id)
	if !ok || !slices.Equal(got, []string{"--resume", "9f3c-11ab"}) {
		t.Errorf("resumes with %v, %v", got, ok)
	}
	// A kind that needs an id and has never seen one cannot resume. Starting
	// fresh and saying so beats a command line with a hole in it.
	if got, ok := robo.ResumeArgs(""); ok {
		t.Errorf("resumed a conversation it cannot name: %v", got)
	}
	// A kind that names nothing is unaffected by an id it never asked for.
	if got, ok := For("claude").ResumeArgs("9f3c-11ab"); !ok || !slices.Equal(got, []string{"--continue"}) {
		t.Errorf("claude resumes with %v, %v", got, ok)
	}
}

// TestTheLastConversationOnScreenWins: an agent told to start a new conversation
// says so on the same screen, and resuming the one before it would bring back
// what somebody just cleared.
func TestTheLastConversationOnScreenWins(t *testing.T) {
	robo := Adapter{Resume: []string{"-r", "{session}"}, Session: `session: (\S+)`}
	if id := robo.SessionFrom("session: one\r\ncleared\r\nsession: two\r\n"); id != "two" {
		t.Errorf("read %q", id)
	}
	if id := robo.SessionFrom("nothing about a session here"); id != "" {
		t.Errorf("read %q off a screen that carries none", id)
	}
	// A pattern that drags in half the screen is a pattern to fix, not an id to
	// put on a command line.
	if id := robo.SessionFrom("session: " + strings.Repeat("x", maxSession+1)); id != "" {
		t.Errorf("read %d characters as an id", len(id))
	}
}

// TestLoadRefusesAHalfDescribedSession: each half is useless without the other,
// and both fail at a restart somebody was counting on rather than at startup.
func TestLoadRefusesAHalfDescribedSession(t *testing.T) {
	defer restoreKinds()()
	dir := t.TempDir()
	for _, bad := range []string{
		`{"robo": {"resume": ["--resume", "{session}"], "markers": {"busy": "x"}}}`,
		`{"robo": {"resume": ["--continue"], "session": "id: (\\S+)", "markers": {"busy": "x"}}}`,
		`{"robo": {"resume": ["-r", "{session}"], "session": "id: (", "markers": {"busy": "x"}}}`,
		`{"robo": {"resume": ["-r", "{session}"], "session": "id: (\\S+) (\\S+)", "markers": {"busy": "x"}}}`,
	} {
		path := filepath.Join(dir, "kinds.json")
		write(t, path, bad)
		if _, err := Load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// TestTheInterruptKeyBelongsToTheKind: kolo sent Esc to everything, which is a
// key that clears the input box of an agent that stops on Ctrl-C.
func TestTheInterruptKeyBelongsToTheKind(t *testing.T) {
	for _, c := range []struct {
		named string
		sends []byte
	}{
		{"", []byte{Esc}},
		{"esc", []byte{Esc}},
		{"ESCAPE", []byte{Esc}},
		{"ctrl+c", []byte{3}},
		{"ctrl+g", []byte{7}},
		{"q", []byte("q")},
	} {
		if got := (Adapter{Interrupt: c.named}).InterruptKey(); !slices.Equal(got, c.sends) {
			t.Errorf("%q sends %v, want %v", c.named, got, c.sends)
		}
	}
}

// TestLoadRefusesAKeyNobodyCanPress: a key kolo cannot spell is a stop button
// that does nothing, found at the moment somebody needs it most.
func TestLoadRefusesAKeyNobodyCanPress(t *testing.T) {
	defer restoreKinds()()
	path := filepath.Join(t.TempDir(), "kinds.json")
	for _, bad := range []string{"ctrl+enter", "ctrl+", "control-c", "escape key"} {
		body, err := json.Marshal(map[string]Adapter{
			"robo": {Markers: detect.Markers{Busy: "working"}, Interrupt: bad},
		})
		if err != nil {
			t.Fatal(err)
		}
		write(t, path, string(body))
		if _, err := Load(path); err == nil {
			t.Errorf("%q was accepted as a key", bad)
		}
	}
}

// TestDiscoveredFindsWhatIsInstalled checks discovery against a PATH holding
// exactly one agent, so the test does not depend on what the machine running it
// happens to have. A name kolo has heard of but that is not there is not
// reported: lending an absent command helps nobody.
func TestDiscoveredFindsWhatIsInstalled(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"aider", "not-an-agent"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	got := Discovered()
	if len(got) != 1 || got[0] != "aider" {
		t.Errorf("Discovered() = %v, want [aider]", got)
	}
}
