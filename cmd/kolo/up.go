package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

// upCmd is the whole of kolo in one command: the hub the org connects to, and
// this machine lending itself to that hub. Everything it needs that does not
// exist yet — the org file, this machine's credential, the first member — it
// makes, so getting started is one line and nothing to paste between steps.
//
// serve, token and host remain for an org whose hub lives somewhere other than
// the machine running the agents. That is the deployment; this is the default.
func upCmd(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	var dirs, allow list
	fs.Var(&dirs, "dir", "a directory the org may run agents in (repeat for more; default the current directory)")
	fs.Var(&allow, "allow", "an agent command line the org may run, flags and all (repeat; default whichever kolo knows and finds installed)")
	orgPath := fs.String("org", config.Path("org.json"), "org file, created if it is not there")
	name := fs.String("name", "", "org name, used only when creating the org file (default this directory's name)")
	addr := fs.String("addr", "", "address the hub listens on (default 0.0.0.0:7300, or :443 with -tls-domain)")
	tlsDomain := fs.String("tls-domain", "", "get and renew a certificate for this domain, and serve https")
	tlsCache := fs.String("tls-cache", hub.DefaultCache(), "where to keep certificates between restarts")
	tlsStaging := fs.Bool("tls-staging", false, "use Let's Encrypt's test service, whose certificates browsers do not trust")
	state := fs.String("state", config.Path("agents.json"), "where to record the agents running here")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo up [-dir <path>...] [-allow <command>]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nWith -tls-domain the hub gets and renews its own certificate, so it can")
		fmt.Fprintln(os.Stderr, "be reached from anywhere. That needs the domain pointed at this machine")
		fmt.Fprintln(os.Stderr, "and ports 80 and 443 open to the internet.")
		fmt.Fprintln(os.Stderr, "\nStarts a hub and lends this machine to it. What the org may run here")
		fmt.Fprintln(os.Stderr, "is bounded by -dir and -allow, but a running agent has this user's")
		fmt.Fprintln(os.Stderr, "whole account. Run it as a user that owns only what the org should have.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	strayOrg(fs)

	// Both of these are refusals in kolo host, where the machine being lent is
	// deliberate and saying so costs nothing. Here they are the common case, so
	// they are answered rather than asked.
	if len(dirs) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dirs = list{cwd}
	}
	if err := resolveDirs(dirs); err != nil {
		return err
	}
	if len(allow) == 0 {
		allow = installedKinds()
		if len(allow) == 0 {
			return fmt.Errorf("no agent command found: kolo knows %s, and none of them are on PATH\n"+
				"Install one, or name it with -allow if it lives somewhere else",
				strings.Join(adapter.Kinds(), ", "))
		}
	}

	// The org, this machine and the first member all have to be on disk before
	// the hub starts: it reads the file once and never again.
	created, err := hub.Init(*orgPath, orgName(*name))
	if err != nil {
		return err
	}
	id, err := machineID()
	if err != nil {
		return err
	}
	// A fresh token every start, replacing the last. Nothing keeps this one —
	// it goes straight to the host half in memory — so there is nothing to lose
	// and nothing left behind by a start that crashed.
	hostToken, hostHash, err := hub.NewToken()
	if err != nil {
		return err
	}
	org, err := hub.SetHost(*orgPath, hub.Host{ID: id, TokenHash: hostHash})
	if err != nil {
		return err
	}
	// An invite rather than a member: whoever started this joins the same way
	// everyone else does, so there is one way in rather than a founder's way and
	// a joiner's way.
	//
	// Only when there is not one already. A host restarted three times before
	// anybody joined used to hand out three live links, none of which the next
	// one withdrew — and minting over a link already pasted somewhere is the
	// other half of that, so a live one is named rather than replaced.
	var invite string
	if len(org.Members) == 0 && len(org.Live(time.Now())) == 0 {
		var err error
		if org, invite, err = hub.AddInvite(*orgPath, "team", time.Now().Add(inviteDays*24*time.Hour), defaultUses); err != nil {
			return err
		}
	}

	domains := split(*tlsDomain)
	if *addr == "" {
		*addr = "0.0.0.0:7300"
		if len(domains) > 0 {
			*addr = ":443"
		}
	}
	s, err := hub.Listen(org, *addr)
	if err != nil {
		return err
	}
	defer s.Close()

	// Where a person types the hub, and where the host half in this process
	// reaches it. They differ once there is a certificate: the org arrives by
	// name over https, and the host half stays on loopback rather than going out
	// to that name and hoping the network answers itself.
	shown := browseURL(s.Addr())
	local := "http://" + net.JoinHostPort("127.0.0.1", portOf(s.Addr()))
	if len(domains) > 0 {
		if err := s.Secure(hub.TLS{Domains: domains, Cache: *tlsCache, Staging: *tlsStaging}); err != nil {
			return err
		}
		shown = "https://" + domains[0]
		loopback, err := s.AlsoServe("127.0.0.1:0")
		if err != nil {
			return err
		}
		local = "http://" + loopback
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		s.Close()
	}()

	// The host half dials the hub half over loopback rather than being called
	// directly, so it is the same host talking to the same hub as it would be on
	// two machines. One process is a deployment, not a second code path.
	agents := host.NewAgents(host.Config{
		Hub:     local,
		Token:   hostToken,
		Dirs:    dirs,
		Allow:   allow,
		Version: version,
	}, *state)
	defer agents.StopAll()

	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	if created {
		fmt.Printf("Created %s for %s.\n", *orgPath, org.Name)
	}
	fmt.Printf("\n%s is up at %s\n", org.Name, shown)
	fmt.Printf("Lending %s, running %s.\n", strings.Join(dirs, " "), strings.Join(allow, " "))
	if !created {
		fmt.Printf("Org in %s, %s.\n", *orgPath, members(len(org.Members)))
	}
	if invite != "" {
		fmt.Printf("\nSend your team this. Opening it is the whole of joining:\n\n    %s\n\n", hub.InviteURL(shown, invite))
		fmt.Printf("%s.\n", bound(defaultUses, inviteDays))
		fmt.Printf("Anyone holding the link can use it: kolo invite -off team withdraws it,\nand kolo who says who came through.\n")
	} else {
		wayIn(org, *orgPath, shown)
	}
	if len(domains) == 0 && !onLoopback(*addr) {
		fmt.Printf("\nThis hub serves plain http, so a member's token crosses the network where\n" +
			"anyone on the path can read it. Fine on a network you trust. To be reached\n" +
			"from anywhere, point a domain at this machine and pass -tls-domain.\n")
	}
	fmt.Println()

	if err := agents.Restore(); err != nil {
		log.Print(err)
	}
	if names := agents.Names(); len(names) > 0 {
		log.Printf("brought back %s", strings.Join(names, " "))
	}
	host.Run(ctx, agents, func(e host.Event) {
		switch {
		case e.Connected:
			log.Printf("lending %s to %s", e.Host, e.Org)
		case e.Err != nil:
			log.Printf("%v; retrying in %s", e.Err, e.Retry.Round(100_000_000))
		}
	})
	// host.Run returns when the signal arrives, which is also what closes the
	// hub; a Serve that ended on its own is the interesting error.
	select {
	case err := <-served:
		return err
	default:
		return nil
	}
}

