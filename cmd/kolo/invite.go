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
	uses := fs.Int("uses", defaultUses, "how many people may use it, or 0 for anyone who has it")
	off := fs.String("off", "", "withdraw the invite with this name instead of making one")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo invite [-hub <url>] [-days <n>] [-uses <n>]")
		fmt.Fprintln(os.Stderr, "       kolo invite -off <name>")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nAn invite can only be spent on becoming a member, and only until it")
		fmt.Fprintln(os.Stderr, "expires, which is what makes it safe to paste where a team can see it.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *off != "" {
		if _, err := hub.WithdrawInvite(*orgPath, *off); err != nil {
			return missingOrg(err, *orgPath)
		}
		fmt.Printf("Withdrew %s. The link stops working within a couple of seconds.\n\n", *off)
		fmt.Printf("Whoever already joined with it is still a member: kolo who -org %s says\n", *orgPath)
		fmt.Printf("who came through it, and removing one is deleting their line.\n")
		return nil
	}

	if *days < 1 {
		return fmt.Errorf("-days must be at least 1: an invite that has already expired is one nobody can use")
	}

	expires := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
	_, token, err := hub.AddInvite(*orgPath, *id, expires, *uses)
	if err != nil {
		return missingOrg(err, *orgPath)
	}

	fmt.Printf("Send this to the people who should have an agent:\n\n    %s\n\n", hub.InviteURL(*hubURL, token))
	fmt.Printf("%s.\n", bound(*uses, *days))
	fmt.Printf("They say what to call them and are in — nothing to install, no token\nto paste.\n\n")
	fmt.Printf("Anyone holding the link can use it, so if it goes somewhere it should not:\n\n")
	fmt.Printf("    kolo invite -org %s -off %s\n", *orgPath, *id)
	return nil
}

// bound says what stops the link, in the order somebody worries about it: how
// many people, then how long.
func bound(uses, days int) string {
	who := fmt.Sprintf("The first %d people who open it are in", uses)
	if uses == 0 {
		who = "Anyone who opens it is in"
	} else if uses == 1 {
		who = "The first person to open it is in"
	}
	if days == 1 {
		return who + ", and it stops working after a day"
	}
	return fmt.Sprintf("%s, and it stops working after %d days", who, days)
}
