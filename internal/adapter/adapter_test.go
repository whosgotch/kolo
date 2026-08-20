package adapter

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
