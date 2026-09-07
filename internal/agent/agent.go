// Package agent runs a CLI agent under a pseudo-terminal.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// KOLO_TOKEN and KOLO_JOIN are this machine's credential, and an agent is a
// shell somebody else is typing into.
var scrubbed = []string{
	"COLORTERM",
	"CLAUDE_CODE_CHILD_SESSION",
	"KOLO_TOKEN",
	"KOLO_JOIN",
}

type Agent struct {
	cmd *exec.Cmd
	pty *os.File

	mu sync.Mutex
}

// Start launches argv under a PTY; empty dir means the current one.
func Start(argv []string, dir string, cols, rows int) (*Agent, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("agent: no command given")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = childEnv(os.Environ())

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("agent: start %s: %w", argv[0], err)
	}
	return &Agent{cmd: cmd, pty: f}, nil
}

// Read reports io.EOF once the agent exits.
func (a *Agent) Read(p []byte) (int, error) { return a.pty.Read(p) }

func (a *Agent) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pty.Write(p)
}

func (a *Agent) Resize(cols, rows int) error {
	return pty.Setsize(a.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (a *Agent) Wait() error { return a.cmd.Wait() }

// Close kills the process if it's still running, and releases the PTY,
// ending any in-flight Read.
func (a *Agent) Close() error {
	if a.cmd.Process != nil {
		a.cmd.Process.Kill()
	}
	return a.pty.Close()
}

func childEnv(env []string) []string {
	drop := make(map[string]bool, len(scrubbed)+1)
	for _, k := range scrubbed {
		drop[k] = true
	}
	drop["TERM"] = true

	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); !ok || !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, "TERM=xterm-256color")
}
