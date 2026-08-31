package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/whosgotch/kolo/internal/host"
)

func TestEveryCommandIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range append(append([]string{}, everyday...), separately...) {
		if listed[name] {
			t.Errorf("%s is listed twice", name)
		}
		listed[name] = true
	}
	for name := range commands {
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

// Help is read in an 80-column terminal.
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

func TestOverviewLeadsWithUp(t *testing.T) {
	var b bytes.Buffer
	overview(&b)
	if !strings.Contains(b.String(), "kolo up") {
		t.Error("the overview does not mention kolo up")
	}
}

// One rule about everything kolo says, so it is tested in one place even
// though half of what it reaches lives in doctor.go: a reader of this text
// installed a binary and has no checkout, so a bare docs/reference.md sends
// them to a file that is not on their machine.
func TestTheDocsAreALinkNotAPathOnSomebodyElsesMachine(t *testing.T) {
	var overviewText bytes.Buffer
	overview(&overviewText)

	installed(t, "sh", "cat", "env")
	doctorText, _ := report(t, host.State{Allows: []string{"sh", "cat", "env"}}, absent(t))

	texts := map[string]string{"(overview)": overviewText.String(), "(doctor)": doctorText}
	for name, long := range longHelp {
		texts[name] = long
	}
	for name, text := range texts {
		// The link itself contains the path, so it goes before the search.
		if bare := strings.ReplaceAll(text, referenceURL, ""); strings.Contains(bare, "docs/reference.md") {
			t.Errorf("kolo %s names docs/reference.md as a path rather than %s:\n%s", name, referenceURL, text)
		}
	}
}
