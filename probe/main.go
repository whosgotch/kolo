// Command probe is the Milestone 0 throwaway experiment.
//
// It launches an agent CLI in a PTY, feeds the output through the candidate VT
// emulator, and drives the child with a small timed script so we can answer:
//
//  1. Does the TUI render correctly through github.com/charmbracelet/x/vt?
//  2. Does a line arriving while the agent is busy queue as a follow-up message?
//  3. What happens if a line arrives while a tool-permission dialog is on screen?
//
// This code is deliberately not reusable. It lives on probe/milestone-0 and is
// never merged.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

func main() {
	var (
		scriptPath = flag.String("script", "", "path to probe script")
		outDir     = flag.String("out", "probe-out", "directory for dumps")
		workDir    = flag.String("dir", "", "working directory for the child (default: cwd)")
		emuName    = flag.String("emu", "charm", "VT emulator: charm | vt10x")
		cols       = flag.Int("cols", 120, "terminal width")
		rows       = flag.Int("rows", 40, "terminal height")
	)
	flag.Parse()

	argv := flag.Args()
	if len(argv) == 0 {
		argv = []string{"claude"}
	}
	if *scriptPath == "" {
		fatal(errors.New("-script is required"))
	}

	steps, err := parseScript(*scriptPath)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}

	p, err := newProbe(argv, *workDir, *emuName, *cols, *rows, *outDir)
	if err != nil {
		fatal(err)
	}
	defer p.close()

	fmt.Printf("probe: %s at %dx%d, %d steps\n", strings.Join(argv, " "), *cols, *rows, len(steps))
	for i, s := range steps {
		if err := p.run(i, s); err != nil {
			fmt.Fprintf(os.Stderr, "step %d (%s): %v\n", i, s.verb, err)
			break
		}
	}

	p.dump(len(steps), "final")
	fmt.Printf("probe: done, artifacts in %s/\n", *outDir)
}

// --- script ---------------------------------------------------------------

type step struct {
	verb string // wait | send | dump | waitfor | key
	arg  string
	dur  time.Duration
}

// parseScript reads a line-based script. Blank lines and # comments are skipped.
//
//	wait 5s
//	send explain what you just did
//	waitfor 60s Do you want to
//	dump permission-dialog
//	key esc
func parseScript(path string) ([]step, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var steps []step
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		verb, rest, _ := strings.Cut(text, " ")
		rest = strings.TrimSpace(rest)

		switch verb {
		case "send", "type", "dump", "key":
			if rest == "" {
				return nil, fmt.Errorf("line %d: %s needs an argument", line, verb)
			}
			steps = append(steps, step{verb: verb, arg: rest})
		case "wait":
			d, err := time.ParseDuration(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			steps = append(steps, step{verb: verb, dur: d})
		case "waitfor":
			timeout, needle, _ := strings.Cut(rest, " ")
			d, err := time.ParseDuration(timeout)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			if strings.TrimSpace(needle) == "" {
				return nil, fmt.Errorf("line %d: waitfor needs a substring", line)
			}
			steps = append(steps, step{verb: verb, dur: d, arg: strings.TrimSpace(needle)})
		default:
			return nil, fmt.Errorf("line %d: unknown verb %q", line, verb)
		}
	}
	return steps, sc.Err()
}

// --- probe ----------------------------------------------------------------

// emulator is the slice of VT behaviour the probe needs, so the two candidates
// can be swapped with -emu.
type emulator interface {
	Write([]byte) (int, error)
	Render() string
	// Replies returns the emulator's response stream (answers to terminal
	// capability queries), or nil if it does not generate one.
	Replies() io.Reader
}

type charmEmu struct{ *vt.SafeEmulator }

func (c charmEmu) Replies() io.Reader { return c.SafeEmulator }

type vt10xEmu struct{ vt10x.Terminal }

func (v vt10xEmu) Render() string     { return v.String() }
func (v vt10xEmu) Replies() io.Reader { return nil }

type probe struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	term   emulator
	raw    *os.File
	outDir string

	mu   sync.Mutex // guards writes to ptmx; see "writes are atomic" invariant
	done chan struct{}
}

