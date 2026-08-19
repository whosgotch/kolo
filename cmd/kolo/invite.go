package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/whosgotch/kolo/internal/hub"
)

// inviteCmd mints a link that turns whoever opens it into a member.
//
// It replaces minting a token per person and sending each one somewhere private.
// An org has a channel it already talks in; this is the thing to paste there.
func inviteCmd(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	orgPath := fs.String("org", "org.json", "org file to record it in")
	hubURL := fs.String("hub", defaultHubURL, "where the people opening it will reach the hub")
	id := fs.String("id", "team", "what this invite is called in the org file and the log")
	days := fs.Int("days", 7, "how many days it works for")
	uses := fs.Int("uses", 0, "how many people may use it, or 0 for as many as the window allows")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo invite [-hub <url>] [-days <n>] [-uses <n>]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nAn invite can only be spent on becoming a member, and only until it")
		fmt.Fprintln(os.Stderr, "expires, which is what makes it safe to paste where a team can see it.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days < 1 {
		return fmt.Errorf("-days must be at least 1: an invite that has already expired is one nobody can use")
	}

	expires := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
	_, token, err := hub.AddInvite(*orgPath, *id, expires, *uses)
	if err != nil {
		return missingOrg(err, *orgPath)
	}

	fmt.Printf("Send this to everyone who should have an agent. It works %s:\n\n", forDays(*days))
	fmt.Printf("    %s\n\n", hub.InviteURL(*hubURL, token))
	fmt.Println("They open it, say what to call them, and are in. Nothing to install and")
	fmt.Println("no token to paste.")
	if *uses > 0 {
		fmt.Printf("\nIt is good for %d, and stops working after that.\n", *uses)
	}
	fmt.Printf("\nThe hub already running has it: an invite is read when it is spent, not\nat startup.\n")
	return nil
}

func forDays(days int) string {
	if days == 1 {
		return "for a day"
	}
	return fmt.Sprintf("for %d days", days)
}
