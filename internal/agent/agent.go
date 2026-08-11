// Package agent runs a CLI agent under a pseudo-terminal.
//
// The agent believes it owns a real terminal: it is what draws the TUI that
// viewers end up watching. Everything the process writes comes back out of
// Read; everything written to it arrives as if typed.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// scrubbed environment variables, and why.
//
// COLORTERM makes the agent emit 24-bit colour, which vt10x packs into the same
// integer space as its palette indices — rgb(0,0,200) becomes indistinguishable
// from palette 200. Removing it keeps the agent on 256 colours, where the
// snapshot encoder is lossless (see internal/term.writeColor).
//
// CLAUDE_CODE_CHILD_SESSION is inherited when kolo is itself run from inside an
// agent session. It disables the child's transcript saving and puts a warning in
// its footer, neither of which the host asked for (docs/probe-findings.md,
// incidental #2).
var scrubbed = []string{"COLORTERM", "CLAUDE_CODE_CHILD_SESSION"}

// Agent is a CLI agent running under a PTY.
type Agent struct {
	cmd *exec.Cmd
	pty *os.File

	// mu makes each write indivisible. Nothing may interleave inside one, or a
	// line arrives at the agent split down the middle. In Milestone 1 the host's
	// keystrokes are the only writer; guest messages join them here later, as do
	// any emulator replies if the emulator is ever changed for one that answers
	// capability queries (docs/probe-findings.md #2, #3).
	mu sync.Mutex
}

// Start launches argv under a PTY of the given size.
func Start(argv []string, cols, rows int) (*Agent, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("agent: no command given")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = childEnv(os.Environ())

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("agent: start %s: %w", argv[0], err)
	}
	return &Agent{cmd: cmd, pty: f}, nil
}

// Read returns output from the agent. It reports io.EOF once the agent exits.
func (a *Agent) Read(p []byte) (int, error) { return a.pty.Read(p) }

// Write sends input to the agent as if it were typed. The write is indivisible;
// callers still choose what belongs in one.
func (a *Agent) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pty.Write(p)
}

// Resize tells the agent its terminal changed size, so it redraws to fit.
func (a *Agent) Resize(cols, rows int) error {
	return pty.Setsize(a.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Wait blocks until the agent exits.
func (a *Agent) Wait() error { return a.cmd.Wait() }

// Close kills the agent if it is still running and releases the PTY, which ends
// any in-flight Read.
func (a *Agent) Close() error {
	if a.cmd.Process != nil {
		a.cmd.Process.Kill()
	}
	return a.pty.Close()
}

// childEnv returns env with the scrubbed variables removed and TERM pinned to
// what the emulator implements, so the agent never emits sequences the snapshot
// cannot reproduce.
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
