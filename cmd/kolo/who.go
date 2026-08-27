package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/hub"
)

func whoCmd(args []string) error {
	fs := flag.NewFlagSet("who", flag.ExitOnError)
	orgPath := fs.String("org", config.Path("org.json"), "org file to read")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo who [-org <path>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	org, err := hub.Load(*orgPath)
	if err != nil {
		return missingOrg(err, *orgPath)
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(out, "%s\n\n", org.Name)

	fmt.Fprintf(out, "members\t%d\n", len(org.Members))
	for _, m := range org.Members {
		fmt.Fprintf(out, "  %s\t%s\t%s\n", m.ID, m.Name, joinedHow(m))
	}
	if len(org.Members) == 0 {
		fmt.Fprintln(out, "  (nobody yet: send them an invite)")
	}

	fmt.Fprintf(out, "\nmachines\t%d\n", len(org.Hosts))
	for _, h := range org.Hosts {
		fmt.Fprintf(out, "  %s\n", h.ID)
	}

	live := org.Live(time.Now())
	fmt.Fprintf(out, "\nlinks that still work\t%d\n", len(live))
	for _, v := range live {
		fmt.Fprintf(out, "  %s\t%s\t%s\n", v.ID, usesLeft(v), "until "+v.Expires.Local().Format("Mon 2 Jan 15:04"))
	}
	if len(live) == 0 {
		fmt.Fprintln(out, "  (none: kolo invite makes one)")
	}
	if spent := len(org.Invites) - len(live); spent > 0 {
		fmt.Fprintf(out, "\n%d spent or expired, still listed in %s. kolo invite -off spent\nclears them.\n", spent, *orgPath)
	}
	return out.Flush()
}

func joinedHow(m hub.Member) string {
	if m.Joined.IsZero() {
		return "added by hand"
	}
	return fmt.Sprintf("joined %s via %s", m.Joined.Local().Format("2 Jan 15:04"), m.Via)
}

func usesLeft(v hub.Invite) string {
	if v.Uses == 0 {
		return "anyone holding it"
	}
	if v.Uses == 1 {
		return "1 use left"
	}
	return fmt.Sprintf("%d uses left", v.Uses)
}
