package main

import (
	"bytes"
	"strings"
	"testing"
)

// A command missing from the overview is one nobody finds. The list is written
// by hand, in two groups, so this is what notices when the two drift apart.
func TestEveryCommandIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range append(append([]string{}, everyday...), separately...) {
		if listed[name] {
			t.Errorf("%s is listed twice", name)
		}
		listed[name] = true
	}
	for name := range commands {
		// help itself is the thing being read, and says so at the bottom.
		if name == "help" {
			continue
		}
		if !listed[name] {
			t.Errorf("kolo %s is a command but appears in no group in kolo help", name)
		}
	}
	for name := range listed {
		if _, ok := commands[name]; !ok {
			t.Errorf("kolo help lists %s, which is not a command", name)
		}
	}
}

// The brief is one line in a list; a long one wraps and the list stops lining up.
func TestBriefsAreShort(t *testing.T) {
	for name, cmd := range commands {
		if cmd.brief == "" {
			t.Errorf("%s has no brief", name)
		}
		if len(cmd.brief) > 48 {
			t.Errorf("%s: brief is %d characters, too long for the list", name, len(cmd.brief))
		}
	}
}

// Help is read in a terminal, which is 80 columns until somebody says otherwise.
func TestHelpFitsInATerminal(t *testing.T) {
	var overviewText bytes.Buffer
	overview(&overviewText)

	texts := map[string]string{"(overview)": overviewText.String()}
	for name, long := range longHelp {
		texts[name] = long
	}
	for name, text := range texts {
		for i, line := range strings.Split(text, "\n") {
			if n := len([]rune(line)); n > 78 {
				t.Errorf("kolo help %s line %d is %d columns:\n%s", name, i+1, n, line)
			}
		}
	}
}

func TestLongHelpNamesRealCommands(t *testing.T) {
	for name := range longHelp {
		if _, ok := commands[name]; !ok {
			t.Errorf("longHelp explains %q, which is not a command", name)
		}
	}
}

// The overview is the first thing anyone sees, so it has to carry the one
// command that gets them started.
func TestOverviewLeadsWithUp(t *testing.T) {
	var b bytes.Buffer
	overview(&b)
	if !strings.Contains(b.String(), "kolo up") {
		t.Error("the overview does not mention kolo up")
	}
}
