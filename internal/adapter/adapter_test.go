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

func TestAKindIsTheBinaryNotTheCommandLine(t *testing.T) {
	for _, command := range []string{"claude", "/opt/homebrew/bin/claude", "claude --model opus"} {
		if got := For(command).Resume; !slices.Equal(got, []string{"--resume", SessionPlaceholder}) {
			t.Errorf("%q resumes with %v", command, got)
		}
	}
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
	if For("claude").Resume == nil {
		t.Error("configuring a kind lost the built-in ones")
	}
}

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

func TestLoadWithoutAFileIsFine(t *testing.T) {
	added, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || added != nil {
		t.Errorf("added %v, %v", added, err)
	}
}

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
	if got, ok := robo.ResumeArgs(""); ok {
		t.Errorf("resumed a conversation it cannot name: %v", got)
	}
	if got, ok := For("claude").ResumeArgs("9f3c-11ab"); !ok || !slices.Equal(got, []string{"--resume", "9f3c-11ab"}) {
		t.Errorf("claude resumes with %v, %v", got, ok)
	}
	if got, ok := For("claude").ResumeArgs(""); ok {
		t.Errorf("resumed without the id it pinned: %v", got)
	}
}

func TestTheLastConversationOnScreenWins(t *testing.T) {
	robo := Adapter{Resume: []string{"-r", "{session}"}, Session: `session: (\S+)`}
	if id := robo.SessionFrom("session: one\r\ncleared\r\nsession: two\r\n"); id != "two" {
		t.Errorf("read %q", id)
	}
	if id := robo.SessionFrom("nothing about a session here"); id != "" {
		t.Errorf("read %q off a screen that carries none", id)
	}
	if id := robo.SessionFrom("session: " + strings.Repeat("x", maxSession+1)); id != "" {
		t.Errorf("read %d characters as an id", len(id))
	}
}

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

// TestAKindTheHostPinsItsIdentity: an agent whose conversation is named by the
// host at birth needs no screen to read: the id is kolo's own, minted before
// the agent could say anything, and filled in wherever it is asked for.
func TestAKindTheHostPinsItsIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinds.json")
	body := `{"pinbot": {"markers": {"busy": "working"},
		"pin": ["--session-id", "{session}"], "resume": ["--resume", "{session}"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"pinbot"}) {
		t.Fatalf("Load added %v", added)
	}

	kind := For("pinbot")
	if !kind.ResumesByName() {
		t.Error("a pinned kind cannot prove which conversation is its own")
	}
	got, ok := kind.PinArgs("abc-123")
	if !ok || !slices.Equal(got, []string{"--session-id", "abc-123"}) {
		t.Errorf("PinArgs gave %v, %v", got, ok)
	}
	if got, ok := kind.ResumeArgs("abc-123"); !ok || !slices.Equal(got, []string{"--resume", "abc-123"}) {
		t.Errorf("ResumeArgs gave %v, %v", got, ok)
	}
	if got, ok := kind.ResumeArgs(""); ok {
		t.Errorf("resumed without the id it pinned: %v", got)
	}

	// And a pin that names nothing would put a hole in the command line.
	broken := filepath.Join(t.TempDir(), "kinds.json")
	if err := os.WriteFile(broken, []byte(`{"x": {"pin": ["--session-id"], "resume": ["-r", "{session}"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil {
		t.Error("a pin without {session} was accepted")
	}
}
