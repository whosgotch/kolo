package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Two groups, because most people need one of them.
//
// Everything an org does day to day is the first three: start the machine, let
// people in, see who is in. The other three exist for a hub that lives somewhere
// other than the machine running the agents, which is a deployment rather than a
// way of using kolo. A list that mixes them makes the common case look like a
// choice between six things.
var (
	everyday   = []string{"up", "invite", "who"}
	separately = []string{"serve", "token", "host"}
)

// helpCmd explains kolo, or one of its commands.
func helpCmd(args []string) error {
	if len(args) == 0 {
		overview(os.Stdout)
		return nil
	}
	name := args[0]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "kolo: no command %q\n\n", name)
		overview(os.Stderr)
		os.Exit(2)
	}
	long, ok := longHelp[name]
	if !ok {
		fmt.Printf("kolo %s — %s\n\nkolo %s -h lists its flags.\n", name, cmd.brief, name)
		return nil
	}
	fmt.Printf("kolo %s — %s\n\n%s\n\nkolo %s -h lists its flags.\n", name, cmd.brief, strings.TrimSpace(long), name)
	return nil
}

// overview is what somebody who has just been handed kolo needs: what it is,
// the one command that starts it, and where to look next. Not every flag there
// is — that is what -h is for, and a wall of them is how a simple thing comes to
// look complicated.
func overview(w io.Writer) {
	fmt.Fprintln(w, "kolo — shared agents on a machine your team lends.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "On the machine you are lending:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    kolo up")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "That starts everything and prints a link. Everyone else opens the link,")
	fmt.Fprintln(w, "says what to call them, and picks an agent. Nobody installs anything.")
	fmt.Fprintln(w)

	briefs(w, "Everyday", everyday)
	fmt.Fprintln(w)
	briefs(w, "Running the hub away from the agents", separately)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "kolo help <command> for what one is for, kolo <command> -h for its flags.")
}

func briefs(w io.Writer, title string, names []string) {
	fmt.Fprintf(w, "%s:\n", title)
	for _, name := range names {
		fmt.Fprintf(w, "  %-8s %s\n", name, commands[name].brief)
	}
}

// The long half of each command's help: what it is for and when to reach for it,
// which is the part a flag list cannot say.
var longHelp = map[string]string{
	"up": `Runs the hub and lends this machine to it, in one process.

Makes whatever is missing on the way: the org file, named after the
directory it was started in; a credential for this machine, minted fresh
every start and never written down; and, the first time, an invite link
to send the team.

    cd ~/work/api
    kolo up

It lends the directory it was started in and allows whichever agent
commands kolo knows about and finds installed. -dir and -allow say
otherwise, and may be repeated.

It listens on every interface, so the org can reach it. That means a
member's token crosses the network in a header with no TLS, which is a
considered choice on a trusted network and a bad one anywhere else.
-addr 127.0.0.1:7300 keeps it to this machine.

`,

	"invite": `Makes a link that turns whoever opens it into a member.

    kolo invite
    kolo invite -id contractors -uses 3 -days 1
    kolo invite -off contractors

An invite is bounded twice: by how many people may spend it, ten unless
-uses says otherwise, and by how long it lasts, seven days unless -days
does. Neither is security on its own — whoever holds the link can spend
it, and they choose the name they appear under. The bounds are what keeps
a leak small enough to notice and undo.

-off withdraws one. It takes effect within a couple of seconds and does
not remove whoever already joined through it: they are members now, and
kolo who says which link each of them came from.

`,

	"who": `Says who is in the org, which machines are in it, and which invite
links still work.

    kolo who

Members show when they joined and which link let them in, which is the
question a link that got somewhere it should not have raises. Somebody
minted by hand with kolo token has no such record and says so.

`,

	"serve": `Runs the hub on its own, for an org whose hub is not the machine
running the agents.

    kolo serve -org org.json -addr 0.0.0.0:7300

The hub carries no TLS. Reaching it across the internet means putting it
behind something that terminates TLS, or a member's token travels in the
clear.

It reads the org file again whenever it changes, so adding somebody and
revoking them both take effect without a restart. A file that will not
parse is complained about and ignored, because an org nobody is in is a
worse answer to a typo than carrying on.

`,

	"token": `Mints one credential, for one person or one machine.

    kolo token -id dana -name "Dana"
    kolo token -host -id devbox -hub https://hub.acme.com

For most orgs kolo invite has replaced this for people: it is one link
rather than a token per person, sent somewhere private. What is left here
is a machine's credential, and a person who should not arrive by a link
anyone in a channel can open.

The token is printed once and stored nowhere. The hub keeps only its
hash, so a lost one is replaced rather than recovered.

`,

	"host": `Lends this machine to an org whose hub is somewhere else.

    kolo host -join kolo_join_… -dir ~/work/api -allow claude

The join string carries the hub and this machine's token together,
because they were minted together and are useless apart. A host keeping
its two halves elsewhere — a secret store, a unit file — can pass -hub
and -token, or $KOLO_HUB and $KOLO_TOKEN.

-dir and -allow bound what the org may start here. They do not bound what
a running agent may reach: it has this user's whole account. Run a host as
a user that owns only what the org should have.

Whoever runs it does not use it. It takes no input and draws nothing.

`,
}