// What kolo up hands out, and what kolo invite offers unless told otherwise. A
// link goes in a channel, gets forwarded, and outlives the reason it was made,
// so it is bounded twice: long enough to get a team in over a week that has a
// weekend in it, and wide enough for a team rather than a company.
//
// Neither bound is security on its own — whoever holds the link can spend it —
// they are what keeps a leak small enough to notice and undo.
const (
	inviteDays  = 7
	defaultUses = 10
)

// installedKinds is every agent kolo knows about that this machine can actually
// run, so the common case names none of them.
func installedKinds() list {
	var found list
	for _, kind := range adapter.Kinds() {
		if _, err := exec.LookPath(kind); err == nil {
			found = append(found, kind)
		}
	}
	return found
}

// orgName falls back to the directory kolo was started in. It is a label, shown
// in the browser and the log, and one that can be corrected by editing a line.
func orgName(given string) string {
	if given != "" {
		return given
	}
	if cwd, err := os.Getwd(); err == nil {
		if base := filepath.Base(cwd); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return "kolo"
}

// machineID is what this host appears as in the log. The hostname, because that
// is the name the org already has for the machine.
func machineID() (string, error) {
	name, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("this machine has no hostname to be known by; use kolo host -join with an id you choose")
	}
	// Trailing .local and the like: the machine is "devbox" to everyone talking
	// about it, and an id is read by people.
	if base, _, ok := strings.Cut(name, "."); ok {
		name = base
	}
	return name, nil
}

