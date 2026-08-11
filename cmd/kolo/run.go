package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/whosgotch/kolo/internal/agent"
	"github.com/whosgotch/kolo/internal/link"
	"github.com/whosgotch/kolo/internal/relay"
	"github.com/whosgotch/kolo/internal/server"
	"github.com/whosgotch/kolo/internal/session"
	hostterm "golang.org/x/term"
)

// fallbackCols and fallbackRows are used when the host's terminal has no size
// to report, which happens when kolo's own output is redirected.
const (
	fallbackCols = 120
	fallbackRows = 40
)

// runCmd runs an agent on this machine.
//
// The agent is the host's own: their terminal, their files, their permissions.
// Joining a hub adds the org's knowledge of it, not anyone's control over it.
func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	port := fs.Int("port", 0, "port for the viewer (0 picks a free one)")
	lan := fs.Bool("lan", false, "let other machines on your network open the session")
	hubURL := fs.String("hub", os.Getenv("KOLO_HUB"), "hub to join (default $KOLO_HUB)")
	token := fs.String("token", os.Getenv("KOLO_TOKEN"), "member token (default $KOLO_TOKEN)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo run [flags] <agent> [args...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	// Checked before anything starts. Half a configuration used to be caught
	// after the agent was running and the viewer had printed its address, which
	// killed a session that looked like it had come up.
	if (*hubURL == "") != (*token == "") {
		missing, other := "-token (or $KOLO_TOKEN)", "-hub"
		if *hubURL == "" {
			missing, other = "-hub (or $KOLO_HUB)", "-token"
		}
		return fmt.Errorf("%s is not set, and %s is: joining a hub needs both", missing, other)
	}
	return run(argv, *port, *lan, *hubURL, *token)
}

func run(argv []string, port int, lan bool, hubURL, token string) error {
	cols, rows := hostSize()

	a, err := agent.Start(argv, cols, rows)
	if err != nil {
		return err
	}
	defer a.Close()

	live := session.New(cols, rows)

	// Guests' lines go to the queue, never straight to the agent. The queue
	// asks the hub what is on screen and releases a line only while the agent
	// is idle at its input box (internal/relay, internal/detect).
	guests := relay.New(a, live.State)
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	srv, err := server.Listen(live, host, port, func(nickname, text string) error {
		m, err := guests.Submit(nickname, text)
		if err != nil {
			return err
		}
		live.Announce(queued{"queued", m.ID, m.Nickname, m.Text, len(guests.Pending())})
		return nil
	})
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
	if lan {
		// Said plainly, because the link is the whole of the access control and
		// it is travelling over a network in the clear.
		fmt.Println("  anyone on your network who has that link can watch this agent and send it messages")
	}

	if hubURL != "" && token != "" {
		stop := joinHub(hubURL, token, argv[0])
		defer stop()
	}

	restore, err := rawMode()
	if err != nil {
		return err
	}
	defer restore()

	stopResize := watchResize(a, live)
	defer stopResize()

	stopRelay := watchQueue(guests, live)
	defer stopRelay()

	// The host's keystrokes go straight through. This goroutine outlives the
	// function: a read already blocked on the terminal cannot be cancelled, and
	// the process is exiting anyway.
	go io.Copy(a, os.Stdin)

	// Fan the agent's output out to the host and to the virtual terminal. This
	// returns when the agent exits and its PTY closes, which a PTY master
	// reports as an error rather than EOF, so the error is the ordinary path.
	io.Copy(io.MultiWriter(os.Stdout, live), a)

	// The agent's own non-zero exit is not kolo failing.
	var exit *exec.ExitError
	if err := a.Wait(); err != nil && !errors.As(err, &exit) {
		return err
	}
	return nil
}

// joinHub registers this agent with the org for as long as it runs. It is
// deliberately not fatal: the agent is useful to the person at the keyboard
// whether or not the org can see it, so a hub that is unreachable is reported
// and retried rather than allowed to stop the session.
func joinHub(hubURL, token, agentName string) (stop func()) {
	machine, err := os.Hostname()
	if err != nil {
		machine = "unknown"
	}
	ctx, cancel := context.WithCancel(context.Background())
	go link.Run(ctx, link.Config{
		Hub:     hubURL,
		Token:   token,
		Agent:   agentName,
		Machine: machine,
		Version: version,
	}, func(e link.Event) {
		switch {
		case e.Connected:
			log.Printf("joined %s as %s", e.Org, e.Member.ID)
		case e.Err != nil:
			log.Printf("hub: %v; retrying in %s", e.Err, e.Retry.Round(time.Second))
		}
	})
	return cancel
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

// queued and sent are what a guest is told about their own message. A guest
// whose line is being held should see that it is waiting rather than watch it
// vanish, which is what happened in Milestone 0 (docs/probe-findings.md #5).
type queued struct {
	Type     string `json:"type"`
	ID       int    `json:"id"`
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
	Pending  int    `json:"pending"`
}

type sent struct {
	Type    string `json:"type"`
	ID      int    `json:"id"`
	Pending int    `json:"pending"`
}

// watchQueue gives the queue a chance to release a line, often enough to feel
// immediate and rarely enough to cost nothing.
//
// Polling is the honest mechanism here: what the queue waits on is the agent's
// screen settling back to its input box, and no event announces that.
func watchQueue(guests *relay.Relay, live *session.Session) (stop func()) {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				m, err := guests.Tick()
				if err != nil {
					log.Printf("relay: %v", err)
					continue
				}
				if m != nil {
					live.Announce(sent{"sent", m.ID, len(guests.Pending())})
				}
			}
		}
	}()
	return func() { close(done) }
}

// watchResize keeps the agent and the virtual terminal the same size as the
// host's, so that what a viewer is shown matches what the host sees.
func watchResize(a *agent.Agent, live *session.Session) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			cols, rows := hostSize()
			if err := a.Resize(cols, rows); err != nil {
				log.Printf("resize: %v", err)
			}
			live.Resize(cols, rows)
		}
	}()
	return func() { signal.Stop(ch) }
}
