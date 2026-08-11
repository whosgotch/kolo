// Command kolo runs a CLI AI agent and makes its terminal watchable.
//
//	kolo claude
//
// The host uses the agent exactly as they would without kolo: keystrokes reach
// it, its output reaches the host's terminal. Alongside that, every byte the
// agent writes also feeds a virtual terminal, which is what a viewer joining
// later is caught up from.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/server"
	hostterm "golang.org/x/term"
)

// fallbackCols and fallbackRows are used when the host's terminal has no size
// to report, which happens when kolo's own output is redirected.
const (
	fallbackCols = 120
	fallbackRows = 40
)

// port 0 asks the operating system for a free one, which keeps a second kolo
// session from colliding with the first.
var port = flag.Int("port", 0, "localhost port for the viewer")

func main() {
	log.SetFlags(0)
	log.SetPrefix("kolo: ")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo [flags] <agent> [args...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	argv := flag.Args()
	if len(argv) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(argv); err != nil {
		log.Fatal(err)
	}
}

func run(argv []string) error {
	cols, rows := hostSize()

	a, err := agent.Start(argv, cols, rows)
	if err != nil {
		return err
	}
	defer a.Close()

	hub := server.NewHub(cols, rows)
	srv, err := server.Listen(hub, *port)
	if err != nil {
		return err
	}
	defer srv.Close()
	go func() {
		if err := srv.Serve(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	// Printed before raw mode, while the terminal still ends lines by itself.
	fmt.Printf("session live: %s\n", srv.URL())

	restore, err := rawMode()
	if err != nil {
		return err
	}
	defer restore()

	stopResize := watchResize(a, hub)
	defer stopResize()

	// The host's keystrokes go straight through. This goroutine outlives the
	// function: a read already blocked on the terminal cannot be cancelled, and
	// the process is exiting anyway.
	go io.Copy(a, os.Stdin)

	// Fan the agent's output out to the host and to the virtual terminal. This
	// returns when the agent exits and its PTY closes, which a PTY master
	// reports as an error rather than EOF, so the error is the ordinary path.
	io.Copy(io.MultiWriter(os.Stdout, hub), a)

	// The agent's own non-zero exit is not kolo failing.
	var exit *exec.ExitError
	if err := a.Wait(); err != nil && !errors.As(err, &exit) {
		return err
	}
	return nil
}

// rawMode hands the host's terminal to the agent: no echo, no line buffering,
// and no signals raised from keystrokes, so that a Ctrl-C is delivered to the
// agent as a byte instead of killing kolo out from under it.
//
// It is a no-op when stdin is not a terminal, which keeps kolo usable in a pipe.
func rawMode() (restore func(), err error) {
	fd := int(os.Stdin.Fd())
	if !hostterm.IsTerminal(fd) {
		return func() {}, nil
	}
	prev, err := hostterm.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { hostterm.Restore(fd, prev) }, nil
}

func hostSize() (cols, rows int) {
	cols, rows, err := hostterm.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		return fallbackCols, fallbackRows
	}
	return cols, rows
}

// watchResize keeps the agent and the virtual terminal the same size as the
// host's, so that what a viewer is shown matches what the host sees.
func watchResize(a *agent.Agent, hub *server.Hub) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			cols, rows := hostSize()
			if err := a.Resize(cols, rows); err != nil {
				log.Printf("resize: %v", err)
			}
			hub.Resize(cols, rows)
		}
	}()
	return func() { signal.Stop(ch) }
}