func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "owner"
}

// browseURL is the address to hand to a person. A hub on 0.0.0.0 is reachable at
// this machine's address on the network, which is what its org needs, and never
// at 0.0.0.0.
func browseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if lan := lanAddr(); lan != "" {
			host = lan
		} else {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// lanAddr is this machine's address on the network it is on, or empty if it
// appears to be on none.
func lanAddr() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil {
			continue
		}
		return n.IP.String()
	}
	return ""
}

func onLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// strayOrg says something when the directory being lent has an org file in it
// that kolo is about to ignore.
//
// Earlier versions kept the org beside whatever you were lending, so a machine
// could end up with an org per directory and no way to tell which one it was
// serving. It lives under ~/.kolo now. The old file is left alone rather than
// moved or read: it may be somebody's real org, and picking it up silently is
// the confusion this change is undoing.
func strayOrg(fs *flag.FlagSet) {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "org" {
			set = true
		}
	})
	if set {
		return
	}
	if _, err := os.Stat("org.json"); err != nil {
		return
	}
	cwd, _ := os.Getwd()
	fmt.Printf("Ignoring the org.json in %s. kolo keeps the org in %s now;\n"+
		"pass -org org.json to use that one instead, or delete it.\n", cwd, config.Dir())
}

// wayIn is what an org that somebody has already joined needs on the screen:
// either a link, or the one command that makes one.
//
// A hub nobody can reach is the failure this is for — the previous version
// printed a kolo invite line with two flags in it, which is a thing to retype
// rather than a way in. The link behind a live invite is a hash by now and
// cannot be shown a second time, so the invites that are still out there are
// named instead: minting another over one still in a channel is how a team ends
// up locked out of a link they were using.
func wayIn(org *hub.Org, orgPath, shown string) {
	if live := org.Live(time.Now()); len(live) > 0 {
		fmt.Printf("\nLinks that still work — none of them can be shown again:\n\n")
		out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, v := range live {
			fmt.Fprintf(out, "    %s\t%s\t%s\n", v.ID, usesLeft(v), "until "+v.Expires.Local().Format("Mon 2 Jan 15:04"))
		}
		out.Flush()
	}
	fmt.Printf("\nA link for anyone else:\n\n    %s\n", inviteLine(orgPath, shown))
}

// inviteLine is kolo invite with the flags this hub needs and none it does not,
// so it can be typed off the screen rather than read as a template.
func inviteLine(orgPath, shown string) string {
	cmd := "kolo invite"
	if orgPath != config.Path("org.json") {
		cmd += " -org " + orgPath
	}
	if shown != defaultHubURL {
		cmd += " -hub " + shown
	}
	return cmd
}

// members counts them the way somebody reads a line about their own org.
func members(n int) string {
	switch n {
	case 0:
		return "nobody in it yet"
	case 1:
		return "1 member"
	}
	return fmt.Sprintf("%d members", n)
}
