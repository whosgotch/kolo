package agent

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChildEnvScrubs(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"TERM=dumb",
		"COLORTERM=truecolor",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"COLORTERM_LIKE=keep-me", // prefix match must not scrub it
		"MALFORMED",
	}
	got := childEnv(in)

	for _, want := range []string{"PATH=/usr/bin", "COLORTERM_LIKE=keep-me", "MALFORMED", "TERM=xterm-256color"} {
		if !slices.Contains(got, want) {
			t.Errorf("childEnv dropped %q", want)
		}
	}
	for _, unwanted := range []string{"TERM=dumb", "COLORTERM=truecolor", "CLAUDE_CODE_CHILD_SESSION=1"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("childEnv kept %q", unwanted)
		}
	}
}

// readAll drains the PTY until the agent exits. A PTY master reports EIO rather
// than EOF once the child is gone, so the error ends the read either way.
func readAll(t *testing.T, a *Agent) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := a.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func TestStartAppliesChildEnv(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")

	a, err := Start([]string{"sh", "-c", `echo "term=$TERM colorterm=[$COLORTERM] child=[$CLAUDE_CODE_CHILD_SESSION]"`}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if got, want := readAll(t, a), "term=xterm-256color colorterm=[] child=[]"; !strings.Contains(got, want) {
		t.Errorf("agent environment = %q, want it to contain %q", got, want)
	}
}

func TestStartSizesTheTerminal(t *testing.T) {
	a, err := Start([]string{"sh", "-c", "stty size"}, 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if got := readAll(t, a); !strings.Contains(got, "40 120") {
		t.Errorf("stty size = %q, want rows 40 cols 120", strings.TrimSpace(got))
	}
}

func TestResize(t *testing.T) {
	a, err := Start([]string{"sh", "-c", "sleep 0.5; stty size"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	time.Sleep(100 * time.Millisecond)
	if err := a.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, a); !strings.Contains(got, "30 100") {
		t.Errorf("stty size after resize = %q, want rows 30 cols 100", strings.TrimSpace(got))
	}
}

func TestWriteReachesTheAgent(t *testing.T) {
	a, err := Start([]string{"sh", "-c", "read line; echo \"got:$line\""}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	time.Sleep(100 * time.Millisecond)
	if _, err := a.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, a); !strings.Contains(got, "got:hello") {
		t.Errorf("agent output = %q, want it to contain %q", got, "got:hello")
	}
}
