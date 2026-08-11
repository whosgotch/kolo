package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/whosgotch/kolo/internal/link"
)

// whoCmd lists who is connected to the hub.
func whoCmd(args []string) error {
	fs := flag.NewFlagSet("who", flag.ExitOnError)
	hubURL := fs.String("hub", os.Getenv("KOLO_HUB"), "hub to ask (default $KOLO_HUB)")
	token := fs.String("token", os.Getenv("KOLO_TOKEN"), "member token (default $KOLO_TOKEN)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo who [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hubURL == "" || *token == "" {
		return fmt.Errorf("set -hub and -token, or $KOLO_HUB and $KOLO_TOKEN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	org, conns, err := link.Presence(ctx, link.Config{Hub: *hubURL, Token: *token})
	if err != nil {
		return err
	}
	if len(conns) == 0 {
		fmt.Printf("%s: nobody connected\n", org)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "%s\t\t\t\n", org)
	for _, c := range conns {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			c.Member.ID, c.Agent, c.Machine, since(c.Since))
	}
	return w.Flush()
}

// since renders how long a connection has been up, at the coarseness someone
// reading a list actually wants.
func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
