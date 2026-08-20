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

	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

// hostCmd lends this machine to the org. Whoever runs it is not a participant:
// it takes no input and draws nothing.
func hostCmd(args []string) error {
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	var dirs, allow list
	fs.Var(&dirs, "dir", "a directory the org may run agents in (repeat for more)")
	fs.Var(&allow, "allow", "an agent command line the org may run, flags and all (repeat, or comma-separated)")
	join := fs.String("join", os.Getenv("KOLO_JOIN"), "the join string the hub printed for this machine; supplies both -hub and -token (default $KOLO_JOIN)")
	hubURL := fs.String("hub", os.Getenv("KOLO_HUB"), "hub to join, if not joining with -join (default $KOLO_HUB)")
	token := fs.String("token", os.Getenv("KOLO_TOKEN"), "this machine's token, if not joining with -join (default $KOLO_TOKEN)")
	state := fs.String("state", config.Path("agents.json"), "where to record the agents running here")
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

	// A join string is where the hub and the token come from together, which is
	// how they were minted. The separate flags stay for a host whose two halves
	// come from somewhere else, such as a secret store.
	if *join != "" {
		reached, minted, err := hub.ParseJoin(*join)
		if err != nil {
			return err
		}
		*hubURL, *token = reached, minted
	}

	switch {
	case *hubURL == "" || *token == "":
		return fmt.Errorf("-join is needed: the string the hub printed when this machine was added\n" +
			"(or -hub and -token separately, or $KOLO_HUB and $KOLO_TOKEN)")
	case len(dirs) == 0:
		return fmt.Errorf("-dir is needed: a host that lends no directory can run nothing")
	case len(allow) == 0:
		return fmt.Errorf("-allow is needed: say which agent commands the org may run, such as -allow claude")
	}

	if err := resolveDirs(dirs); err != nil {
		return err
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

	log.Printf("lending %s, running %s", strings.Join(dirs, ", "), strings.Join(allow, ", "))

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

// resolveDirs makes every lent directory absolute in place, and refuses one
// that is not there. Checked at startup, so a typo is a refusal now rather than
// every create failing later for a reason nobody can see.
func resolveDirs(dirs list) error {
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

// split reads a flag that may carry a comma-separated set.
func split(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
