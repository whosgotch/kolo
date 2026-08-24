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

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

func hostCmd(args []string) error {
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	var dirs, allow list
	fs.Var(&dirs, "dir", "a directory the org may run agents in (repeat for more)")
	fs.Var(&allow, "allow", "an agent command line the org may run, flags and all (repeat, or comma-separated; '*' lends any command on PATH)")
	join := fs.String("join", os.Getenv("KOLO_JOIN"), "the join string the hub printed for this machine; supplies both -hub and -token (default $KOLO_JOIN)")
	hubURL := fs.String("hub", os.Getenv("KOLO_HUB"), "hub to join, if not joining with -join (default $KOLO_HUB)")
	token := fs.String("token", os.Getenv("KOLO_TOKEN"), "this machine's token, if not joining with -join (default $KOLO_TOKEN)")
	state := fs.String("state", config.Path("agents.json"), "where to record the agents running here")
	kinds := fs.String("kinds", config.Path("kinds.json"), "agent kinds to know about beyond the ones kolo ships with")
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
	if err := loadKinds(*kinds); err != nil {
		return err
	}

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

	// Before the hub is reached, so agents come back whether or not it's up
	// — they're the org's, not the connection's.
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

// list is a repeatable flag; each occurrence may be comma-separated.
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

func split(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func loadKinds(path string) error {
	added, err := adapter.Load(path)
	if err != nil {
		return err
	}
	if len(added) > 0 {
		log.Printf("agent kinds from %s: %s", path, strings.Join(added, ", "))
	}
	return nil
}
