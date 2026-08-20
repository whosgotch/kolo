package adapter

import (
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
