// Command kolorec records an agent session for use as a test fixture.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/term"
)

const (
	cols = 120
	rows = 40
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("kolorec: ")

	script := flag.String("script", "", "script to follow")
	out := flag.String("out", "recordings", "directory for the recording")
	dir := flag.String("dir", "", "working directory for the agent")
	flag.Parse()

	if *script == "" || flag.NArg() == 0 {
		log.Fatal("usage: kolorec -script <file> [-out dir] [-dir workdir] <agent> [args...]")
	}
	steps, err := parseScript(*script)
	if err != nil {
		log.Fatal(err)
	}

	// Resolved before chdir, so -out stays relative to the invocation dir.
	outDir, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	if *dir != "" {
		if err := os.Chdir(*dir); err != nil {
			log.Fatal(err)
		}
	}
	if err := run(steps, *script, outDir, flag.Args()); err != nil {
		log.Fatal(err)
	}
}

type step struct {
	verb string
	arg  string
	dur  time.Duration
}

// parseScript parses the script verbs: wait, send, type, key, waitfor, dump.
// Blank lines and # comments are ignored.
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
		case "send", "type", "key", "dump":
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
				return nil, fmt.Errorf("line %d: waitfor needs text to wait for", line)
			}
			steps = append(steps, step{verb: verb, dur: d, arg: strings.TrimSpace(needle)})
		default:
			return nil, fmt.Errorf("line %d: unknown verb %q", line, verb)
		}
	}
	return steps, sc.Err()
}

// Control sequences kolo never exposes to a guest.
var keys = map[string]string{
	"enter": "\r",
	"esc":   "\x1b",
	"up":    "\x1b[A",
	"down":  "\x1b[B",
}

func run(steps []step, script, out string, argv []string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(script), filepath.Ext(script))

	a, err := agent.Start(argv, "", cols, rows)
	if err != nil {
		return err
	}
	defer a.Close()

	raw, err := os.Create(filepath.Join(out, name+".raw"))
	if err != nil {
		return err
	}
	defer raw.Close()

	screen := term.New(cols, rows)
	go io.Copy(io.MultiWriter(raw, screen), a)

	log.Printf("recording %s: %s, %d steps", name, strings.Join(argv, " "), len(steps))
	for i, s := range steps {
		if err := do(a, screen, out, name, i, s); err != nil {
			return fmt.Errorf("step %d (%s): %w", i, s.verb, err)
		}
	}
	log.Printf("done, %s.raw written", name)
	return nil
}

func do(a *agent.Agent, screen *term.Screen, out, name string, i int, s step) error {
	switch s.verb {
	case "wait":
		log.Printf("  [%02d] wait %s", i, s.dur)
		time.Sleep(s.dur)

	case "send":
		// Text and Enter as separate writes: bundled they read as a paste and
		// never submit.
		log.Printf("  [%02d] send %q", i, s.arg)
		if _, err := a.Write([]byte(s.arg)); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		_, err := a.Write([]byte("\r"))
		return err

	case "type":
		log.Printf("  [%02d] type %q", i, s.arg)
		_, err := a.Write([]byte(s.arg))
		return err

	case "key":
		seq, ok := keys[s.arg]
		if !ok {
			return fmt.Errorf("unknown key %q", s.arg)
		}
		log.Printf("  [%02d] key %s", i, s.arg)
		_, err := a.Write([]byte(seq))
		return err

	case "waitfor":
		log.Printf("  [%02d] waitfor %q (up to %s)", i, s.arg, s.dur)
		if !waitFor(screen, s.arg, s.dur) {
			return fmt.Errorf("never saw %q", s.arg)
		}

	case "dump":
		path := filepath.Join(out, fmt.Sprintf("%s.%s.screen", name, s.arg))
		log.Printf("  [%02d] dump %s", i, s.arg)
		return os.WriteFile(path, []byte(screen.Text()), 0o644)
	}
	return nil
}

func waitFor(screen *term.Screen, needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(screen.Text(), needle) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
