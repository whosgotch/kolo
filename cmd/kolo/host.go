package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/whosgotch/kolo/internal/host"
)

// hostCmd lends this machine to the org. Whoever runs it is not a participant:
// it takes no input and draws nothing.
func hostCmd(args []string) error {
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	var dirs, allow list
	fs.Var(&dirs, "dir", "a directory the org may run agents in (repeat for more)")
	fs.Var(&allow, "allow", "an agent command the org may run (repeat, or comma-separated)")
	hubURL := fs.String("hub", os.Getenv("KOLO_HUB"), "hub to join (default $KOLO_HUB)")
	token := fs.String("token", os.Getenv("KOLO_TOKEN"), "this machine's token (default $KOLO_TOKEN)")
	state := fs.String("state", defaultState(), "where to record the agents running here")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo host -dir <path> [-dir <path>...] -allow <command>")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nThese flags bound what the org may start here, not what a running")
		fmt.Fprintln(os.Stderr, "agent may reach: it has this user's whole account. Run a host as a")
		fmt.Fprintln(os.Stderr, "user that owns only what the org should have.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case *hubURL == "" || *token == "":
		return fmt.Errorf("-hub and -token are both needed (or $KOLO_HUB and $KOLO_TOKEN)")
	case len(dirs) == 0:
		return fmt.Errorf("-dir is needed: a host that lends no directory can run nothing")
	case len(allow) == 0:
		return fmt.Errorf("-allow is needed: say which agent commands the org may run, such as -allow claude")
	}

	// Checked here, so a typo is a refusal at startup rather than every create
	// failing later for a reason nobody can see.
	for i, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return fmt.Errorf("-dir %s: %w", d, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("-dir %s: %w", d, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("-dir %s is not a directory", d)
		}
		dirs[i] = abs
	}

	cfg := host.Config{
		Hub:     *hubURL,
		Token:   *token,
		Dirs:    dirs,
		Allow:   allow,
		Version: version,
	}
	agents := host.NewAgents(cfg, *state)
	defer agents.StopAll()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("lending %s, running %s", strings.Join(dirs, " "), strings.Join(allow, " "))

	// Before the hub is reached, so that agents come back whether or not it is
	// up. They are the org's, not the connection's.
	if err := agents.Restore(); err != nil {
		log.Print(err)
	}
	if names := agents.Names(); len(names) > 0 {
		log.Printf("brought back %s", strings.Join(names, " "))
	}
	host.Run(ctx, agents, func(e host.Event) {
		switch {
		case e.Connected:
			log.Printf("joined %s as %s", e.Org, e.Host)
		case e.Err != nil:
			log.Printf("%v; retrying in %s", e.Err, e.Retry.Round(100_000_000))
		}
	})
	return nil
}

// list is a flag that may be given more than once, and may carry a
// comma-separated set each time.
type list []string

func (l *list) String() string { return strings.Join(*l, ",") }

func (l *list) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

// defaultState is where a host records what it is running: the user's config
// directory, because a host is started from wherever and must find it again.
func defaultState() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "kolo", "agents.json")
}
