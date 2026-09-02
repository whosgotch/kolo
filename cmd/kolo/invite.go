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

func inviteCmd(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	orgPath := fs.String("org", config.Path("org.json"), "org file to record it in")
	hubURL := fs.String("hub", "", "where the people opening it will reach the hub (default where the hub said it was at its last start)")
	id := fs.String("id", standingID, "which link this is, in the org file and the log")
	days := fs.Int("days", inviteDays, "how many days a link made now works for")
	uses := fs.Int("uses", defaultUses, "how many people may use it, or 0 for anyone who has it")
	fresh := fs.Bool("new", false, "replace this link with a new one, so the old one stops working")
	listing := fs.Bool("list", false, "show every link there is, working or not, instead of one")
	var off list
	fs.Var(&off, "off", "withdraw the link with this `name` instead of showing one: repeat or comma-separate for several, all for every link, spent for the ones that no longer work")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo invite [-hub <url>]")
		fmt.Fprintln(os.Stderr, "       kolo invite -new [-days <n>] [-uses <n>]")
		fmt.Fprintln(os.Stderr, "       kolo invite -list")
		fmt.Fprintln(os.Stderr, "       kolo invite -off <name>[,<name>...]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nAn org keeps one link, called team, and kolo shows that same one every")
		fmt.Fprintln(os.Stderr, "time rather than minting another. -new is for when it has gone somewhere")
		fmt.Fprintln(os.Stderr, "it should not; -id names a second link for a second group of people.")
		fmt.Fprintln(os.Stderr, "\nAn invite can only be spent on becoming a member, and only until it")
		fmt.Fprintln(os.Stderr, "expires, which is what makes it safe to paste where a team can see it.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	org, err := hub.Load(*orgPath)
	if err != nil {
		return missingOrg(err, *orgPath)
	}
	if len(off) > 0 {
		return withdraw(*orgPath, org, off)
	}
	if *listing {
		return listInvites(org)
	}

	if *days < 1 {
		return fmt.Errorf("-days must be at least 1: an invite that has already expired is one nobody can use")
	}

	// The standing link unless it can't serve: gone, expired, spent, or made
	// before kolo kept the token and so unshowable.
	v, ok := org.Invite(*id)
	made := false
	if *fresh || !ok || !v.Showable(time.Now()) {
		expires := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
		if org, _, err = hub.SetInvite(*orgPath, *id, expires, *uses); err != nil {
			return missingOrg(err, *orgPath)
		}
		v, _ = org.Invite(*id)
		made = true
	}

	if made && ok {
		fmt.Printf("The old %s link has stopped working. This one is its replacement:\n\n", *id)
	} else {
		fmt.Printf("Send this to whoever should have an agent:\n\n")
	}
	fmt.Printf("    %s\n\n", hub.InviteURL(reachAt(*hubURL, org), v.Token))
	fmt.Printf("%s, until %s. They say what to call them and are in.\n", usesLeft(v), v.Expires.Local().Format("Mon 2 Jan 15:04"))
	fmt.Printf("Nothing to install, no token to paste.\n\n")
	fmt.Printf("Anyone holding it can use it: kolo invite -new replaces it, kolo invite\n-off %s withdraws it, and kolo who says who came through.\n", v.ID)
	return nil
}

// withdraw kills links by name, and by the two words that stand for a set of
// them: an org that ended up with a drawer full has no interest in naming
// each one.
func withdraw(orgPath string, org *hub.Org, named []string) error {
	ids, err := resolve(org, named)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Printf("Nothing to withdraw.\n")
		return nil
	}
	_, gone, err := hub.WithdrawInvites(orgPath, ids)
	if err != nil {
		return missingOrg(err, orgPath)
	}
	wrap(os.Stdout, "", fmt.Sprintf("Withdrew %s. %s working within a couple of seconds.",
		english(gone), verb(gone, "The link stops", "Those links stop")))
	fmt.Println()
	wrap(os.Stdout, "", fmt.Sprintf("Whoever already joined with %s is still a member: kolo who "+
		"says who came through which, and kolo who -remove takes one out.",
		verb(gone, "it", "them")))
	return nil
}

// resolve turns what was asked for into invite ids: a name is itself, and
// all and spent stand for a set, unless an invite is actually called that,
// in which case its own name wins.
func resolve(org *hub.Org, named []string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, name := range named {
		if _, ok := org.Invite(name); ok {
			add(name)
			continue
		}
		switch name {
		case "all":
			for _, v := range org.Invites {
				add(v.ID)
			}
		case "spent":
			for _, v := range org.Invites {
				if v.Spent(time.Now()) {
					add(v.ID)
				}
			}
		default:
			return nil, fmt.Errorf("no link called %s. kolo invite -list says which there are", name)
		}
	}
	return ids, nil
}

// listInvites is the table kolo up used to print on every start, and now
// also what an org reads before deciding which links to be rid of, so it
// shows the dead ones too, which are exactly the ones worth withdrawing.
func listInvites(org *hub.Org) error {
	if len(org.Invites) == 0 {
		fmt.Printf("No links at all. kolo invite makes one.\n")
		return nil
	}
	now := time.Now()
	dead := 0
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, v := range org.Invites {
		if v.Spent(now) {
			dead++
			fmt.Fprintf(out, "%s\tno longer works\n", v.ID)
			continue
		}
		shown := "kolo invite -id " + v.ID
		if !v.Showable(now) {
			shown = "cannot be shown again"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", v.ID, usesLeft(v),
			"until "+v.Expires.Local().Format("Mon 2 Jan 15:04"), shown)
	}
	out.Flush()

	spent := "the ones that no longer work"
	switch {
	case dead == 1:
		spent = "the one that no longer works"
	case dead > 1:
		spent = fmt.Sprintf("the %d that no longer work", dead)
	}
	fmt.Printf("\nkolo invite -off <name> withdraws one, and takes several: -off all clears\n")
	fmt.Printf("every link, -off spent %s.\n", spent)
	return nil
}