func newProbe(argv []string, workDir, emuName string, cols, rows int, outDir string) (*probe, error) {
	var term emulator
	switch emuName {
	case "charm":
		term = charmEmu{vt.NewSafeEmulator(cols, rows)}
	case "vt10x":
		term = vt10xEmu{vt10x.New(vt10x.WithSize(cols, rows))}
	default:
		return nil, fmt.Errorf("unknown emulator %q", emuName)
	}

	raw, err := os.Create(filepath.Join(outDir, "raw.log"))
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Dir = workDir

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		raw.Close()
		return nil, err
	}

	p := &probe{
		cmd:    cmd,
		ptmx:   ptmx,
		term:   term,
		raw:    raw,
		outDir: outDir,
		done:   make(chan struct{}),
	}

	go p.pump()
	go p.replies()
	return p, nil
}

// replies drains the emulator's response stream back into the child.
//
// The emulator answers terminal-capability queries (device attributes, cursor
// position, ...) by writing to an internal pipe. If nobody reads it, the
// emulator blocks mid-Write while holding its lock and the whole process
// deadlocks. Claude queries on startup, so this is not optional.
//
// Replies go through the same mutex as guest input: a reply must never land in
// the middle of a line.
func (p *probe) replies() {
	src := p.term.Replies()
	if src == nil {
		fmt.Fprintln(os.Stderr, "  <- emulator has no reply stream; child gets no answer to capability queries")
		return
	}
	buf := make([]byte, 1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.ptmx.Write(buf[:n])
			p.mu.Unlock()
			fmt.Fprintf(os.Stderr, "  <- emulator reply: %q\n", buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// pump copies child output into both the raw log and the emulator.
func (p *probe) pump() {
	defer close(p.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.raw.Write(buf[:n])
			p.term.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *probe) run(i int, s step) error {
	switch s.verb {
	case "wait":
		fmt.Printf("  [%02d] wait %s\n", i, s.dur)
		time.Sleep(s.dur)

	case "send":
		fmt.Printf("  [%02d] send %q\n", i, s.arg)
		return p.write(s.arg + "\r")

	case "type":
		// text only, no Enter — isolates what printable characters alone do
		fmt.Printf("  [%02d] type %q\n", i, s.arg)
		return p.write(s.arg)

	case "key":
		fmt.Printf("  [%02d] key %s\n", i, s.arg)
		seq, ok := keys[s.arg]
		if !ok {
			return fmt.Errorf("unknown key %q", s.arg)
		}
		return p.write(seq)

	case "dump":
		fmt.Printf("  [%02d] dump %s\n", i, s.arg)
		p.dump(i, s.arg)

	case "waitfor":
		fmt.Printf("  [%02d] waitfor %q (up to %s)\n", i, s.arg, s.dur)
		if !p.waitFor(s.arg, s.dur) {
			fmt.Printf("       ...not found, continuing\n")
		}
	}
	return nil
}

// keys are the only control sequences the probe can send. Kolo itself will never
// expose these to guests — they exist here purely to answer question 3.
var keys = map[string]string{
	"esc":   "\x1b",
	"enter": "\r",
	"up":    "\x1b[A",
	"down":  "\x1b[B",
}

// write sends bytes to the child atomically, mirroring the invariant the real
// runner has to hold.
func (p *probe) write(s string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.ptmx.WriteString(s)
	return err
}

func (p *probe) waitFor(needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(p.term.Render(), needle) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (p *probe) dump(i int, label string) {
	name := filepath.Join(p.outDir, fmt.Sprintf("%02d-%s.txt", i, label))
	body := fmt.Sprintf("--- %s @ %s ---\n%s\n",
		label, time.Now().Format(time.TimeOnly), p.term.Render())
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "dump %s: %v\n", label, err)
	}
}

func (p *probe) close() {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	p.ptmx.Close()

	waited := make(chan struct{})
	go func() { p.cmd.Wait(); close(waited) }()

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		fmt.Fprintln(os.Stderr, "probe: child did not reap, abandoning")
	}
	p.raw.Close()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "probe:", err)
	os.Exit(1)
}
